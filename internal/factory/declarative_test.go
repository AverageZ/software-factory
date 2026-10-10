package factory

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func declarationTestWorkflow() Workflow {
	return Workflow{
		ID: "declarative", Name: "Declarative workflow",
		Inputs: map[string]string{"event": "object"},
		Declaration: &Declaration{
			Version: 1,
			Actions: map[string]WorkflowNode{
				"work": {Kind: "command", Repository: "repo", Config: map[string]any{"command": []any{"sh", "-c", "printf '%s' '{}'"}, "outputJson": true}},
			},
			Steps: []DeclarationStep{{ID: "first", Run: "work"}},
		},
	}
}

func declarationTestCondition(path, operator string, value any) *DeclarationCondition {
	return &DeclarationCondition{Path: path, Operator: operator, Value: value}
}

func TestDeclarationIgnoresSubmittedGraphProjection(t *testing.T) {
	w := declarationTestWorkflow()
	w.Nodes = []WorkflowNode{{ID: "untrusted", Kind: "command"}}
	w.Edges = []Edge{{ID: "broken", Source: "untrusted", Target: "missing"}}
	w.Declaration.Given = declarationTestCondition("event.enabled", "equals", true)
	w.Declaration.Steps = []DeclarationStep{
		{ID: "declaration-1", Run: "work", If: declarationTestCondition("event.first", "equals", true)},
		{ID: "next", Run: "work"},
	}
	got, err := compileDeclaration(w)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWorkflow(got, nil); err != nil {
		t.Fatal(err)
	}
	if _, exists := workflowNode(got, "untrusted"); exists {
		t.Fatal("submitted derived graph overrode the declaration")
	}
}

func TestDeclarationRejectsInvalidReferencesAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Workflow)
	}{
		{"version", func(w *Workflow) { w.Declaration.Version = 2 }},
		{"empty steps", func(w *Workflow) { w.Declaration.Steps = nil }},
		{"missing action", func(w *Workflow) { w.Declaration.Steps[0].Run = "missing" }},
		{"graph action", func(w *Workflow) { w.Declaration.Actions["unused"] = WorkflowNode{Kind: "parallel"} }},
		{"duplicate step", func(w *Workflow) { w.Declaration.Steps = append(w.Declaration.Steps, w.Declaration.Steps[0]) }},
		{"reserved step", func(w *Workflow) { w.Declaration.Steps[0].ID = "inputs" }},
		{"invalid step", func(w *Workflow) { w.Declaration.Steps[0].ID = "has.dot" }},
		{"shadow input", func(w *Workflow) { w.Declaration.Steps[0].As = "event" }},
		{"duplicate alias", func(w *Workflow) {
			w.Declaration.Steps[0].As = "Result"
			w.Declaration.Steps = append(w.Declaration.Steps, DeclarationStep{ID: "second", Run: "work", As: "Result"})
		}},
		{"future condition", func(w *Workflow) {
			w.Declaration.Steps[0].As = "Result"
			w.Declaration.Steps[0].If = declarationTestCondition("Result.ready", "equals", true)
		}},
		{"given result", func(w *Workflow) { w.Declaration.Given = declarationTestCondition("Result.ready", "equals", true) }},
		{"unknown path", func(w *Workflow) { w.Declaration.Given = declarationTestCondition("missing", "exists", nil) }},
		{"empty path component", func(w *Workflow) { w.Declaration.Given = declarationTestCondition("event..ready", "exists", nil) }},
		{"unknown operator", func(w *Workflow) { w.Declaration.Given = declarationTestCondition("event", "matchesRegex", "x") }},
		{"missing comparison value", func(w *Workflow) { w.Declaration.Given = declarationTestCondition("event", "equals", nil) }},
		{"empty condition group", func(w *Workflow) { w.Declaration.Given = &DeclarationCondition{All: []DeclarationCondition{}} }},
		{"mixed condition", func(w *Workflow) {
			w.Declaration.Given = &DeclarationCondition{Any: []DeclarationCondition{{Path: "event", Operator: "exists"}}, Path: "event"}
		}},
		{"future input", func(w *Workflow) {
			w.Declaration.Steps[0].Inputs = map[string]Input{"x": {Type: "object", From: "Result"}}
		}},
		{"graph reference", func(w *Workflow) {
			w.Declaration.Steps[0].Inputs = map[string]Input{"x": {Type: "object", From: "inputs.event"}}
		}},
		{"input type mismatch", func(w *Workflow) {
			w.Declaration.Steps[0].Inputs = map[string]Input{"x": {Type: "string", From: "event"}}
		}},
		{"mixed input", func(w *Workflow) {
			w.Declaration.Steps[0].Inputs = map[string]Input{"x": {Type: "object", From: "event", Value: map[string]any{}}}
		}},
		{"invalid literal", func(w *Workflow) {
			w.Declaration.Steps[0].Inputs = map[string]Input{"x": {Type: "boolean", Value: "true"}}
		}},
		{"too many steps", func(w *Workflow) { w.Declaration.Steps = make([]DeclarationStep, maxDeclarationSteps+1) }},
		{"too many actions", func(w *Workflow) {
			for i := range maxDeclarationSteps {
				w.Declaration.Actions[fmt.Sprintf("work%d", i)] = w.Declaration.Actions["work"]
			}
		}},
		{"oversized declaration", func(w *Workflow) {
			w.Declaration.Actions["work"].Config["command"] = []any{strings.Repeat("x", maxDeclarationBytes)}
		}},
		{"too deep", func(w *Workflow) {
			c := DeclarationCondition{Path: "event", Operator: "exists"}
			for range maxConditionDepth + 1 {
				c = DeclarationCondition{All: []DeclarationCondition{c}}
			}
			w.Declaration.Given = &c
		}},
		{"too many rules", func(w *Workflow) {
			rules := make([]DeclarationCondition, maxConditionRules+1)
			for i := range rules {
				rules[i] = DeclarationCondition{Path: "event", Operator: "exists"}
			}
			w.Declaration.Given = &DeclarationCondition{Any: rules}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := declarationTestWorkflow()
			tc.edit(&w)
			if _, err := compileDeclaration(w); err == nil {
				t.Fatal("invalid declaration was accepted")
			}
		})
	}
}

