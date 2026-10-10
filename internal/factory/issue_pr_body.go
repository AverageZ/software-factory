package factory

import (
	"fmt"
	"io"
	"os"
	"strings"
)

const issuePRBodyLimit = 16 << 10

func issuePRBodyFilename(id string) string { return ".factory-pr-body-" + id + ".md" }

// The agent's review-facing report is separate from the code checkpoint. Persist
// it on the issue job before removing the handoff file, so publication retries
// reuse the same description instead of rerunning the agent.
func readIssuePRBody(worktree, id string) (string, error) {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return "", fmt.Errorf("open PR body worktree: %w", err)
	}
	defer root.Close()
	file, err := root.Open(issuePRBodyFilename(id))
	if err != nil {
		return "", fmt.Errorf("open agent PR body: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > issuePRBodyLimit {
		return "", fmt.Errorf("agent PR body must be a regular file of at most %d bytes", issuePRBodyLimit)
	}
	data, err := io.ReadAll(io.LimitReader(file, issuePRBodyLimit+1))
	if err != nil {
		return "", err
	}
	if len(data) > issuePRBodyLimit {
		return "", fmt.Errorf("agent PR body exceeds %d bytes", issuePRBodyLimit)
	}
	body := strings.TrimSpace(string(data))
	evidence := strings.Index(body, "\n## Evidence\n")
	danger := strings.Index(body, "\n## Merge Danger\n")
	if !strings.HasPrefix(body, "## Summary\n") || evidence < len("## Summary\n") || danger <= evidence+len("\n## Evidence\n") {
		return "", fmt.Errorf("agent PR body must have nonempty Summary, Evidence, and Merge Danger sections in order")
	}
	if strings.TrimSpace(body[len("## Summary\n"):evidence]) == "" || strings.TrimSpace(body[evidence+len("\n## Evidence\n"):danger]) == "" || strings.TrimSpace(body[danger+len("\n## Merge Danger\n"):]) == "" {
		return "", fmt.Errorf("agent PR body must have nonempty Summary, Evidence, and Merge Danger sections in order")
	}
	proof := strings.ToLower(body[evidence+len("\n## Evidence\n"):danger])
	risk := strings.ToLower(body[danger+len("\n## Merge Danger\n"):])
	if !strings.Contains(proof, "before") || !strings.Contains(proof, "after") ||
		(!strings.Contains(risk, "one-way") && !strings.Contains(risk, "two-way")) || !strings.Contains(risk, "blast radius") {
		return "", fmt.Errorf("agent PR body must state before/after evidence, door type, and blast radius")
	}
	return body, nil
}

func removeIssuePRBody(worktree, id string) error {
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return fmt.Errorf("open PR body worktree: %w", err)
	}
	defer root.Close()
	if err := root.Remove(issuePRBodyFilename(id)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove agent PR body before checkpoint: %w", err)
	}
	return nil
}

func formatIssuePRBody(j savedIssueJob) string {
	return fmt.Sprintf("%s\n\nCloses #%d\n\n<details>\n<summary>Factory checkpoint</summary>\n\nRun: `%s`  \nBase: `%s`  \nCandidate: `%s`\n\n</details>\n\n%s", j.PRBody, j.Number, j.RunID, j.BaseSHA, j.HeadSHA, issuePRMarker(j.ID))
}
