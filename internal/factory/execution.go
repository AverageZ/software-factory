package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const outputLimit = 64 << 10

type executionResult struct {
	outputs   map[string]Value
	artifacts []string
	err       error
}
type transcript struct {
	mu   sync.Mutex
	file *os.File
	err  error
}

func (t *transcript) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return 0, t.err
	}
	n, err := t.file.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		t.err = err
	}
	return n, err
}

type capturedStream struct {
	log       *transcript
	data      bytes.Buffer
	truncated bool
}

func (s *capturedStream) Write(p []byte) (int, error) {
	n, err := s.log.Write(p)
	remaining := outputLimit - s.data.Len()
	count := len(p)
	if count > remaining {
		count = remaining
		s.truncated = true
	}
	if count > 0 {
		s.data.Write(p[:count])
	}
	return n, err
}
func (s *capturedStream) String() string {
	if s.truncated {
		return s.data.String() + "\n[Output truncated at 64 KiB; the complete transcript is in the node log.]"
	}
	return s.data.String()
}
func logRelative(runID, nodeID string, attempt int) string {
	return filepath.Join("runs", runID, nodeID, fmt.Sprintf("attempt-%d.log", attempt))
}
func (e *Engine) perform(ctx context.Context, r savedRun, n WorkflowNode, inputs map[string]Value, attempt int) (result executionResult) {
	result.outputs = map[string]Value{}
	result.artifacts = []string{}
	path := filepath.Join(e.data, logRelative(r.Run.ID, n.ID, attempt))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		result.err = err
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		result.err = fmt.Errorf("open execution transcript: %w", err)
		return
	}
	log := &transcript{file: file}
	defer func() { result.err = errors.Join(result.err, log.err, file.Sync(), file.Close()) }()
	if seconds, ok := n.Config["timeoutSeconds"].(float64); ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
		defer cancel()
	}
	defer func() {
		if result.err != nil {
			return
		}
		paths, err := artifactPaths(n)
		if err != nil {
			result.err = err
			return
		}
		if len(paths) == 0 {
			return
		}
		for _, repository := range r.Project.Repositories {
			if repository.ID == r.Binding.Repositories[n.Repository] {
				result.artifacts, result.err = e.copyArtifacts(ctx, repository.Path, r.Run.ID, n.ID, attempt, paths)
				return
			}
		}
		result.err = fmt.Errorf("artifact repository slot %s is not bound", n.Repository)
	}()
	if err := validateAgentSettings(n); err != nil {
		result.err = err
		return
	}
	until, looping := n.Config["until"]
	limit := 1
	if looping {
		limit = defaultAgentIterations
		if configured, ok := n.Config["maxIterations"].(float64); ok {
			limit = int(configured)
		}
	}
	for iteration := 1; iteration <= limit; iteration++ {
		if err := ctx.Err(); err != nil {
			result.err = err
			return
		}
		if looping {
			if _, err := fmt.Fprintf(log, "\n[Factory: completion iteration %d of %d]\n", iteration, limit); err != nil {
				result.err = err
				return
			}
		}
		result = e.performPass(ctx, r, n, inputs, log)
		if result.err != nil || !looping {
			return
		}
		if evaluateBranchConditions(until.(map[string]any), result.outputs) {
			return
		}
		if iteration == limit {
			result.err = fmt.Errorf("agent completion condition was not met after %d iterations", limit)
			_, err := fmt.Fprintf(log, "\n[Factory: %v]\n", result.err)
			result.err = errors.Join(result.err, err)
			return
		}
		// Do not mutate the snapshotted inputs. A successful but incomplete
		// result is feedback, not a retry of a failed process.
		if iteration == 1 {
			next := make(map[string]Value, len(inputs)+1)
			for key, value := range inputs {
				next[key] = value
			}
			inputs = next
		}
		inputs["previousResult"] = result.outputs["result"]
	}
	return
}

