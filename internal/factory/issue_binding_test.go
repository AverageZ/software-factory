package factory

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func issueBindingFixture(projectID, repositoryID, bindingID, prompt string) (Workflow, Binding) {
	w := Workflow{ID: "workflow-" + bindingID, Name: "Configured issue", Inputs: map[string]string{"issue": "object", "setting": "string"}, Nodes: []WorkflowNode{{ID: "configured-agent", Name: "Configured agent", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": prompt}, Inputs: map[string]Input{"issue": {Type: "object", From: "inputs.issue"}, "option": {Type: "string", From: "inputs.setting"}}}}, Edges: []Edge{}}
	b := Binding{ID: bindingID, Name: "Configured issue", ProjectID: projectID, WorkflowID: w.ID, Repositories: map[string]string{"source": repositoryID}, Inputs: map[string]Value{"setting": {Type: "string", Value: "bound option"}}}
	return w, b
}

func TestIssueBindingSelectionFailsClosed(t *testing.T) {
	w, b := issueBindingFixture("p", "r", "configured", "Do configured work")
	otherW, otherB := issueBindingFixture("p", "r", "other", "Do other work")
	e := &Engine{workflows: map[string]Workflow{w.ID: w, otherW.ID: otherW}, bindings: map[string]Binding{b.ID: b, otherB.ID: otherB}}
	config := intakeConfig{ProjectID: "p", RepositoryID: "r", IssueBindingID: b.ID}
	for _, tc := range []struct {
		name   string
		labels []string
		want   string
	}{
		{"default", []string{"agent:run"}, b.ID},
		{"case-insensitive binding selector", []string{"AGENT:RUN", "AGENT/WORKFLOW:OTHER"}, otherB.ID},
		{"optional publication mode", []string{"agent:run", "agent/mode:repair"}, b.ID},
		{"unknown binding", []string{"agent:run", "agent/workflow:bug"}, ""},
		{"conflicting selectors", []string{"agent:run", "agent/workflow:configured", "agent/workflow:other"}, ""},
		{"unauthorized", []string{"agent/workflow:configured"}, ""},
		{"unsupported mode", []string{"agent:run", "agent/mode:observe"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue := githubIssue{State: "open"}
			for _, label := range tc.labels {
				issue.Labels = append(issue.Labels, gitHubLabelResponse{Name: label})
			}
			got, snapshot, err := e.selectIssueBindingLocked(config, issue)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("selection unexpectedly succeeded: %+v", got)
				}
				return
			}
			if err != nil || got.ID != tc.want || snapshot.ID != got.WorkflowID {
				t.Fatalf("selection = %+v, %v", got, err)
			}
		})
	}
	if err := e.checkIssueBindingLocked("p", "r", ""); err == nil {
		t.Fatal("missing binding was accepted")
	}
	otherB.Repositories["source"] = "another-repository"
	e.bindings[otherB.ID] = otherB
	if err := e.checkIssueBindingLocked("p", "r", otherB.ID); err == nil {
		t.Fatal("binding for another repository was accepted")
	}
	w.Nodes = append(w.Nodes, WorkflowNode{ID: "check", Kind: "validation"})
	e.workflows[w.ID] = w
	if err := e.checkIssueBindingLocked("p", "r", b.ID); err == nil || !strings.Contains(err.Error(), "one agent node") {
		t.Fatalf("unsupported graph accepted: %v", err)
	}
}

func TestIntakeWithoutIssueBindingDoesNoWork(t *testing.T) {
	data := t.TempDir()
	store, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{store: store, data: data, ctx: ctx, stop: cancel, projects: map[string]Project{}, intakes: map[string]intakeConfig{}, intakeBusy: map[string]bool{}, runs: map[string]savedRun{}, active: map[string]activeExecution{}, errors: make(chan error, 1)}
	defer e.Close()
	e.mu.Lock()
	e.projects["p"] = Project{ID: "p", Repositories: []Repository{{ID: "r", Path: t.TempDir()}}}
	e.intakes[githubKey("p", "r")] = intakeConfig{Enabled: true, ProjectID: "p", RepositoryID: "r"}
	e.mu.Unlock()
	if _, err := e.setRepositoryIntake("p", "r", true); err == nil || !strings.Contains(err.Error(), "issue workflow binding") {
		t.Fatalf("enable without binding = %v", err)
	}
	report, err := e.pollRepositoryIntake(context.Background(), "p", "r")
	if err != nil || !strings.Contains(report.Error, "issue workflow binding") || len(report.Jobs) != 0 || len(e.Runs("p")) != 0 {
		t.Fatalf("unconfigured intake did work: %+v, %v", report, err)
	}
}

func TestIssueAndRevisionSelectionAPIPersistsWithoutSeeding(t *testing.T) {
	f := newWorkspaceFixture(t)
	store, err := openStore(f.data)
	if err != nil {
		t.Fatal(err)
	}
	p := Project{ID: "p", Name: "Project", Repositories: []Repository{{ID: "r", Name: "Repository", Path: f.repository}}, Harness: Harness{Binary: "omp", Model: "test"}, Policy: Policy{MaxParallel: 1}}
	w, b := issueBindingFixture("p", "r", "configured", "User issue prompt")
	revisionW := Workflow{ID: "revision", Name: "User revision", Inputs: map[string]string{"pullRequest": "object", "feedback": "array"}, Nodes: []WorkflowNode{{ID: "revise", Name: "Revise", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "User revision prompt", "feedbackSources": []any{"inline"}}, Inputs: map[string]Input{"pullRequest": {Type: "object", From: "inputs.pullRequest"}, "feedback": {Type: "array", From: "inputs.feedback"}}}}}
	revisionB := Binding{ID: "revision-binding", Name: "Revision", ProjectID: "p", WorkflowID: revisionW.ID, Repositories: map[string]string{"source": "r"}}
	syncW := Workflow{ID: "sync", Name: "User synchronization", Inputs: map[string]string{"pullRequest": "object", "synchronization": "object"}, Nodes: []WorkflowNode{{ID: "sync", Name: "Synchronize", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "Resolve and validate the prepared merge."}, Inputs: map[string]Input{"pullRequest": {Type: "object", From: "inputs.pullRequest"}, "synchronization": {Type: "object", From: "inputs.synchronization"}}}}}
	syncB := Binding{ID: "sync-binding", Name: "Synchronization", ProjectID: "p", WorkflowID: syncW.ID, Repositories: map[string]string{"source": "r"}}
	if err := store.save(record{"project", p.ID, p}, record{"workflow", w.ID, w}, record{"binding", b.ID, b}, record{"workflow", revisionW.ID, revisionW}, record{"binding", revisionB.ID, revisionB}, record{"workflow", syncW.ID, syncW}, record{"binding", syncB.ID, syncB}); err != nil {
		t.Fatal(err)
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	e, err := Open(f.data)
	if err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ suffix, binding string }{{"issue", b.ID}, {"review", revisionB.ID}, {"sync", syncB.ID}} {
		request := httptest.NewRequest("PUT", "http://localhost/api/projects/p/repositories/r/intake/"+endpoint.suffix, strings.NewReader(`{"bindingId":"`+endpoint.binding+`"}`))
		recorder := httptest.NewRecorder()
		e.Handler("", false).ServeHTTP(recorder, request)
		if recorder.Code != 200 {
			t.Fatalf("binding API %s: %d %s", endpoint.suffix, recorder.Code, recorder.Body.String())
		}
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = Open(f.data)
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.repositoryIntake("p", "r")
	if err != nil || report.IssueBindingID != b.ID || report.ReviewBindingID != revisionB.ID || report.SyncBindingID != syncB.ID || len(e.Workflows()) != 3 {
		t.Fatalf("restart lost configured bindings: %+v, %v", report, err)
	}
	if _, err := e.setIssueBinding("p", "r", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setReviewBinding("p", "r", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.setSyncBinding("p", "r", ""); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e, err = Open(f.data)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	report, err = e.repositoryIntake("p", "r")
	if err != nil || report.IssueBindingID != "" || report.ReviewBindingID != "" || report.SyncBindingID != "" || len(e.Workflows()) != 3 {
		t.Fatalf("restart restored removed selections or deleted user workflows: %+v, %v", report, err)
	}
}

func TestIssueExecutionUsesSnapshotPromptAndMappedInputs(t *testing.T) {
	data := t.TempDir()
	worktree := t.TempDir()
	capture := filepath.Join(data, "prompt")
	harness := filepath.Join(data, "harness")
	script := "#!/bin/sh\nwhile [ \"$#\" -gt 0 ]; do\n if [ \"$1\" = '-p' ]; then shift; printf '%s' \"$1\" > \"$FACTORY_CAPTURE\"; break; fi\n shift\ndone\nprintf 'configured behavior' > changed.txt\nprintf 'completed configured work\\n'\n"
	if err := os.WriteFile(harness, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FACTORY_CAPTURE", capture)
	store, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{store: store, data: data, ctx: ctx, stop: cancel, runs: map[string]savedRun{}, issueJobs: map[string]savedIssueJob{}, active: map[string]activeExecution{}, wake: make(chan struct{}, 1), errors: make(chan error, 1)}
	defer e.Close()
	w, b := issueBindingFixture("p", "r", "configured", "EXACT USER PROMPT")
	p := Project{ID: "p", Repositories: []Repository{{ID: "r", Path: t.TempDir()}}, Harness: Harness{Binary: harness, Model: "test"}}
	job := savedIssueJob{IssueJob: IssueJob{ID: "job", ProjectID: "p", RepositoryID: "r", Number: 17, Title: "Issue title", URL: "https://github.com/owner/repo/issues/17", Worktree: worktree, BaseSHA: strings.Repeat("a", 40)}, Project: p, Repository: p.Repositories[0], Binding: copyJSON(b), Workflow: copyJSON(w), Body: "Issue body"}
	// Editing the live workflow after admission must not change this job.
	w.Nodes[0].Config["prompt"] = "CHANGED LIVE PROMPT"
	if err := e.executeIssueAgent(ctx, &job); err != nil {
		t.Fatal(err)
	}
	prompt, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	text := string(prompt)
	if !strings.HasPrefix(text, "EXACT USER PROMPT") || strings.Contains(text, "CHANGED LIVE PROMPT") || !strings.Contains(text, "Factory-managed issue") {
		t.Fatalf("configured prompt not preserved: %s", text)
	}
	parts := strings.SplitN(text, "Workflow inputs (typed JSON data):\n", 2)
	if len(parts) != 2 {
		t.Fatalf("agent inputs not delivered: %s", text)
	}
	var inputs map[string]Value
	if err := json.Unmarshal([]byte(parts[1]), &inputs); err != nil {
		t.Fatal(err)
	}
	issue := inputs["issue"].Value.(map[string]any)
	if inputs["option"].Value != "bound option" || inputs["setting"].Type != "" || issue["title"] != job.Title || issue["body"] != job.Body || issue["baseSHA"] != job.BaseSHA || issue["number"] != float64(17) {
		t.Fatalf("wrong mapped agent inputs: %+v", inputs)
	}
	if content, err := os.ReadFile(filepath.Join(worktree, "changed.txt")); err != nil || string(content) != "configured behavior" {
		t.Fatalf("agent did not run in managed worktree: %q, %v", content, err)
	}
	run := e.runs[job.RunID]
	if run.Binding.ID != b.ID || run.Workflow.ID != b.WorkflowID || run.Run.Nodes["configured-agent"].Status != "succeeded" || run.Run.Nodes["factory-publish"].Status != "pending" {
		t.Fatalf("wrong managed execution state: %+v", run.Run)
	}
}

func TestRestartDoesNotInventWorkflowForLegacyIssueJobs(t *testing.T) {
	data := t.TempDir()
	store, err := openStore(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "paused", "publishing"} {
		job := savedIssueJob{IssueJob: IssueJob{ID: status, Status: status, Worktree: "retained-worktree", Branch: "factory/issue-" + status, HeadSHA: strings.Repeat("a", 40)}}
		if err := store.save(record{"issue-job", job.ID, job}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.close(); err != nil {
		t.Fatal(err)
	}
	e, err := Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	for _, status := range []string{"queued", "paused", "publishing"} {
		job := e.issueJobs[status]
		if job.Status != "paused" || !strings.Contains(job.Error, "no configured workflow snapshot") || job.Worktree != "retained-worktree" || job.HeadSHA == "" || job.Workflow.ID != "" || job.Binding.ID != "" {
			t.Fatalf("legacy candidate was migrated or lost evidence: %+v", job)
		}
	}
	if len(e.Workflows()) != 0 || len(e.Runs("")) != 0 {
		t.Fatal("restart invented issue work")
	}
}
