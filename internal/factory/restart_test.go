package factory

import (
	"context"
	"testing"
)

func TestPrepareRestartPreservesActiveWork(t *testing.T) {
	cases := []struct {
		name string
		set  func(*Engine)
	}{
		{"process", func(e *Engine) { e.active = map[string]activeExecution{"run/node": {}} }},
		{"managed execution", func(e *Engine) { e.issueActive = map[string]bool{"job": true} }},
		{"intake poll", func(e *Engine) { e.intakeBusy = map[string]bool{"repo": true} }},
		{"connection check", func(e *Engine) { e.githubBusy = map[string]bool{"repo": true} }},
		{"issue publication", func(e *Engine) {
			e.issueJobs = map[string]savedIssueJob{"job": {IssueJob: IssueJob{Status: "publishing"}}}
		}},
		{"admitted revision", func(e *Engine) {
			e.reviewJobs = map[string]savedReviewJob{"job": {ReviewJob: ReviewJob{Status: "queued"}}}
		}},
		{"admitted synchronization", func(e *Engine) {
			e.syncJobs = map[string]savedSyncJob{"job": {SyncJob: SyncJob{Status: "queued"}}}
		}},
		{"running node", func(e *Engine) {
			e.runs = map[string]savedRun{"run": {Run: Run{Nodes: map[string]NodeExecution{"node": {Status: "running"}}}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			e := &Engine{ctx: ctx, stop: stop}
			tc.set(e)
			if err := e.PrepareRestart(); err == nil {
				t.Fatal("restart admitted while work was active")
			}
			if ctx.Err() != nil || e.healthyLocked() != nil {
				t.Fatal("rejected restart stopped the engine")
			}
		})
	}
}

func TestPrepareRestartStopsAdmissionWithQueuedWork(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{ctx: ctx, stop: stop,
		runs:      map[string]savedRun{"queued": {Run: Run{Nodes: map[string]NodeExecution{"node": {Status: "pending"}}}}},
		issueJobs: map[string]savedIssueJob{"queued": {IssueJob: IssueJob{Status: "queued"}}},
	}
	if err := e.PrepareRestart(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("restart did not stop admission")
	}
	if err := e.healthyLocked(); err == nil {
		t.Fatal("engine still accepts work after restart admission")
	}
	if e.runs["queued"].Run.Nodes["node"].Status != "pending" || e.issueJobs["queued"].Status != "queued" {
		t.Fatal("safe queued work was changed during restart")
	}
}