func (e *Engine) performPass(ctx context.Context, r savedRun, n WorkflowNode, inputs map[string]Value, log *transcript) (result executionResult) {
	result.outputs = map[string]Value{}
	result.artifacts = []string{}
	if n.Kind == "integration" {
		result.outputs, result.err = performHTTP(ctx, n, log)
		return
	}
	repositoryID := r.Binding.Repositories[n.Repository]
	cwd := ""
	for _, repository := range r.Project.Repositories {
		if repository.ID == repositoryID {
			cwd = repository.Path
			break
		}
	}
	if cwd == "" {
		result.err = fmt.Errorf("repository slot %s is not bound", n.Repository)
		return
	}
	var args []string
	var err error
	if n.Kind == "agent" {
		prompt := configString(n, "prompt")
		if outputJSON, _ := n.Config["outputJson"].(bool); outputJSON {
			prompt += "\n\nReturn one complete JSON object on stdout, without Markdown fences or surrounding prose."
		}
		if until, ok := n.Config["until"]; ok {
			condition, err := json.Marshal(until)
			if err != nil {
				result.err = err
				return
			}
			prompt += "\n\nFactory evaluates this completion condition against the result envelope: " + string(condition) + ". If previousResult is supplied, continue from that successful but incomplete iteration. Do not claim completion unless the work and checks are complete."
		}
		if len(inputs) > 0 {
			encoded, err := json.MarshalIndent(inputs, "", "  ")
			if err != nil {
				result.err = err
				return
			}
			prompt += "\n\nWorkflow inputs (typed JSON data):\n" + string(encoded)
		}
		// Only execution presentation/model flags are supplied. HOME, auth,
		// configuration, skills and repository rule discovery remain inherited.
		model := configString(n, "model")
		if model == "" {
			model = r.Project.Harness.Model
		}
		args = []string{r.Project.Harness.Binary, "--model", model, "--no-title", "--no-pty", "-p", prompt}
	} else {
		args, err = commandArgs(n)
		if err != nil {
			result.err = err
			return
		}
	}
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = cwd
	encoded, err := json.Marshal(inputs)
	if err != nil {
		result.err = err
		return
	}
	cmd.Env = append(withoutEnv(os.Environ(), "FACTORY_INPUTS"), "FACTORY_INPUTS="+string(encoded))
	if r.IssueJobID != "" {
		environment := cmd.Env[:0]
		for _, setting := range cmd.Env {
			if !strings.HasPrefix(setting, "GIT_") {
				environment = append(environment, setting)
			}
		}
		cmd.Env = environment
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the process group, not just the harness: tools and grandchildren must
	// not survive a cancelled run or daemon shutdown.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	stdout := &capturedStream{log: log}
	stderr := &capturedStream{log: log}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err = cmd.Run()
	if r.IssueJobID != "" && cmd.Process != nil {
		// A successful harness must not leave tools holding this workspace open.
		// The lease remains held until this process group has been terminated.
		if killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); killErr != nil && !errors.Is(killErr, syscall.ESRCH) {
			err = errors.Join(err, fmt.Errorf("stop workspace process group: %w", killErr))
		}
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			exitCode = exit.ExitCode()
		}
		if ctx.Err() != nil {
			err = fmt.Errorf("execution stopped: %w", ctx.Err())
		}
		result.err = err
	}
	envelope := map[string]any{"exitCode": exitCode, "stdout": stdout.String(), "stderr": stderr.String()}
	if outputJSON, _ := n.Config["outputJson"].(bool); outputJSON && result.err == nil {
		var data map[string]any
		switch {
		case stdout.truncated:
			result.err = fmt.Errorf("structured task output exceeded the 64 KiB capture limit")
		default:
			if err := json.Unmarshal(stdout.data.Bytes(), &data); err != nil {
				result.err = fmt.Errorf("structured task output must be a complete JSON object: %w", err)
			} else if data == nil {
				result.err = fmt.Errorf("structured task output must be a JSON object, not null")
			} else {
				envelope["data"] = data
			}
		}
	}
	result.outputs["result"] = Value{Type: nodeOutputType(n), Value: envelope}
	if result.err != nil {
		_, writeErr := fmt.Fprintf(log, "\n[Factory: %v]\n", result.err)
		result.err = errors.Join(result.err, writeErr)
		return
	}
	return
}
func withoutEnv(env []string, key string) []string {
	result := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, key+"=") {
			result = append(result, item)
		}
	}
	return result
}
func performHTTP(ctx context.Context, n WorkflowNode, log *transcript) (map[string]Value, error) {
	outputs := map[string]Value{}
	method := configString(n, "method")
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if value, ok := n.Config["body"]; ok {
		data, err := json.Marshal(value)
		if err != nil {
			return outputs, err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, configString(n, "url"), body)
	if err != nil {
		return outputs, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if headers, ok := n.Config["headers"].(map[string]any); ok {
		for key, value := range headers {
			request.Header.Set(key, value.(string))
		}
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		_, writeErr := fmt.Fprintf(log, "HTTP request failed: %v\n", err)
		return outputs, errors.Join(err, writeErr)
	}
	defer response.Body.Close()
	if _, err := fmt.Fprintf(log, "HTTP %s\n", response.Status); err != nil {
		return outputs, err
	}
	stream := &capturedStream{log: log}
	_, readErr := io.Copy(stream, response.Body)
	var value any = stream.String()
	if !stream.truncated {
		var decoded any
		if json.Unmarshal(stream.data.Bytes(), &decoded) == nil {
			value = decoded
		}
	}
	outputs["result"] = Value{Type: nodeOutputType(n), Value: map[string]any{"status": response.StatusCode, "body": value}}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return outputs, errors.Join(fmt.Errorf("HTTP integration returned %s", response.Status), readErr)
	}
	return outputs, readErr
}
func (e *Engine) copyArtifacts(ctx context.Context, repo, runID, nodeID string, attempt int, paths []string) ([]string, error) {
	names := []string{}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return names, err
	}
	defer root.Close()
	destination := filepath.Join(e.data, "runs", runID, nodeID, "artifacts")
	if err := os.MkdirAll(destination, 0700); err != nil {
		return names, err
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return names, err
		}
		// Resolve for clear diagnostics, then open through os.Root so a concurrent
		// symlink replacement cannot race the containment check.
		resolved, err := filepath.EvalSymlinks(filepath.Join(repo, path))
		if err != nil {
			return names, fmt.Errorf("artifact %s: %w", path, err)
		}
		relative, err := filepath.Rel(repo, resolved)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return names, fmt.Errorf("artifact %s escapes repository", path)
		}
		input, err := root.Open(relative)
		if err != nil {
			return names, fmt.Errorf("artifact %s: %w", path, err)
		}
		info, err := input.Stat()
		if err != nil {
			input.Close()
			return names, err
		}
		if !info.Mode().IsRegular() {
			input.Close()
			return names, fmt.Errorf("artifact %s is not a regular file", path)
		}
		name := "attempt-" + strconv.Itoa(attempt) + "-" + filepath.Base(path)
		output, err := os.OpenFile(filepath.Join(destination, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			input.Close()
			return names, err
		}
		_, copyErr := io.Copy(output, input)
		err = errors.Join(copyErr, input.Close(), output.Sync(), output.Close())
		if err != nil {
			return names, fmt.Errorf("artifact %s: %w", path, err)
		}
		names = append(names, name)
	}
	return names, nil
}
func (e *Engine) LogFile(runID, nodeID, attemptText string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return "", missing("run")
	}
	x, ok := r.Run.Nodes[nodeID]
	if !ok {
		return "", missing("node")
	}
	attempt := x.Attempt
	if attemptText != "" {
		var err error
		attempt, err = strconv.Atoi(attemptText)
		if err != nil || attempt < 1 || attempt > x.Attempt {
			return "", invalid(fmt.Errorf("invalid attempt"))
		}
	}
	if x.LogPath == "" || attempt == 0 {
		return "", missing("node log")
	}
	return filepath.Join(e.data, logRelative(runID, nodeID, attempt)), nil
}
func (e *Engine) ArtifactFile(runID, nodeID, name string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[runID]
	if !ok {
		return "", missing("run")
	}
	x, ok := r.Run.Nodes[nodeID]
	if !ok {
		return "", missing("node")
	}
	if name == "" || filepath.Base(name) != name {
		return "", missing("artifact")
	}
	for _, artifact := range x.Artifacts {
		if artifact == name {
			return filepath.Join(e.data, "runs", runID, nodeID, "artifacts", name), nil
		}
	}
	return "", missing("artifact")
}