func TestDeclarationAcceptsBoundedMaximumGraphAndConditions(t *testing.T) {
	w := declarationTestWorkflow()
	rules := make([]DeclarationCondition, maxConditionRules)
	for i := range rules {
		rules[i] = DeclarationCondition{Path: "event", Operator: "exists"}
	}
	w.Declaration.Given = &DeclarationCondition{All: rules}
	w.Declaration.Steps = make([]DeclarationStep, maxDeclarationSteps)
	for i := range w.Declaration.Steps {
		w.Declaration.Steps[i] = DeclarationStep{ID: fmt.Sprintf("step%d", i), Run: "work", If: declarationTestCondition("event", "exists", nil)}
	}
	_, err := compileDeclaration(w)
	if err != nil {
		t.Fatal(err)
	}
	c := DeclarationCondition{Path: "event", Operator: "exists"}
	for range maxConditionDepth {
		c = DeclarationCondition{All: []DeclarationCondition{c}}
	}
	w.Declaration.Given = &c
	if _, err := compileDeclaration(w); err != nil {
		t.Fatalf("maximum supported condition depth was rejected: %v", err)
	}
}

func TestDeclarationStrictJSONAndExplicitNull(t *testing.T) {
	for _, data := range []string{
		`{"version":1,"actions":{},"steps":[],"typo":true}`,
		`{"version":1,"actions":{"a":{"kind":"command","typo":true}},"steps":[]}`,
		`{"version":1,"actions":{},"steps":[{"id":"x","run":"a","typo":true}]}`,
		`{"version":1,"actions":{},"steps":[{"id":"x","run":"a","inputs":{"x":{"type":"object","from":"event","typo":true}}}]}`,
		`{"version":1,"actions":{"a":{"kind":"command","inputs":{"x":{"type":"object","from":"event","typo":true}}}},"steps":[]}`,
		`{"version":1,"defaults":{"maxIterations":null},"actions":{},"steps":[]}`,
		`{"version":1,"defaults":{"timeoutSeconds":null},"actions":{},"steps":[]}`,
		`{"version":1,"defaults":{"model":null},"actions":{},"steps":[]}`,
		`{"version":1,"given":{"all":[]},"actions":{},"steps":[]}`,
		`{"version":1,"given":{"any":null},"actions":{},"steps":[]}`,
		`{"version":1,"given":{"path":"event","operator":"exists","typo":true},"actions":{},"steps":[]}`,
		`{"version":1,"given":{"all":[{"path":"event","operator":"exists"}],"path":""},"actions":{},"steps":[]}`,
	} {
		var d Declaration
		if err := json.Unmarshal([]byte(data), &d); err == nil {
			t.Errorf("accepted invalid JSON declaration: %s", data)
		}
	}
	w := declarationTestWorkflow()
	if err := json.Unmarshal([]byte(`{"path":"event.optional","operator":"equals","value":null}`), &w.Declaration.Given); err != nil {
		t.Fatal(err)
	}
	compiled, err := compileDeclaration(w)
	if err != nil {
		t.Fatal(err)
	}
	gate := compiled.Nodes[0]
	conditions := gate.Config["conditions"].(map[string]any)
	if !evaluateBranchConditions(conditions, map[string]Value{"event": {Type: "object", Value: map[string]any{"optional": nil}}}) {
		t.Fatal("explicit JSON null comparison was lost")
	}
}

