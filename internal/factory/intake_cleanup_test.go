package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClosedPRCleanupContinuesWhenIntakeDisabled(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	remote := filepath.Join(root, "remote.git")
	data := filepath.Join(root, "data")
	bin := filepath.Join(root, "bin")
	for _, path := range []string{repo, bin} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	intakeTestGit(t, repo, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("base\n"), 0600); err != nil {
		t.Fatal(err)
	}
	intakeTestGit(t, repo, "add", "tracked.txt")
	intakeTestGit(t, repo, "-c", "user.name=Factory Test", "-c", "user.email=factory@example.invalid", "commit", "-m", "base")
	intakeTestGit(t, repo, "clone", "--bare", repo, remote)
	intakeTestGit(t, repo, "config", "user.name", "Factory Test")
	intakeTestGit(t, repo, "config", "user.email", "factory@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("developer's uncommitted work\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	e := &Engine{store: s, data: data, ctx: ctx, stop: stop, projects: map[string]Project{}, intakes: map[string]intakeConfig{}, issueJobs: map[string]savedIssueJob{}, intakeBusy: map[string]bool{}, issueActive: map[string]bool{}, active: map[string]activeExecution{}, errors: make(chan error, 1)}
	defer e.Close()
	release, err := acquireIssueLease(repo, "closed-job")
	if err != nil {
		t.Fatal(err)
	}
	w, err := prepareIssueWorkspace(ctx, data, repo, "closed-job", remote, "main")
	if err != nil {
		release()
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(w.Path, "candidate.txt"), []byte("tested candidate\n"), 0600); err != nil {
		release()
		t.Fatal(err)
	}
	w, err = checkpointIssueWorkspace(ctx, repo, w, "candidate")
	release()
	if err != nil {
		t.Fatal(err)
	}
	retainedFile := filepath.Join(w.Path, "uncheckpointed.txt")
	if err = os.WriteFile(retainedFile, []byte("must not be deleted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p := Project{ID: "p", Repositories: []Repository{{ID: "r", Path: repo}}}
	e.projects[p.ID] = p
	c := intakeConfig{Enabled: false, ProjectID: "p", RepositoryID: "r", RepositoryPath: repo}
	e.intakes[githubKey("p", "r")] = c
	j := savedIssueJob{IssueJob: IssueJob{ID: "closed-job", ProjectID: "p", RepositoryID: "r", GitHubRepositoryID: 123, Number: 2463, Status: "published", PullRequestNumber: 42}, Project: p, Repository: p.Repositories[0], Connection: GitHubConnection{ID: 123, Owner: "owner", Name: "repo"}}
	j.setWorkspace(w)
	if err = e.saveIssueJob(j); err != nil {
		t.Fatal(err)
	}
	pr := issuePullRequest{Number: 42, State: "closed", URL: "https://github.com/owner/repo/pull/42", Body: issuePRMarker(j.ID)}
	pr.Head.Ref = j.Branch
	pr.Head.SHA = j.HeadSHA
	pr.Head.Repo.ID = 123
	pr.Base.Repo.ID = 123
	fixture, err := json.Marshal(pr)
	if err != nil {
		t.Fatal(err)
	}
	fixturePath := filepath.Join(root, "closed-pr.json")
	if err = os.WriteFile(fixturePath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
case "$*" in
 *'repositories/123/pulls/42'*) exec cat "$FACTORY_TEST_PR" ;;
 *) exit 1 ;;
esac
`
	if err = os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FACTORY_TEST_PR", fixturePath)
	report, err := e.pollRepositoryIntake(ctx, "p", "r")
	if err != nil {
		t.Fatal(err)
	}
	if report.Enabled || report.Error != "" || len(report.Jobs) != 1 {
		t.Fatalf("disabled lifecycle poll failed: %+v", report)
	}
	observed := report.Jobs[0]
	if observed.Status != "closed" || observed.Worktree == "" || observed.Error == "" {
		t.Fatalf("dirty closed PR workspace was not retained with an error: %+v", observed)
	}
	if contents, err := os.ReadFile(retainedFile); err != nil || string(contents) != "must not be deleted\n" {
		t.Fatalf("uncheckpointed file lost: %q, %v", contents, err)
	}
	if err = os.Remove(retainedFile); err != nil {
		t.Fatal(err)
	}
	report, err = e.pollRepositoryIntake(ctx, "p", "r")
	if err != nil {
		t.Fatal(err)
	}
	observed = report.Jobs[0]
	if observed.Status != "closed" || observed.Worktree != "" || observed.Error != "" {
		t.Fatalf("cleaned closed PR state not persisted: %+v", observed)
	}
	if _, err = os.Stat(w.Path); !os.IsNotExist(err) {
		t.Fatalf("closed PR worktree still exists: %v", err)
	}
	if sha := strings.TrimSpace(intakeTestGit(t, repo, "rev-parse", "refs/heads/"+w.Branch)); sha != w.HeadSHA {
		t.Fatalf("checkpoint branch lost: %s", sha)
	}
	if contents, err := os.ReadFile(filepath.Join(repo, "tracked.txt")); err != nil || string(contents) != "developer's uncommitted work\n" {
		t.Fatalf("developer checkout changed: %q, %v", contents, err)
	}
}
