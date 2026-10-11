package factory

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPRSynchronizationResolvesConflictAndCASPublishesMerge(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	ctx := context.Background()
	release, err := acquireIssueLease(fixture.repository, "original")
	if err != nil {
		t.Fatal(err)
	}
	originalWorkspace, err := prepareIssueWorkspace(ctx, fixture.data, fixture.repository, "original", fixture.remote, "main")
	if err != nil {
		release()
		t.Fatal(err)
	}
	workspaceTestWrite(t, filepath.Join(originalWorkspace.Path, "tracked.txt"), []byte("pull request change\n"))
	originalWorkspace, err = checkpointIssueWorkspace(ctx, fixture.repository, originalWorkspace, "PR change")
	if err != nil {
		release()
		t.Fatal(err)
	}
	if err = pushIssueWorkspace(ctx, fixture.repository, originalWorkspace, fixture.remote); err != nil {
		release()
		t.Fatal(err)
	}
	if err = cleanupIssueWorkspace(ctx, fixture.data, fixture.repository, "original", originalWorkspace); err != nil {
		release()
		t.Fatal(err)
	}
	release()
	originalHead := originalWorkspace.HeadSHA
	secondBranch := "factory/issue-z-second"
	workspaceTestGit(t, fixture.repository, "push", fixture.remote, originalHead+":refs/heads/"+secondBranch)

	upstream := filepath.Join(filepath.Dir(fixture.remote), "upstream")
	workspaceTestGit(t, filepath.Dir(upstream), "clone", fixture.remote, upstream)
	workspaceTestGit(t, upstream, "config", "user.name", "Upstream")
	workspaceTestGit(t, upstream, "config", "user.email", "upstream@example.invalid")
	workspaceTestWrite(t, filepath.Join(upstream, "tracked.txt"), []byte("base branch change\n"))
	workspaceTestGit(t, upstream, "add", "tracked.txt")
	workspaceTestGit(t, upstream, "commit", "-m", "Advance base")
	workspaceTestGit(t, upstream, "push", "origin", "main")
	baseHead := workspaceTestGit(t, upstream, "rev-parse", "HEAD")

	workspaceTestGit(t, fixture.repository, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(fixture.data, "bin")
	if err = os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	gitWrapper := `#!/bin/bash
args=()
for arg in "$@"; do
 if [[ "$arg" == "https://github.com/owner/repo.git" ]]; then arg="$FACTORY_TEST_REMOTE"; fi
 args+=("$arg")
done
exec "$FACTORY_TEST_GIT" "${args[@]}"
`
	workspaceTestWrite(t, filepath.Join(bin, "git"), []byte(gitWrapper))
	if err = os.Chmod(filepath.Join(bin, "git"), 0700); err != nil {
		t.Fatal(err)
	}
	gh := `#!/bin/sh
case "$*" in
 *'repos/owner/repo'*) printf '%s' '{"id":123,"name":"repo","owner":{"login":"owner"},"default_branch":"main","archived":false,"disabled":false,"has_issues":true,"permissions":{"push":true}}' ;;
 *'repositories/123/issues?'*) printf '%s' '[[]]' ;;
 *'--method POST repositories/123/issues/42/labels'*)
  touch "$FACTORY_TEST_SYNC_SELECTED"
  printf '%s' '[{"name":"agent:run"},{"name":"agent/workflow:sync"}]' ;;
 *'repositories/123/issues/42'*)
  if [ -f "$FACTORY_TEST_SYNC_SELECTED" ]; then
   printf '%s' '{"number":42,"state":"open","pull_request":{},"labels":[{"name":"agent:run"},{"name":"agent/workflow:sync"}]}'
  else
   printf '%s' '{"number":42,"state":"open","pull_request":{},"labels":[{"name":"agent:run"}]}'
  fi ;;
 *'repositories/123/issues/43'*) printf '%s' '{"number":43,"state":"open","pull_request":{},"labels":[{"name":"agent:run"},{"name":"agent/workflow:sync"}]}' ;;
 *'repositories/123/pulls/42'*) printf '{"number":42,"html_url":"https://github.com/owner/repo/pull/42","state":"open","head":{"ref":"factory/issue-original","sha":"%s","repo":{"id":123}},"base":{"ref":"main","repo":{"id":123}}}' "$FACTORY_TEST_PR_HEAD" ;;
 *'repositories/123/pulls/43'*) printf '{"number":43,"html_url":"https://github.com/owner/repo/pull/43","state":"open","head":{"ref":"factory/issue-z-second","sha":"%s","repo":{"id":123}},"base":{"ref":"main","repo":{"id":123}}}' "$FACTORY_TEST_PR_HEAD" ;;
 *'repositories/123/commits/main'*) printf '{"sha":"%s"}' "$FACTORY_TEST_BASE_HEAD" ;;
 *'repositories/123/compare/'*) printf '%s' '{"status":"diverged"}' ;;
 *) exit 1 ;;
esac
`
	workspaceTestWrite(t, filepath.Join(bin, "gh"), []byte(gh))
	if err = os.Chmod(filepath.Join(bin, "gh"), 0700); err != nil {
		t.Fatal(err)
	}
	harness := `#!/bin/sh
printf 'resolved pull request and base changes\n' > tracked.txt
printf 'synchronization validated\n'
`
	workspaceTestWrite(t, filepath.Join(bin, "fake-harness"), []byte(harness))
	if err = os.Chmod(filepath.Join(bin, "fake-harness"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FACTORY_TEST_GIT", gitBinary)
	t.Setenv("FACTORY_TEST_REMOTE", fixture.remote)
	t.Setenv("FACTORY_TEST_PR_HEAD", originalHead)
	t.Setenv("FACTORY_TEST_BASE_HEAD", baseHead)
	t.Setenv("FACTORY_TEST_SYNC_SELECTED", filepath.Join(fixture.data, "sync-selected"))

	store, err := openStore(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	operation, stop := context.WithCancel(context.Background())
	engine := &Engine{
		store:       store,
		data:        fixture.data,
		ctx:         operation,
		stop:        stop,
		projects:    map[string]Project{},
		workflows:   map[string]Workflow{},
		bindings:    map[string]Binding{},
		intakes:     map[string]intakeConfig{},
		intakeBusy:  map[string]bool{},
		issueJobs:   map[string]savedIssueJob{},
		reviewJobs:  map[string]savedReviewJob{},
		syncJobs:    map[string]savedSyncJob{},
		runs:        map[string]savedRun{},
		active:      map[string]activeExecution{},
		issueActive: map[string]bool{},
		errors:      make(chan error, 1),
	}
	defer engine.Close()
	project := Project{ID: "project", Name: "Project", Repositories: []Repository{{ID: "repository", Name: "Repository", Path: fixture.repository}}, Harness: Harness{Binary: filepath.Join(bin, "fake-harness"), Model: "fake"}, Policy: Policy{MaxParallel: 1}}
	workflow := Workflow{ID: "sync", Name: "Synchronize PR", Inputs: map[string]string{"pullRequest": "object", "synchronization": "object"}, Nodes: []WorkflowNode{{ID: "resolve", Name: "Resolve and validate", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "Resolve the prepared merge and validate it."}, Inputs: map[string]Input{"pullRequest": {Type: "object", From: "inputs.pullRequest"}, "synchronization": {Type: "object", From: "inputs.synchronization"}}}}}
	binding := Binding{ID: "sync-binding", Name: "Synchronization", ProjectID: project.ID, WorkflowID: workflow.ID, Repositories: map[string]string{"source": "repository"}, Inputs: map[string]Value{}}
	connection := GitHubConnection{ID: 123, Owner: "owner", Name: "repo", DefaultBranch: "main", CanPush: true}
	issueWorkflow, issueBinding := issueBindingFixture(project.ID, "repository", "issue-binding", "Configured issue work")
	engine.projects[project.ID] = project
	engine.workflows[workflow.ID] = workflow
	engine.workflows[issueWorkflow.ID] = issueWorkflow
	engine.bindings[binding.ID] = binding
	engine.bindings[issueBinding.ID] = issueBinding
	engine.intakes[githubKey(project.ID, "repository")] = intakeConfig{Enabled: true, ProjectID: project.ID, RepositoryID: "repository", RepositoryPath: fixture.repository, Connection: connection, IssueBindingID: issueBinding.ID, SyncBindingID: binding.ID}
	issue := savedIssueJob{IssueJob: IssueJob{ID: "original", ProjectID: project.ID, RepositoryID: "repository", GitHubRepositoryID: 123, Number: 5, Status: "published", Branch: originalWorkspace.Branch, HeadSHA: originalHead, PullRequestNumber: 42, PullRequestURL: "https://github.com/owner/repo/pull/42"}, Repository: project.Repositories[0], Project: project, Connection: connection, BaseBranch: "main"}
	engine.issueJobs[issue.ID] = issue
	secondIssue := issue
	secondIssue.ID = "z-second"
	secondIssue.Branch = secondBranch
	secondIssue.PullRequestNumber = 43
	secondIssue.PullRequestURL = "https://github.com/owner/repo/pull/43"
	engine.issueJobs[secondIssue.ID] = secondIssue

	if _, err = engine.pollRepositoryIntake(ctx, project.ID, "repository"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { engine.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("PR synchronization did not finish")
	}
	if len(engine.syncJobs) != 1 {
		t.Fatalf("synchronization jobs = %d", len(engine.syncJobs))
	}
	var synchronization savedSyncJob
	for _, job := range engine.syncJobs {
		synchronization = job
	}
	if synchronization.Status != "published" || !synchronization.Conflicted || synchronization.RunID == "" || synchronization.Worktree != "" {
		t.Fatalf("synchronization did not publish and clean up: %+v", synchronization.SyncJob)
	}
	if engine.runs[synchronization.RunID].Run.Status != "succeeded" {
		t.Fatalf("synchronization run status = %s", engine.runs[synchronization.RunID].Run.Status)
	}
	if _, err = os.Stat(filepath.Join(fixture.data, "sync-selected")); err != nil {
		t.Fatalf("synchronization monitoring label was not selected: %v", err)
	}
	remoteHead := strings.Fields(workspaceTestGit(t, fixture.repository, "ls-remote", "--heads", fixture.remote, originalWorkspace.Branch))[0]
	if remoteHead != synchronization.HeadSHA || remoteHead == originalHead {
		t.Fatalf("PR branch was not advanced to the synchronization checkpoint: %s", remoteHead)
	}
	parents := strings.Fields(workspaceTestGit(t, fixture.repository, "show", "-s", "--format=%P", remoteHead))
	if len(parents) != 2 || parents[0] != originalHead || parents[1] != baseHead {
		t.Fatalf("published checkpoint is not the expected merge: %v", parents)
	}
	content := workspaceTestGit(t, fixture.repository, "show", remoteHead+":tracked.txt")
	if content != "resolved pull request and base changes" {
		t.Fatalf("resolved content = %q", content)
	}
	remoteBase := strings.Fields(workspaceTestGit(t, fixture.repository, "ls-remote", "--heads", fixture.remote, "main"))[0]
	if remoteBase != baseHead {
		t.Fatalf("synchronization changed the base branch: %s", remoteBase)
	}
}

func TestSyncBindingRequiresManagedInputs(t *testing.T) {
	engine := &Engine{
		bindings: map[string]Binding{
			"binding": {ID: "binding", ProjectID: "project", WorkflowID: "workflow", Repositories: map[string]string{"source": "repository"}},
		},
		workflows: map[string]Workflow{
			"workflow": {
				ID:     "workflow",
				Inputs: map[string]string{"pullRequest": "object"},
				Nodes: []WorkflowNode{{
					ID:         "sync",
					Kind:       "agent",
					Repository: "source",
					Inputs:     map[string]Input{"pullRequest": {Type: "object", From: "inputs.pullRequest"}},
				}},
			},
		},
	}
	if err := engine.checkSyncBindingLocked("project", "repository", "binding"); err == nil || !strings.Contains(err.Error(), "synchronization") {
		t.Fatalf("invalid synchronization workflow was accepted: %v", err)
	}
}

func TestSyncRestartQuarantinesUnfinishedWork(t *testing.T) {
	data := t.TempDir()
	store, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "working", "publishing"} {
		job := savedSyncJob{SyncJob: SyncJob{ID: status, Status: status, PullRequestHeadSHA: strings.Repeat("a", 40), BaseSHA: strings.Repeat("b", 40)}}
		if err := store.save(record{"sync-job", job.ID, job}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	for _, id := range []string{"queued", "working", "publishing"} {
		job := engine.syncJobs[id]
		if job.Status != "interrupted" || job.Error == "" {
			t.Fatalf("restart left synchronization executable: %+v", job.SyncJob)
		}
	}
}
