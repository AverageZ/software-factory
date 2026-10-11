package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newRecoveryTestEngine(t *testing.T, f workspaceFixture) *Engine {
	t.Helper()
	store, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	project := Project{ID: "project", Repositories: []Repository{{ID: "repository", Path: f.repository}}, Policy: Policy{MaxParallel: 1}}
	engine := &Engine{
		store:       store,
		data:        f.data,
		ctx:         ctx,
		stop:        stop,
		projects:    map[string]Project{project.ID: project},
		intakes:     map[string]intakeConfig{githubKey(project.ID, "repository"): {Enabled: true, ProjectID: project.ID, RepositoryID: "repository", RepositoryPath: f.repository}},
		issueJobs:   map[string]savedIssueJob{},
		reviewJobs:  map[string]savedReviewJob{},
		syncJobs:    map[string]savedSyncJob{},
		intakeBusy:  map[string]bool{},
		issueActive: map[string]bool{},
		active:      map[string]activeExecution{},
		errors:      make(chan error, 1),
	}
	t.Cleanup(func() { engine.Close() })
	return engine
}

func TestRecoverSyncJobDiscardsUncheckpointedWorkAndAllowsRetry(t *testing.T) {
	f := newWorkspaceFixture(t)
	engine := newRecoveryTestEngine(t, f)
	ctx := context.Background()
	release, err := acquireIssueLease(f.repository, "failed-sync")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := prepareIssueWorkspace(ctx, f.data, f.repository, "failed-sync", f.remote, "main")
	if err != nil {
		release()
		t.Fatal(err)
	}
	unfinished := filepath.Join(workspace.Path, "unfinished.txt")
	workspaceTestWrite(t, unfinished, []byte("discard me\n"))
	release()

	project := engine.projects["project"]
	issue := savedIssueJob{
		IssueJob:   IssueJob{ID: "source", ProjectID: project.ID, RepositoryID: "repository", Status: "published", PullRequestNumber: 42},
		Project:    project,
		Repository: project.Repositories[0],
	}
	job := savedSyncJob{
		SyncJob: SyncJob{
			ID:                 "failed-sync",
			ProjectID:          project.ID,
			RepositoryID:       "repository",
			IssueJobID:         issue.ID,
			PullRequestNumber:  42,
			Status:             "failed",
			Worktree:           workspace.Path,
			PullRequestHeadSHA: workspace.BaseSHA,
			BaseSHA:            strings.Repeat("b", 40),
		},
		Project: project,
		Issue:   issue,
	}
	engine.issueJobs[issue.ID] = issue
	engine.syncJobs[job.ID] = job
	if err := engine.store.save(record{"issue-job", issue.ID, issue}, record{"sync-job", job.ID, job}); err != nil {
		t.Fatal(err)
	}

	report, err := engine.recoverSyncJob(ctx, project.ID, "repository", job.ID, "retry", job.PullRequestHeadSHA, job.BaseSHA, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Synchronizations) != 1 || report.Synchronizations[0].Status != "released" || report.Synchronizations[0].Worktree != "" {
		t.Fatalf("recovered synchronization = %+v", report.Synchronizations)
	}
	if _, err := os.Stat(workspace.Path); !os.IsNotExist(err) {
		t.Fatalf("discarded synchronization worktree still exists: %v", err)
	}
	recovered := engine.syncJobs[job.ID]
	if syncJobBlocks(recovered) || syncJobResolved(recovered.Status) {
		t.Fatalf("released synchronization cannot be retried: %+v", recovered.SyncJob)
	}
	other, err := acquireIssueLease(f.repository, "other")
	if err != nil {
		t.Fatalf("discarded synchronization still blocks the repository: %v", err)
	}
	other()
}

func TestRecoverReviewJobRetryAndDismissControlFeedbackReplay(t *testing.T) {
	f := newWorkspaceFixture(t)
	engine := newRecoveryTestEngine(t, f)
	project := engine.projects["project"]
	issue := savedIssueJob{
		IssueJob:   IssueJob{ID: "source", ProjectID: project.ID, RepositoryID: "repository", Status: "published", PullRequestNumber: 42},
		Project:    project,
		Repository: project.Repositories[0],
	}
	engine.issueJobs[issue.ID] = issue
	release, err := acquireIssueLease(f.repository, "busy-worker")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	for _, action := range []string{"retry", "dismiss"} {
		job := savedReviewJob{
			ReviewJob: ReviewJob{
				ID:                "review-" + action,
				ProjectID:         project.ID,
				RepositoryID:      "repository",
				IssueJobID:        issue.ID + "-" + action,
				PullRequestNumber: 42,
				Status:            "failed",
				BaseSHA:           f.base,
				FeedbackKeys:      []string{"inline:1"},
			},
			Project: project,
			Issue:   issue,
		}
		job.Issue.ID = job.IssueJobID
		engine.reviewJobs[job.ID] = job
		if err := engine.store.save(record{"review-job", job.ID, job}); err != nil {
			t.Fatal(err)
		}
		report, err := engine.recoverReviewJob(context.Background(), project.ID, "repository", job.ID, action, f.base, "")
		if err != nil {
			t.Fatal(err)
		}
		var recovered savedReviewJob
		for _, item := range report.Revisions {
			if item.ID == job.ID {
				recovered = engine.reviewJobs[item.ID]
			}
		}
		if recovered.ID == "" || reviewJobBlocks(recovered) {
			t.Fatalf("%s recovery still blocks revision: %+v", action, recovered.ReviewJob)
		}
		if got, want := reviewJobConsumesFeedback(recovered), action == "dismiss"; got != want {
			t.Fatalf("%s consumes feedback = %v, want %v", action, got, want)
		}
	}
}
