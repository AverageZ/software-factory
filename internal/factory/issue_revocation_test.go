package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIssuePublicationStopsOnRevocationDuringBaseLookup(t *testing.T) {
	for _, action := range []string{"cancel", "disable"} {
		t.Run(action, func(t *testing.T) {
			f := newWorkspaceFixture(t)
			release, err := acquireIssueLease(f.repository, "revoked")
			if err != nil {
				t.Fatal(err)
			}
			w, err := prepareIssueWorkspace(context.Background(), f.data, f.repository, "revoked", f.remote, "main")
			if err != nil {
				release()
				t.Fatal(err)
			}
			w = workspaceTestCheckpoint(t, f, w)
			release()
			workspaceTestGit(t, f.repository, "remote", "set-url", "origin", "https://github.com/owner/repo.git")
			s, err := openStore(f.data)
			if err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(context.Background())
			e := &Engine{store: s, data: f.data, ctx: ctx, stop: stop, projects: map[string]Project{}, intakes: map[string]intakeConfig{}, issueJobs: map[string]savedIssueJob{}, runs: map[string]savedRun{}, intakeBusy: map[string]bool{}, issueActive: map[string]bool{}, active: map[string]activeExecution{}, errors: make(chan error, 1), wake: make(chan struct{}, 1)}
			defer e.Close()
			p := Project{ID: "project", Repositories: []Repository{{ID: "repo", Path: f.repository}}, Policy: Policy{MaxParallel: 1}}
			connection := GitHubConnection{ID: 123, Host: "github.com", Owner: "owner", Name: "repo", DefaultBranch: "main"}
			config := intakeConfig{Enabled: true, ProjectID: p.ID, RepositoryID: "repo", RepositoryPath: f.repository, Connection: connection}
			e.projects[p.ID] = p
			e.intakes[githubKey(p.ID, "repo")] = config
			workflow := Workflow{ID: "managed", Nodes: []WorkflowNode{{ID: "implement", Kind: "agent"}, {ID: "publish", Kind: "integration"}}, Edges: []Edge{{Source: "implement", Target: "publish"}}}
			run := newRun(p, Binding{}, workflow, nil, nil, "")
			run.IssueJobID = "revoked"
			run.Run.Status = "running"
			node := run.Run.Nodes["implement"]
			node.Status = "succeeded"
			run.Run.Nodes["implement"] = node
			e.runs[run.Run.ID] = run
			j := savedIssueJob{IssueJob: IssueJob{ID: "revoked", ProjectID: p.ID, RepositoryID: "repo", GitHubRepositoryID: 123, Number: 1, Title: "Repair", Status: "publishing", RunID: run.Run.ID}, Project: p, Repository: p.Repositories[0], Connection: connection, Body: "Fix it", BaseBranch: "main"}
			j.setWorkspace(w)
			if err = e.store.save(record{"run", run.Run.ID, run}, record{"intake", githubKey(p.ID, "repo"), config}); err != nil {
				t.Fatal(err)
			}
			if err = e.saveIssueJob(j); err != nil {
				t.Fatal(err)
			}
			signal := filepath.Join(f.data, "lookup-started")
			gate := filepath.Join(f.data, "lookup-release")
			bin := filepath.Join(f.data, "bin")
			if err = os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
case "$*" in
 *'repos/owner/repo'*) printf '%s' '{"id":123,"name":"repo","owner":{"login":"owner"},"default_branch":"main","archived":false,"disabled":false,"has_issues":true,"permissions":{"push":true}}' ;;
 *'repositories/123/issues/1'*) printf '%s' '{"number":1,"title":"Repair","body":"Fix it","state":"open","labels":[{"name":"agent:run"}]}' ;;
 *'repositories/123/pulls?'*) printf '%s' '[[]]' ;;
 *'repositories/123/commits/main'*)
  printf ready > "$FACTORY_LOOKUP_SIGNAL"
  while [ ! -f "$FACTORY_LOOKUP_GATE" ]; do sleep 0.01; done
  printf '{"sha":"%s"}' "$FACTORY_BASE_SHA" ;;
 *) exit 1 ;;
esac
`
			if err = os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("FACTORY_LOOKUP_SIGNAL", signal)
			t.Setenv("FACTORY_LOOKUP_GATE", gate)
			t.Setenv("FACTORY_BASE_SHA", w.BaseSHA)
			workerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			e.active["issue/"+j.ID] = activeExecution{cancel: cancel, projectID: p.ID}
			e.issueActive[j.ID] = true
			e.wg.Add(1)
			done := make(chan struct{})
			go func() { e.workIssue(workerCtx, j); close(done) }()
			deadline := time.Now().Add(15 * time.Second)
			for {
				if _, err = os.Stat(signal); err == nil {
					break
				}
				select {
				case <-done:
					t.Fatal("publication stopped before the controlled base lookup")
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("publication never reached base lookup")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if action == "cancel" {
				if _, err = e.Cancel(run.Run.ID); err != nil {
					t.Fatal(err)
				}
				// Cancellation must stop the in-flight publication operation, not merely
				// change the UI state while a GitHub command continues indefinitely.
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("cancelled publication command remained live")
				}
			} else {
				if _, err = e.setRepositoryIntake(p.ID, "repo", false); err != nil {
					t.Fatal(err)
				}
				workspaceTestWrite(t, gate, []byte("release"))
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("disabled worker did not finish its freshness check")
				}
			}
			e.mu.Lock()
			observed := e.issueJobs[j.ID]
			recordedRun := e.runs[run.Run.ID]
			e.mu.Unlock()
			if observed.PullRequestNumber != 0 || observed.Worktree == "" || observed.Status == "published" {
				t.Fatalf("revoked publication escaped quarantine: %+v", observed)
			}
			if _, err = os.Stat(w.Path); err != nil {
				t.Fatalf("revoked local candidate lost: %v", err)
			}
			if refs := workspaceTestGit(t, f.repository, "ls-remote", "--heads", f.remote, w.Branch); strings.TrimSpace(refs) != "" {
				t.Fatalf("revoked branch was published: %s", refs)
			}
			if action == "cancel" && recordedRun.Run.Status != "cancelled" {
				t.Fatalf("cancelled run became %s", recordedRun.Run.Status)
			}
		})
	}
}
