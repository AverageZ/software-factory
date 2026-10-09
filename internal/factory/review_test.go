package factory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRevisionPublishesExistingBranchAndDeduplicates(t *testing.T) {
	runReviewRevision(t, false, false)
}

func TestReviewRevisionStopsWhenAuthorizationIsRevoked(t *testing.T) {
	runReviewRevision(t, true, false)
}

func TestTrustedReviewSelectsWorkflowAndRevisesExistingPR(t *testing.T) {
	runReviewRevision(t, false, true)
}

func runReviewRevision(t *testing.T, revoke, autoSelect bool) {
	f := newWorkspaceFixture(t)
	ctx := context.Background()
	release, err := acquireIssueLease(f.repository, "original")
	if err != nil {
		t.Fatal(err)
	}
	w, err := prepareIssueWorkspace(ctx, f.data, f.repository, "original", f.remote, "main")
	if err != nil {
		t.Fatal(err)
	}
	w = workspaceTestCheckpoint(t, f, w)
	if err = pushIssueWorkspace(ctx, f.repository, w, f.remote); err != nil {
		t.Fatal(err)
	}
	if err = cleanupIssueWorkspace(ctx, f.data, f.repository, "original", w); err != nil {
		t.Fatal(err)
	}
	release()
	original := w.HeadSHA
	workspaceTestGit(t, f.repository, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(f.data, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := `#!/bin/bash
args=()
for arg in "$@"; do
 if [[ "$arg" == "https://github.com/owner/repo.git" ]]; then arg="$FACTORY_TEST_REMOTE"; fi
 args+=("$arg")
done
exec "$FACTORY_TEST_GIT" "${args[@]}"
`
	workspaceTestWrite(t, filepath.Join(bin, "git"), []byte(wrapper))
	if err = os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	gh := `#!/bin/sh
case "$*" in
 *'repos/owner/repo'*) printf '%s' '{"id":123,"name":"repo","owner":{"login":"owner"},"default_branch":"main","archived":false,"disabled":false,"has_issues":true,"permissions":{"push":true}}' ;;
 *'repositories/123/issues?'*) printf '%s' '[[]]' ;;
 *'repositories/123/pulls/42/comments?'*) printf '%s' '[[]]' ;;
 *'repositories/123/pulls/42/reviews?'*) printf '%s' '[[]]' ;;
 *'repositories/123/issues/42/comments?'*) printf '%s' '[[{"id":99,"body":"Please adjust the tracked file","html_url":"https://github.com/owner/repo/pull/42#issuecomment-99","updated_at":"2026-01-01T00:00:00Z","user":{"login":"reviewer","type":"User"}}]]' ;;
 *'repositories/123/collaborators/reviewer/permission'*) printf '%s' '{"permission":"write"}' ;;
 *'--method POST repositories/123/issues/42/labels'*)
  printf 'selected' > "$FACTORY_TEST_SELECTED"
  printf '%s' '[{"name":"agent:run"},{"name":"agent/workflow:review"}]' ;;
 *'repositories/123/issues/42'*)
  if [ -n "$FACTORY_TEST_REVOKE" ] && [ -f "$FACTORY_TEST_REVOKE" ]; then
    printf '%s' '{"number":42,"state":"open","pull_request":{},"labels":[]}'
  elif [ -n "$FACTORY_TEST_SELECTED" ] && [ ! -f "$FACTORY_TEST_SELECTED" ]; then
    printf '%s' '{"number":42,"state":"open","pull_request":{},"labels":[{"name":"agent:run"}]}'
  else
    printf '%s' '{"number":42,"state":"open","pull_request":{},"labels":[{"name":"agent:run"},{"name":"agent/workflow:review"}]}'
  fi ;;
 *'repositories/123/pulls/42'*) printf '{"number":42,"html_url":"https://github.com/owner/repo/pull/42","state":"open","head":{"ref":"factory/issue-original","sha":"%s","repo":{"id":123}},"base":{"ref":"main","repo":{"id":123}}}' "$FACTORY_TEST_HEAD" ;;
 *) exit 1 ;;
esac
`
	workspaceTestWrite(t, filepath.Join(bin, "gh"), []byte(gh))
	if err = os.Chmod(filepath.Join(bin, "gh"), 0700); err != nil {
		t.Fatal(err)
	}
	harness := `#!/bin/sh
if [ -n "$FACTORY_TEST_REVOKE" ]; then touch "$FACTORY_TEST_REVOKE"; fi
printf 'review revision\n' > tracked.txt
printf 'validated revision\n'
`
	workspaceTestWrite(t, filepath.Join(bin, "fake-harness"), []byte(harness))
	if err = os.Chmod(filepath.Join(bin, "fake-harness"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FACTORY_TEST_GIT", gitBinary)
	t.Setenv("FACTORY_TEST_REMOTE", f.remote)
	t.Setenv("FACTORY_TEST_HEAD", original)
	if revoke {
		t.Setenv("FACTORY_TEST_REVOKE", filepath.Join(f.data, "authorization-removed"))
	}
	if autoSelect {
		t.Setenv("FACTORY_TEST_SELECTED", filepath.Join(f.data, "review-selected"))
	}
	s, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	operation, stop := context.WithCancel(context.Background())
	e := &Engine{store: s, data: f.data, ctx: operation, stop: stop, projects: map[string]Project{}, workflows: map[string]Workflow{}, bindings: map[string]Binding{}, intakes: map[string]intakeConfig{}, intakeBusy: map[string]bool{}, issueJobs: map[string]savedIssueJob{}, reviewJobs: map[string]savedReviewJob{}, runs: map[string]savedRun{}, active: map[string]activeExecution{}, issueActive: map[string]bool{}, errors: make(chan error, 1)}
	defer e.Close()
	p := Project{ID: "p", Name: "project", Repositories: []Repository{{ID: "r", Name: "repo", Path: f.repository}}, Harness: Harness{Binary: filepath.Join(bin, "fake-harness"), Model: "fake"}, Policy: Policy{MaxParallel: 1}}
	workflow := Workflow{ID: "review", Name: "Review revision", Inputs: map[string]string{"pullRequest": "object", "feedback": "array"}, Nodes: []WorkflowNode{{ID: "revise", Name: "Revise", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "Use the trusted review items and validate the change.", "feedbackSources": []any{"conversation"}}, Inputs: map[string]Input{"pullRequest": {Type: "object", From: "inputs.pullRequest"}, "feedback": {Type: "array", From: "inputs.feedback"}}}}}
	b := Binding{ID: "binding", Name: "Revision", ProjectID: "p", WorkflowID: "review", Repositories: map[string]string{"source": "r"}, Inputs: map[string]Value{}}
	e.projects[p.ID] = p
	e.workflows[workflow.ID] = workflow
	e.bindings[b.ID] = b
	c := GitHubConnection{ID: 123, Owner: "owner", Name: "repo", DefaultBranch: "main", CanPush: true}
	config := intakeConfig{Enabled: true, ProjectID: "p", RepositoryID: "r", RepositoryPath: f.repository, Connection: c, ReviewBindingID: b.ID}
	e.intakes[githubKey("p", "r")] = config
	issue := savedIssueJob{IssueJob: IssueJob{ID: "original", ProjectID: "p", RepositoryID: "r", GitHubRepositoryID: 123, Number: 5, Status: "published", Branch: w.Branch, HeadSHA: original, PullRequestNumber: 42, PullRequestURL: "https://github.com/owner/repo/pull/42"}, Repository: p.Repositories[0], Project: p, Connection: c, BaseBranch: "main"}
	e.issueJobs[issue.ID] = issue
	if _, err = e.pollRepositoryIntake(ctx, "p", "r"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("review revision did not finish")
	}
	var revision savedReviewJob
	for _, j := range e.reviewJobs {
		revision = j
	}
	if revoke {
		if revision.Status != "failed" || revision.RunID == "" || revision.Worktree == "" {
			t.Fatalf("revoked revision was not retained: %+v", revision.ReviewJob)
		}
		if e.runs[revision.RunID].Run.Status != "failed" {
			t.Fatalf("revoked publication run status: %s", e.runs[revision.RunID].Run.Status)
		}
		if head := strings.TrimSpace(workspaceTestGit(t, f.repository, "ls-remote", "--heads", f.remote, w.Branch)); !strings.HasPrefix(head, original+"\t") {
			t.Fatalf("revoked revision advanced the PR branch: %s", head)
		}
		return
	}
	if autoSelect {
		if _, err := os.Stat(filepath.Join(f.data, "review-selected")); err != nil {
			t.Fatalf("trusted review did not select its workflow: %v", err)
		}
	}
	if revision.Status != "published" || revision.RunID == "" || revision.Worktree != "" {
		t.Fatalf("revision did not publish and clean up: %+v", revision.ReviewJob)
	}
	if e.runs[revision.RunID].Run.Status != "succeeded" {
		t.Fatalf("review run status: %s", e.runs[revision.RunID].Run.Status)
	}
	actual := strings.TrimSpace(workspaceTestGit(t, f.repository, "ls-remote", "--heads", f.remote, w.Branch))
	if !strings.HasPrefix(actual, revision.HeadSHA+"\t") || revision.HeadSHA == original {
		t.Fatalf("PR branch did not advance safely: %s", actual)
	}
	if _, err = e.pollRepositoryIntake(ctx, "p", "r"); err != nil {
		t.Fatal(err)
	}
	if len(e.reviewJobs) != 1 {
		t.Fatalf("feedback was replayed: %d revision jobs", len(e.reviewJobs))
	}
}

func TestReviewPushRefusesAdvancedBranch(t *testing.T) {
	f := newWorkspaceFixture(t)
	ctx := context.Background()
	w := workspaceTestPrepare(t, f, "revision")
	w = workspaceTestCheckpoint(t, f, w)
	workspaceTestGit(t, f.repository, "push", f.remote, "main:refs/heads/factory/issue-reviewed")
	workspaceTestWrite(t, filepath.Join(f.repository, "tracked.txt"), []byte("new remote work\n"))
	workspaceTestGit(t, f.repository, "add", "tracked.txt")
	workspaceTestGit(t, f.repository, "commit", "-m", "remote advance")
	workspaceTestGit(t, f.repository, "push", f.remote, "main:refs/heads/factory/issue-reviewed")
	err := pushReviewWorkspace(ctx, f.repository, w, f.remote, "factory/issue-reviewed", w.BaseSHA)
	if err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(fmt.Sprintf("expected changed remote branch rejection, got %v", err))
	}
}
