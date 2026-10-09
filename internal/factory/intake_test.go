package factory

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIssueAuthorizationSelectors(t *testing.T) {
	cases := []struct {
		name    string
		state   string
		labels  []string
		pr      bool
		allowed bool
	}{
		{"sample issue", "open", []string{"chore", "agent:run"}, false, true},
		{"case insensitive GitHub names", "open", []string{"Agent:Run", "AGENT/MODE:REPAIR"}, false, true},
		{"explicit bug repair", "open", []string{"agent:run", "agent/workflow:bug", "agent/mode:repair"}, false, true},
		{"projection is not authorization", "open", []string{"agent/status:active", "agent/phase:implementing"}, false, false},
		{"projections do not revoke", "open", []string{"agent:run", "agent/status:failed", "agent/phase:triaging"}, false, true},
		{"closed issue", "closed", []string{"agent:run"}, false, false},
		{"pull request", "open", []string{"agent:run"}, true, false},
		{"observe cannot publish", "open", []string{"agent:run", "agent/mode:observe"}, false, false},
		{"manage cannot bypass repair", "open", []string{"agent:run", "agent/mode:manage"}, false, false},
		{"conflicting modes", "open", []string{"agent:run", "agent/mode:repair", "agent/mode:observe"}, false, false},
		{"unsupported workflow", "open", []string{"agent:run", "agent/workflow:review"}, false, false},
		{"conflicting workflows", "open", []string{"agent:run", "agent/workflow:bug", "agent/workflow:rollback"}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issue := githubIssue{State: tc.state}
			for _, label := range tc.labels {
				issue.Labels = append(issue.Labels, gitHubLabelResponse{Name: label})
			}
			if tc.pr {
				issue.PullRequest = json.RawMessage(`{"url":"https://api.github.com/repos/owner/repo/pulls/1"}`)
			}
			if err := issueAuthorization(issue); (err == nil) != tc.allowed {
				t.Fatalf("authorization = %v; allowed=%t", err, tc.allowed)
			}
		})
	}
}

func intakeTestGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	data, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s: %v", args, data, err)
	}
	return string(data)
}
func TestIntakeDeduplicatesAcrossPollsAndRestart(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	bin := filepath.Join(root, "bin")
	for _, path := range []string{repo, bin} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	intakeTestGit(t, repo, "init", "-b", "main")
	intakeTestGit(t, repo, "remote", "add", "origin", "https://github.com/owner/repo.git")
	// Only the network boundary is replaced. Actual repository discovery, durable
	// admission, authorization selection, and restart recovery execute normally.
	script := `#!/bin/sh
case "$*" in
 *'repos/owner/repo'*) printf '%s' '{"id":123,"name":"repo","owner":{"login":"owner"},"default_branch":"main","archived":false,"disabled":false,"has_issues":true,"permissions":{"push":true}}' ;;
 *'repositories/123/issues?'*) printf '%s' '[[{"number":2463,"title":"Missing rendering coverage","body":"Test show true and false","html_url":"https://github.com/owner/repo/issues/2463","state":"open","labels":[{"name":"chore"},{"name":"agent:run"}]},{"number":2464,"state":"open","pull_request":{"url":"pr"},"labels":[{"name":"agent:run"}]},{"number":2465,"state":"open","labels":[{"name":"agent:run"},{"name":"agent/mode:observe"}]}]]' ;;
 *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	data := filepath.Join(root, "data")
	s, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	e := &Engine{store: s, data: data, ctx: ctx, stop: stop, projects: map[string]Project{}, intakes: map[string]intakeConfig{}, issueJobs: map[string]savedIssueJob{}, intakeBusy: map[string]bool{}, issueActive: map[string]bool{}, active: map[string]activeExecution{}, errors: make(chan error, 1)}
	defer func() {
		if e != nil {
			_ = e.Close()
		}
	}()
	key := githubKey("p", "r")
	e.mu.Lock()
	p := Project{ID: "p", Repositories: []Repository{{ID: "r", Path: repo}}, Policy: Policy{MaxParallel: 0}}
	c := intakeConfig{Enabled: true, ProjectID: "p", RepositoryID: "r", RepositoryPath: repo, Connection: GitHubConnection{ID: 123, Host: "github.com", CanPush: true}}
	e.projects[p.ID] = p
	e.intakes[key] = c
	err = e.store.save(record{"project", p.ID, p}, record{"intake", key, c})
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		report, err := e.pollRepositoryIntake(context.Background(), "p", "r")
		if err != nil {
			t.Fatal(err)
		}
		if report.Error != "" {
			t.Fatal(report.Error)
		}
		if len(report.Jobs) != 2 {
			t.Fatalf("expected two issue subjects, excluding PR: %+v", report.Jobs)
		}
		for _, j := range report.Jobs {
			want := "queued"
			if j.Number == 2465 {
				want = "paused"
			}
			if j.Status != want {
				t.Fatalf("issue %d = %s; want %s", j.Number, j.Status, want)
			}
		}
	}
	e.mu.Lock()
	c.Enabled = false
	e.intakes[key] = c
	var interruptedID string
	for id, j := range e.issueJobs {
		if j.Number == 2463 {
			j.Status = "working"
			j.Worktree = filepath.Join(data, "worktrees", id)
			interruptedID = id
			if err = e.saveIssueJobLocked(j); err != nil {
				break
			}
		}
	}
	if err == nil {
		err = e.store.save(record{"intake", key, c})
	}
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = Open(data)
	if err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	j := e.issueJobs[interruptedID]
	count := len(e.issueJobs)
	e.mu.Unlock()
	if count != 2 || j.Status != "interrupted" || j.Worktree == "" {
		t.Fatalf("restart lost deduplication or uncheckpointed work: count=%d, job=%+v", count, j)
	}
}

func TestIssuePRRequiresOwnedHeadAndMarker(t *testing.T) {
	j := savedIssueJob{IssueJob: IssueJob{ID: "job", GitHubRepositoryID: 123, Branch: "factory/issue-job", HeadSHA: "1234567890123456789012345678901234567890"}, BaseBranch: "main"}
	pr := issuePullRequest{Number: 42, State: "open", Draft: true, Body: issuePRMarker(j.ID)}
	pr.Head.Ref = j.Branch
	pr.Head.Repo.ID = 123
	pr.Base.Repo.ID = 123
	pr.Head.SHA = j.HeadSHA
	pr.Base.Ref = j.BaseBranch
	if err := validateIssuePR(pr, j); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*issuePullRequest){
		func(p *issuePullRequest) { p.Body = "unrelated pull request" },
		func(p *issuePullRequest) { p.Head.Ref = "another-branch" },
		func(p *issuePullRequest) { p.Head.Repo.ID = 456 },
		func(p *issuePullRequest) { p.Base.Repo.ID = 456 },
		func(p *issuePullRequest) { p.Head.SHA = "different-candidate" },
		func(p *issuePullRequest) { p.Base.Ref = "different-base" },
		func(p *issuePullRequest) { p.Draft = false },
	} {
		candidate := pr
		mutate(&candidate)
		if err := validateIssuePR(candidate, j); err == nil {
			t.Fatal("accepted an unrelated PR for reconciliation/cleanup")
		}
	}
	// Human edits after a confirmed publication must not strand a closed PR's
	// local checkpoint. The recorded PR number remains its immutable identity.
	j.PullRequestNumber = pr.Number
	pr.State, pr.Body, pr.Head.SHA, pr.Base.Ref = "closed", "Edited by human", "new-human-commit", "retargeted"
	if err := validateIssuePR(pr, j); err != nil {
		t.Fatalf("confirmed closed PR lost its cleanup association: %v", err)
	}
	pr.Number++
	if err := validateIssuePR(pr, j); err == nil {
		t.Fatal("recorded publication adopted a different PR number")
	}
}