func TestDeclarationEmptyActionInputsAllowZeroInputChild(t *testing.T) {
	w := declarationTestWorkflow()
	w.Inputs = map[string]string{"WorkItem": "object"}
	w.Declaration.Actions = map[string]WorkflowNode{
		"child": {Kind: "workflow", Inputs: map[string]Input{}, Config: map[string]any{"workflowId": "child"}},
	}
	w.Declaration.Steps = []DeclarationStep{{ID: "invoke", Run: "child"}}
	child := Workflow{ID: "child", Name: "Child", Nodes: []WorkflowNode{{ID: "review", Kind: "approval", Config: map[string]any{"message": "Review"}}}}
	// SaveWorkflow and durable reads both use JSON, which must not collapse {}
	// into omitted inputs and accidentally forward WorkItem to this child.
	w = copyJSON(w)
	compiled, err := compileDeclaration(w)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Nodes[0].Inputs) != 0 || compiled.Declaration.Actions["child"].Inputs == nil {
		t.Fatal("explicit empty action inputs were lost")
	}
	if err := validateDefinitions(map[string]Workflow{compiled.ID: compiled, child.ID: child}); err != nil {
		t.Fatal(err)
	}
}

// Run through the public save/bind/start API and real scheduler/process executor.
func runDeclarationTest(t *testing.T, w Workflow, event map[string]any) (Run, Workflow) {
	t.Helper()
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	repo := t.TempDir()
	workspaceTestGit(t, repo, "init", "--initial-branch=main")
	project, err := e.SaveProject(Project{ID: "project", Name: "Project", Repositories: []Repository{{ID: "repository", Name: "Repository", Path: repo}}, Harness: Harness{Binary: "sh"}})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := e.SaveWorkflow(w)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := e.SaveBinding(Binding{ID: "binding", Name: "Binding", ProjectID: project.ID, WorkflowID: compiled.ID, Repositories: map[string]string{"repo": "repository"}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := e.Start(binding.ID, map[string]Value{"event": {Type: "object", Value: event}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !terminal(run.Status) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
		run, err = e.Run(run.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !terminal(run.Status) {
		t.Fatalf("workflow did not settle: %+v", run)
	}
	return run, compiled
}

func TestDeclarationEngineGivenAndIndependentIfContinuation(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		enabled, first, second           bool
		wantFirst, wantSecond, wantFinal string
	}{
		{"both match", true, true, true, "succeeded", "succeeded", "succeeded"},
		{"first false continues", true, false, true, "skipped", "succeeded", "succeeded"},
		{"second false continues", true, true, false, "succeeded", "skipped", "succeeded"},
		{"both false continue", true, false, false, "skipped", "skipped", "succeeded"},
		{"given false guards all", false, true, true, "skipped", "skipped", "skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := declarationTestWorkflow()
			w.Declaration.Given = declarationTestCondition("event.enabled", "equals", true)
			w.Declaration.Steps = []DeclarationStep{
				{ID: "first", Run: "work", If: declarationTestCondition("event.first", "equals", true)},
				{ID: "second", Run: "work", If: declarationTestCondition("event.second", "equals", true)},
				{ID: "final", Run: "work"},
			}
			run, _ := runDeclarationTest(t, w, map[string]any{"enabled": tc.enabled, "first": tc.first, "second": tc.second})
			if run.Status != "succeeded" || run.Nodes["first"].Status != tc.wantFirst || run.Nodes["second"].Status != tc.wantSecond || run.Nodes["final"].Status != tc.wantFinal {
				t.Fatalf("incorrect continuation: %+v", run)
			}
		})
	}
}

func TestDeclarationEngineStructuredDiagnosisBranches(t *testing.T) {
	for _, tc := range []struct {
		name, output, code, labels string
	}{
		{"both diagnoses", `{"needs":["code","labels"],"status":"Ready"}`, "succeeded", "succeeded"},
		{"labels only", `{"needs":["labels"],"status":"Ready"}`, "skipped", "succeeded"},
		{"code only", `{"needs":["code"],"status":"Ready"}`, "succeeded", "skipped"},
		{"missing properties", `{}`, "skipped", "skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := declarationTestWorkflow()
			w.Declaration.Actions["diagnose"] = WorkflowNode{Kind: "command", Repository: "repo", Config: map[string]any{"command": []any{"sh", "-c", "printf '%s' '" + tc.output + "'"}, "outputJson": true}}
			w.Declaration.Steps = []DeclarationStep{
				{ID: "diagnose", Run: "diagnose", As: "Diagnosis"},
				{ID: "code", Run: "work", If: &DeclarationCondition{All: []DeclarationCondition{
					{Path: "Diagnosis.status", Operator: "equals", Value: "Ready"},
					{Path: "Diagnosis.needs", Operator: "contains", Value: "code"},
				}}},
				{ID: "labels", Run: "work", If: &DeclarationCondition{Any: []DeclarationCondition{
					{Path: "Diagnosis.needs", Operator: "contains", Value: "labels"},
					{Path: "event.forceLabels", Operator: "equals", Value: true},
				}}},
				{ID: "final", Run: "work", Inputs: map[string]Input{"diagnosis": {Type: "object", From: "Diagnosis"}}},
			}
			run, _ := runDeclarationTest(t, w, map[string]any{})
			if run.Status != "succeeded" || run.Nodes["code"].Status != tc.code || run.Nodes["labels"].Status != tc.labels || run.Nodes["final"].Status != "succeeded" {
				t.Fatalf("independent diagnosis branches routed incorrectly: %+v", run)
			}
			if run.Nodes["diagnose"].Attempt != 1 {
				t.Fatal("diagnosis unexpectedly ran more than once")
			}
		})
	}
}

func TestDeclarationEngineFailureBlocksContinuation(t *testing.T) {
	for _, output := range []string{"printf '%s' '{}'; exit 7", "printf '%s' 'invalid-json'"} {
		t.Run(output, func(t *testing.T) {
			w := declarationTestWorkflow()
			w.Declaration.Actions["bad"] = WorkflowNode{Kind: "command", Repository: "repo", Config: map[string]any{"command": []any{"sh", "-c", output}, "outputJson": true}}
			w.Declaration.Steps = []DeclarationStep{
				{ID: "failed", Run: "bad", If: declarationTestCondition("event.enabled", "equals", true)},
				{ID: "after", Run: "work"},
			}
			run, _ := runDeclarationTest(t, w, map[string]any{"enabled": true})
			if run.Status != "failed" || run.Nodes["failed"].Status != "failed" || run.Nodes["after"].Status != "failed" || run.Nodes["after"].Attempt != 0 {
				t.Fatalf("failure bypassed continuation join: %+v", run)
			}
		})
	}
}

func TestDeclarationEngineSkippedAliasFailsClosed(t *testing.T) {
	w := declarationTestWorkflow()
	w.Declaration.Steps = []DeclarationStep{
		{ID: "producer", Run: "work", As: "Result", If: declarationTestCondition("event.enabled", "equals", true)},
		{ID: "consumer", Run: "work", If: declarationTestCondition("Result.status", "notExists", nil)},
		{ID: "after", Run: "work"},
	}
	run, _ := runDeclarationTest(t, w, map[string]any{"enabled": false})
	if run.Status != "failed" || run.Nodes["producer"].Status != "skipped" || run.Nodes["consumer"].Attempt != 0 || run.Nodes["after"].Attempt != 0 {
		t.Fatalf("skipped result became a successful condition: %+v", run)
	}
	if !strings.Contains(run.Error, "unavailable") && !strings.Contains(run.Error, "Upstream") {
		t.Fatalf("missing skipped source failure: %s", run.Error)
	}
}
