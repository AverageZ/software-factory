package factory

import (
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"time"
)

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validID(id string) error {
	if !identifier.MatchString(id) {
		return fmt.Errorf("invalid identifier %q (use letters, digits, underscore or hyphen)", id)
	}
	return nil
}
func validType(t string) bool {
	switch t {
	case "string", "number", "boolean", "object", "array", "Issue", "Diagnosis", "Plan", "Patch", "TestResult", "ReviewFinding", "Approval", "PullRequest":
		return true
	}
	return false
}
func validateValue(v Value) error {
	if !validType(v.Type) {
		return fmt.Errorf("unknown value type %q", v.Type)
	}
	ok := false
	switch v.Type {
	case "string":
		_, ok = v.Value.(string)
	case "number":
		_, ok = v.Value.(float64)
	case "boolean":
		_, ok = v.Value.(bool)
	case "array":
		_, ok = v.Value.([]any)
	default:
		_, ok = v.Value.(map[string]any)
	}
	if !ok {
		return fmt.Errorf("value does not match type %s", v.Type)
	}
	return nil
}
func validateProject(p *Project) error {
	if err := validID(p.ID); err != nil {
		return err
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("project name is required")
	}
	if p.Harness.Binary == "" {
		p.Harness.Binary = "omp"
	}
	if p.Harness.Model == "" {
		p.Harness.Model = "anthropic/claude-haiku-4-5"
	}
	if p.Policy.MaxParallel == 0 {
		p.Policy.MaxParallel = 4
	}
	if p.Policy.MaxParallel < 1 {
		return fmt.Errorf("maxParallel must be positive")
	}
	if p.Repositories == nil {
		p.Repositories = []Repository{}
	}
	ids := map[string]bool{}
	for i := range p.Repositories {
		r := &p.Repositories[i]
		if err := validID(r.ID); err != nil {
			return err
		}
		if ids[r.ID] {
			return fmt.Errorf("duplicate repository %s", r.ID)
		}
		ids[r.ID] = true
		root, err := filepath.Abs(r.Path)
		if err != nil {
			return err
		}
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return fmt.Errorf("repository %s: %w", r.ID, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--show-toplevel")
		out, err := cmd.Output()
		cancel()
		if err != nil {
			return fmt.Errorf("repository %s is not a local Git root: %w", r.ID, err)
		}
		actual, err := filepath.EvalSymlinks(strings.TrimSpace(string(out)))
		if err != nil || actual != root {
			return fmt.Errorf("repository %s path must be the Git root", r.ID)
		}
		r.Path = root
	}
	return nil
}
func configString(n WorkflowNode, key string) string {
	value, _ := n.Config[key].(string)
	return value
}
func commandArgs(n WorkflowNode) ([]string, error) {
	items, ok := n.Config["command"].([]any)
	if !ok || len(items) == 0 {
		return nil, fmt.Errorf("node %s requires a nonempty command argv array", n.ID)
	}
	args := make([]string, len(items))
	for i, item := range items {
		var ok bool
		args[i], ok = item.(string)
		if !ok {
			return nil, fmt.Errorf("node %s command arguments must be strings", n.ID)
		}
	}
	if args[0] == "" {
		return nil, fmt.Errorf("node %s command executable is empty", n.ID)
	}
	return args, nil
}
func artifactPaths(n WorkflowNode) ([]string, error) {
	value, exists := n.Config["artifacts"]
	if !exists {
		return nil, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("node %s artifacts must be an array", n.ID)
	}
	paths := make([]string, 0, len(items))
	names := map[string]bool{}
	for _, item := range items {
		path, ok := item.(string)
		if !ok || path == "" || filepath.IsAbs(path) || path == "." || filepath.Clean(path) != path || path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("node %s artifact must be a clean repository-relative file path", n.ID)
		}
		name := filepath.Base(path)
		if names[name] {
			return nil, fmt.Errorf("node %s artifact basenames must be unique", n.ID)
		}
		names[name] = true
		paths = append(paths, path)
	}
	return paths, nil
}
func validateWorkflow(w Workflow, definitions map[string]Workflow) error {
	if err := validID(w.ID); err != nil {
		return err
	}
	if strings.TrimSpace(w.Name) == "" {
		return fmt.Errorf("workflow name is required")
	}
	if len(w.Nodes) == 0 {
		return fmt.Errorf("workflow must have at least one node")
	}
	nodes := map[string]WorkflowNode{}
	for name, typ := range w.Inputs {
		if err := validID(name); err != nil {
			return err
		}
		if !validType(typ) {
			return fmt.Errorf("unknown workflow input type %q", typ)
		}
	}
	for _, n := range w.Nodes {
		if err := validID(n.ID); err != nil {
			return err
		}
		if n.ID == "inputs" {
			return fmt.Errorf("node ID inputs is reserved for workflow input references")
		}
		if nodes[n.ID].ID != "" {
			return fmt.Errorf("duplicate node %s", n.ID)
		}
		nodes[n.ID] = n
		if !validType(nodeOutputType(n)) {
			return fmt.Errorf("node %s has unknown output type", n.ID)
		}
		if n.OutputType != "" && n.OutputType != nodeOutputType(n) {
			return fmt.Errorf("node %s output type is fixed to %s", n.ID, nodeOutputType(n))
		}
		if n.OutputType != "" && n.Kind != "branch" && n.Kind != "approval" {
			if n.OutputType == "string" || n.OutputType == "number" || n.OutputType == "boolean" || n.OutputType == "array" {
				return fmt.Errorf("node %s result is an object, not %s", n.ID, n.OutputType)
			}
		}
		if timeout, exists := n.Config["timeoutSeconds"]; exists {
			seconds, ok := timeout.(float64)
			if !ok || seconds <= 0 || seconds > 604800 {
				return fmt.Errorf("node %s timeoutSeconds must be positive and at most 604800", n.ID)
			}
		}
		switch n.Kind {
		case "command", "tool", "validation":
			if n.Repository == "" {
				return fmt.Errorf("node %s requires a repository slot", n.ID)
			}
			if _, err := commandArgs(n); err != nil {
				return err
			}
			if _, err := artifactPaths(n); err != nil {
				return err
			}
		case "agent":
			if n.Repository == "" || strings.TrimSpace(configString(n, "prompt")) == "" {
				return fmt.Errorf("agent %s requires repository and prompt", n.ID)
			}
			if _, err := artifactPaths(n); err != nil {
				return err
			}
		case "approval":
			if strings.TrimSpace(configString(n, "message")) == "" {
				return fmt.Errorf("approval %s requires a message", n.ID)
			}
		case "branch":
			if _, ok := n.Inputs[configString(n, "input")]; !ok {
				return fmt.Errorf("branch %s must name one of its inputs", n.ID)
			}
			if _, exists := n.Config["equals"]; !exists {
				return fmt.Errorf("branch %s requires equals", n.ID)
			}
		case "workflow":
			child, exists := definitions[configString(n, "workflowId")]
			if !exists {
				return fmt.Errorf("node %s references missing workflow", n.ID)
			}
			if len(n.Inputs) != len(child.Inputs) {
				return fmt.Errorf("workflow node %s must supply each child input", n.ID)
			}
			for name, typ := range child.Inputs {
				input, ok := n.Inputs[name]
				if !ok || input.Type != typ {
					return fmt.Errorf("workflow node %s input %s must have type %s", n.ID, name, typ)
				}
			}
		case "integration":
			u, err := url.Parse(configString(n, "url"))
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("integration %s requires an HTTP(S) URL", n.ID)
			}
			if headers, exists := n.Config["headers"]; exists {
				fields, ok := headers.(map[string]any)
				if !ok {
					return fmt.Errorf("integration headers must be an object")
				}
				for _, value := range fields {
					if _, ok := value.(string); !ok {
						return fmt.Errorf("integration header values must be strings")
					}
				}
			}
			if method, exists := n.Config["method"]; exists {
				text, ok := method.(string)
				if !ok || text == "" || strings.ContainsAny(text, " \t\r\n") {
					return fmt.Errorf("integration method must be an HTTP method")
				}
			}
		case "decision", "parallel":
		default:
			return fmt.Errorf("unknown node kind %q", n.Kind)
		}
	}
	incoming := map[string][]string{}
	outgoing := map[string][]string{}
	degree := map[string]int{}
	edgeIDs := map[string]bool{}
	pairs := map[string]bool{}
	for _, edge := range w.Edges {
		if err := validID(edge.ID); err != nil {
			return err
		}
		if edgeIDs[edge.ID] {
			return fmt.Errorf("duplicate edge %s", edge.ID)
		}
		edgeIDs[edge.ID] = true
		if nodes[edge.Source].ID == "" || nodes[edge.Target].ID == "" {
			return fmt.Errorf("edge %s references missing node", edge.ID)
		}
		pair := edge.Source + "/" + edge.Target + "/" + edge.When
		if pairs[pair] {
			return fmt.Errorf("duplicate graph edge %s", edge.ID)
		}
		pairs[pair] = true
		if edge.When != "" && (nodes[edge.Source].Kind != "branch" || (edge.When != "true" && edge.When != "false")) {
			return fmt.Errorf("edge %s conditional routing requires a branch and true/false", edge.ID)
		}
		incoming[edge.Target] = append(incoming[edge.Target], edge.Source)
		outgoing[edge.Source] = append(outgoing[edge.Source], edge.Target)
		degree[edge.Target]++
	}
	queue := []string{}
	for id := range nodes {
		if degree[id] == 0 {
			queue = append(queue, id)
		}
	}
	count := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		count++
		for _, next := range outgoing[id] {
			degree[next]--
			if degree[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if count != len(nodes) {
		return fmt.Errorf("workflow graph must be acyclic")
	}
	for _, n := range w.Nodes {
		ancestors := map[string]bool{}
		var visit func(string)
		visit = func(id string) {
			for _, previous := range incoming[id] {
				if !ancestors[previous] {
					ancestors[previous] = true
					visit(previous)
				}
			}
		}
		visit(n.ID)
		for name, input := range n.Inputs {
			if err := validID(name); err != nil {
				return err
			}
			if !validType(input.Type) {
				return fmt.Errorf("node %s input %s has unknown type", n.ID, name)
			}
			literal := input.literal || input.Value != nil
			if (input.From != "") == literal {
				return fmt.Errorf("node %s input %s must have exactly one of value/from", n.ID, name)
			}
			if literal {
				if err := validateValue(Value{Type: input.Type, Value: input.Value}); err != nil {
					return fmt.Errorf("node %s input %s: %w", n.ID, name, err)
				}
				continue
			}
			parts := strings.Split(input.From, ".")
			if len(parts) != 2 {
				return fmt.Errorf("invalid input reference %q", input.From)
			}
			var typ string
			if parts[0] == "inputs" {
				typ = w.Inputs[parts[1]]
			} else {
				if !ancestors[parts[0]] || parts[1] != "result" {
					return fmt.Errorf("node %s input %s must reference an ancestor's result output", n.ID, name)
				}
				typ = nodeOutputType(nodes[parts[0]])
			}
			if typ == "" || typ != input.Type {
				return fmt.Errorf("node %s input %s type does not match %s", n.ID, name, input.From)
			}
		}
	}
	return nil
}
func validateDefinitions(definitions map[string]Workflow) error {
	states := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if states[id] == 1 {
			return fmt.Errorf("recursive child workflow reference at %s", id)
		}
		if states[id] == 2 {
			return nil
		}
		states[id] = 1
		w, ok := definitions[id]
		if !ok {
			return fmt.Errorf("missing workflow %s", id)
		}
		if err := validateWorkflow(w, definitions); err != nil {
			return fmt.Errorf("workflow %s: %w", id, err)
		}
		for _, n := range w.Nodes {
			if n.Kind == "workflow" {
				if err := visit(configString(n, "workflowId")); err != nil {
					return err
				}
			}
		}
		states[id] = 2
		return nil
	}
	for id := range definitions {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
func validateBinding(b Binding, p Project, definitions map[string]Workflow, complete bool) error {
	if err := validID(b.ID); err != nil {
		return err
	}
	if strings.TrimSpace(b.Name) == "" {
		return fmt.Errorf("binding name is required")
	}
	w, ok := definitions[b.WorkflowID]
	if !ok {
		return fmt.Errorf("workflow not found")
	}
	repos := map[string]bool{}
	for _, r := range p.Repositories {
		repos[r.ID] = true
	}
	for slot, id := range b.Repositories {
		if err := validID(slot); err != nil {
			return err
		}
		if !repos[id] {
			return fmt.Errorf("repository slot %s references missing project repository", slot)
		}
	}
	seen := map[string]bool{}
	var visit func(Workflow) error
	visit = func(w Workflow) error {
		if seen[w.ID] {
			return nil
		}
		seen[w.ID] = true
		for _, n := range w.Nodes {
			if n.Repository != "" && b.Repositories[n.Repository] == "" {
				return fmt.Errorf("repository slot %s is unbound", n.Repository)
			}
			if n.Kind == "workflow" {
				if err := visit(definitions[configString(n, "workflowId")]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(w); err != nil {
		return err
	}
	return validateInputs(w, b.Inputs, complete)
}
func validateInputs(w Workflow, values map[string]Value, complete bool) error {
	for name, value := range values {
		if w.Inputs[name] != value.Type {
			return fmt.Errorf("input %s has unknown name or mismatched type", name)
		}
		if err := validateValue(value); err != nil {
			return fmt.Errorf("input %s: %w", name, err)
		}
	}
	if complete {
		for name := range w.Inputs {
			if _, ok := values[name]; !ok {
				return fmt.Errorf("required workflow input %s is missing", name)
			}
		}
	}
	return nil
}
func equalJSON(a, b any) bool { return reflect.DeepEqual(a, b) }
func resolveInputs(r *savedRun, n WorkflowNode) (map[string]Value, error) {
	values := map[string]Value{}
	for name, input := range n.Inputs {
		value := Value{Type: input.Type, Value: input.Value}
		if input.From != "" {
			parts := strings.Split(input.From, ".")
			var ok bool
			if parts[0] == "inputs" {
				value, ok = r.Run.Inputs[parts[1]]
			} else {
				source := r.Run.Nodes[parts[0]]
				value, ok = source.Outputs[parts[1]]
				ok = ok && source.Status == "succeeded"
			}
			if !ok {
				return nil, fmt.Errorf("required upstream input %s (%s) is unavailable", name, input.From)
			}
		}
		if value.Type != input.Type {
			return nil, fmt.Errorf("input %s type mismatch", name)
		}
		if err := validateValue(value); err != nil {
			return nil, fmt.Errorf("input %s: %w", name, err)
		}
		values[name] = value
	}
	return values, nil
}
