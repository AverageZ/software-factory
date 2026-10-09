package factory

import (
	"context"
	"strings"
	"testing"
)

func TestReviewBindingRejectsReadOnlyReviewGraph(t *testing.T) {
	e := &Engine{
		bindings:  map[string]Binding{"existing-review": {ID: "existing-review", ProjectID: "project", WorkflowID: "read-only", Repositories: map[string]string{"source": "repository"}}},
		workflows: map[string]Workflow{"read-only": {ID: "read-only", Inputs: map[string]string{"pullRequest": "object", "feedback": "array"}, Nodes: []WorkflowNode{{ID: "check", Kind: "validation", Repository: "source"}, {ID: "review", Kind: "agent", Repository: "source"}}, Edges: []Edge{{Source: "check", Target: "review"}}}},
	}
	if err := e.checkReviewBindingLocked("project", "repository", "existing-review"); err == nil || !strings.Contains(err.Error(), "one agent node") {
		t.Fatalf("read-only review graph was accepted as a revision workflow: %v", err)
	}
}

func TestReviewRestartQuarantinesUnfinishedRevision(t *testing.T) {
	data := t.TempDir()
	store, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "working", "publishing"} {
		job := savedReviewJob{ReviewJob: ReviewJob{ID: status, Status: status, FeedbackKeys: []string{"conversation:1"}}}
		if err := store.save(record{"review-job", job.ID, job}); err != nil {
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
		job := engine.reviewJobs[id]
		if job.Status != "interrupted" || len(job.FeedbackKeys) != 1 || job.Error == "" {
			t.Fatalf("restart lost revision evidence or left work executable: %+v", job)
		}
	}
}

func TestCancellingRevisionStopsPublicationContext(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	w := Workflow{ID: "revision", Nodes: []WorkflowNode{{ID: "revise", Kind: "agent"}, {ID: "factory-publish", Kind: "integration"}}}
	r := newRun(Project{ID: "project"}, Binding{}, w, nil, nil, "")
	r.IssueJobID = "issue"
	r.Run.Status = "running"
	agent := r.Run.Nodes["revise"]
	agent.Status = "succeeded"
	r.Run.Nodes["revise"] = agent
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.mu.Lock()
	e.reviewJobs["revision"] = savedReviewJob{ReviewJob: ReviewJob{ID: "revision", RunID: r.Run.ID}}
	e.active["review/revision"] = activeExecution{cancel: cancel, projectID: "project"}
	err = e.commitRunsLocked(r)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Cancel(r.Run.ID); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("cancellation did not stop managed revision publication")
	}
}

func TestEnabledIntakeSeedsEditableReviewWorkflowOnce(t *testing.T) {
	fixture := newWorkspaceFixture(t)
	store, err := openStore(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	project := Project{ID: "project", Name: "Project", Repositories: []Repository{{ID: "repo", Name: "Repository", Path: fixture.repository}}, Harness: Harness{Binary: "omp", Model: "test"}, Policy: Policy{MaxParallel: 1}}
	config := intakeConfig{Enabled: true, ProjectID: project.ID, RepositoryID: "repo", RepositoryPath: fixture.repository, Connection: GitHubConnection{ID: 123}}
	if err := store.save(record{"project", project.ID, project}, record{"intake", githubKey(project.ID, "repo"), config}); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	engine, err := Open(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	bindingID := engine.intakes[githubKey(project.ID, "repo")].ReviewBindingID
	binding := engine.bindings[bindingID]
	workflow := engine.workflows[binding.WorkflowID]
	if bindingID == "" || workflow.ID == "" || workflow.Nodes[0].Config["feedbackSources"] == nil {
		t.Fatalf("enabled intake has no editable review workflow: %+v", binding)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err = Open(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	if engine.intakes[githubKey(project.ID, "repo")].ReviewBindingID != bindingID || len(engine.workflows) != 1 {
		t.Fatal("restart recreated or lost the review workflow")
	}
	if _, err := engine.setReviewBinding(project.ID, "repo", ""); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	engine, err = Open(fixture.data)
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if c := engine.intakes[githubKey(project.ID, "repo")]; c.ReviewBindingID != "" || !c.ReviewDisabled {
		t.Fatalf("explicit review opt-out was lost on restart: %+v", c)
	}
}
