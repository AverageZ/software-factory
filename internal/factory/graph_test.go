package factory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBranchMergeWaitsForAllPredecessorsButIgnoresSkippedPath(t *testing.T) {
	r := savedRun{Workflow: Workflow{Edges: []Edge{{Source: "left", Target: "merge"}, {Source: "right", Target: "merge"}}}, Run: Run{Nodes: map[string]NodeExecution{"left": {Status: "succeeded"}, "right": {Status: "pending"}}}}
	ready, _, _, _ := dependencies(r, "merge")
	if ready {
		t.Fatal("merge ran before the other predecessor settled")
	}
	r.Run.Nodes["right"] = NodeExecution{Status: "skipped"}
	ready, selected, message, _ := dependencies(r, "merge")
	if !ready || !selected || message != "" {
		t.Fatalf("selected path did not activate merge: ready=%v selected=%v error=%q", ready, selected, message)
	}
	r.Run.Nodes["left"] = NodeExecution{Status: "skipped"}
	ready, selected, message, _ = dependencies(r, "merge")
	if !ready || selected || message != "" {
		t.Fatal("all skipped predecessors must skip the merge")
	}
	r.Run.Nodes["left"] = NodeExecution{Status: "failed"}
	ready, _, message, status := dependencies(r, "merge")
	if !ready || message == "" || status != "failed" {
		t.Fatal("upstream failure was incorrectly treated as an unselected branch")
	}
}

func TestSelectedMergeCannotReadOutputFromSkippedPath(t *testing.T) {
	r := savedRun{Run: Run{Nodes: map[string]NodeExecution{"skipped": {Status: "skipped", Outputs: map[string]Value{"result": {Type: "object", Value: map[string]any{"old": true}}}}}}}
	n := WorkflowNode{Inputs: map[string]Input{"payload": {Type: "object", From: "skipped.result"}}}
	if _, err := resolveInputs(&r, n); err == nil {
		t.Fatal("a skipped node's stale output was accepted")
	}
	r.Run.Nodes["skipped"] = NodeExecution{Status: "succeeded", Outputs: map[string]Value{}}
	if _, err := resolveInputs(&r, n); err == nil {
		t.Fatal("a missing output was silently accepted")
	}
}

func TestValidationRejectsCyclesAndUnorderedOrMistypedData(t *testing.T) {
	base := Workflow{ID: "review", Name: "Review", Nodes: []WorkflowNode{{ID: "source", Kind: "parallel"}, {ID: "sink", Kind: "parallel", Inputs: map[string]Input{"data": {Type: "object", From: "source.result"}}}}, Edges: []Edge{{ID: "forward", Source: "source", Target: "sink"}}}
	if err := validateDefinitions(map[string]Workflow{base.ID: base}); err != nil {
		t.Fatal(err)
	}
	t.Run("graph cycle", func(t *testing.T) {
		w := copyJSON(base)
		w.Edges = append(w.Edges, Edge{ID: "back", Source: "sink", Target: "source"})
		if err := validateDefinitions(map[string]Workflow{w.ID: w}); err == nil {
			t.Fatal("cyclic execution graph accepted")
		}
	})
	t.Run("missing dependency", func(t *testing.T) {
		w := copyJSON(base)
		w.Edges = nil
		if err := validateDefinitions(map[string]Workflow{w.ID: w}); err == nil {
			t.Fatal("data reference can race its producer")
		}
	})
	t.Run("wrong output name", func(t *testing.T) {
		w := copyJSON(base)
		w.Nodes[1].Inputs["data"] = Input{Type: "object", From: "source.unknown"}
		if err := validateDefinitions(map[string]Workflow{w.ID: w}); err == nil {
			t.Fatal("unknown output name accepted")
		}
	})
	t.Run("type mismatch", func(t *testing.T) {
		w := copyJSON(base)
		w.Nodes[1].Inputs["data"] = Input{Type: "string", From: "source.result"}
		if err := validateDefinitions(map[string]Workflow{w.ID: w}); err == nil {
			t.Fatal("mismatched typed edge accepted")
		}
	})
	t.Run("recursive children", func(t *testing.T) {
		a := Workflow{ID: "a", Name: "A", Nodes: []WorkflowNode{{ID: "nested", Kind: "workflow", Config: map[string]any{"workflowId": "b"}}}}
		b := Workflow{ID: "b", Name: "B", Nodes: []WorkflowNode{{ID: "nested", Kind: "workflow", Config: map[string]any{"workflowId": "a"}}}}
		if err := validateDefinitions(map[string]Workflow{"a": a, "b": b}); err == nil {
			t.Fatal("recursive child definitions accepted")
		}
	})
}

func TestExplicitRetryPreservesCompletedWorkAndClearsDependentFailure(t *testing.T) {
	r := savedRun{Workflow: Workflow{Edges: []Edge{{Source: "done", Target: "review"}, {Source: "review", Target: "publish"}}}, Run: Run{Status: "failed", Nodes: map[string]NodeExecution{
		"done":    {Status: "succeeded", Attempt: 1, Outputs: map[string]Value{"result": {Type: "object", Value: map[string]any{"committed": true}}}},
		"review":  {Status: "failed", Attempt: 2, Error: "rejected", Outputs: map[string]Value{"result": {Type: "Approval", Value: map[string]any{"approved": false}}}},
		"publish": {Status: "failed", Error: "upstream review failed"},
	}}}
	resetDescendants(&r, "review")
	if x := r.Run.Nodes["done"]; x.Status != "succeeded" || x.Attempt != 1 || x.Outputs["result"].Value.(map[string]any)["committed"] != true {
		t.Fatal("retry invalidated completed side effects")
	}
	if x := r.Run.Nodes["review"]; x.Status != "pending" || x.Attempt != 2 || len(x.Outputs) != 0 || x.Error != "" {
		t.Fatal("retry retained rejected approval or lost attempt history")
	}
	if r.Run.Nodes["publish"].Status != "pending" || r.Run.Status != "queued" {
		t.Fatal("retry did not unblock dependent execution")
	}
}

func branchRecipeWorkflow(config map[string]any) Workflow {
	return Workflow{
		ID: "recipe", Name: "Recipe",
		Inputs: map[string]string{"payload": "object"},
		Nodes: []WorkflowNode{
			{ID: "gate", Kind: "branch", Inputs: map[string]Input{"payload": {Type: "object", From: "inputs.payload"}}, Config: config},
			{ID: "yes", Kind: "parallel"},
			{ID: "no", Kind: "parallel"},
		},
		Edges: []Edge{
			{ID: "true-edge", Source: "gate", Target: "yes", When: "true"},
			{ID: "false-edge", Source: "gate", Target: "no", When: "false"},
		},
	}
}

func TestBranchConditionWorkflowValidation(t *testing.T) {
	conditions := map[string]any{
		"combinator": "all",
		"rules": []any{
			map[string]any{"input": "payload", "property": "user.name", "operator": "exists"},
			map[string]any{"combinator": "any", "rules": []any{
				map[string]any{"input": "payload", "property": "user.role", "operator": "equals", "value": "admin"},
				map[string]any{"input": "payload", "property": "user.role", "operator": "notEquals", "value": nil},
			}},
		},
	}
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.SaveWorkflow(branchRecipeWorkflow(map[string]any{"conditions": conditions})); err != nil {
		t.Fatalf("nested recipe was rejected: %v", err)
	}
	invalidRules := map[string]any{
		"empty group":      map[string]any{"combinator": "all", "rules": []any{}},
		"unknown group":    map[string]any{"combinator": "all", "rules": []any{map[string]any{"combinator": "xor", "rules": []any{map[string]any{"input": "payload", "operator": "exists"}}}}},
		"wrong rules type": map[string]any{"combinator": "all", "rules": map[string]any{}},
		"missing input":    map[string]any{"combinator": "all", "rules": []any{map[string]any{"operator": "exists"}}},
		"unknown input":    map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "other", "operator": "exists"}}},
		"invalid path":     map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "payload", "property": "user..role", "operator": "exists"}}},
		"missing value":    map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "payload", "operator": "equals"}}},
		"extra value":      map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "payload", "operator": "notExists", "value": nil}}},
		"unknown operator": map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "payload", "operator": "contains", "value": "a"}}},
		"extra field":      map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "payload", "operator": "exists", "ignored": true}}},
	}
	deep := map[string]any{"input": "payload", "operator": "exists"}
	for range maxConditionDepth + 1 {
		deep = map[string]any{"combinator": "all", "rules": []any{deep}}
	}
	invalidRules["excessive depth"] = deep
	oversized := make([]any, maxConditionRules+1)
	for i := range oversized {
		oversized[i] = map[string]any{"input": "payload", "operator": "exists"}
	}
	invalidRules["excessive rules"] = map[string]any{"combinator": "all", "rules": oversized}
	for name, rule := range invalidRules {
		t.Run(name, func(t *testing.T) {
			if _, err := e.SaveWorkflow(branchRecipeWorkflow(map[string]any{"conditions": rule})); err == nil {
				t.Fatal("malformed conditions were saved")
			}
		})
	}
	if _, err := e.SaveWorkflow(branchRecipeWorkflow(map[string]any{"conditions": conditions, "input": "payload", "equals": true})); err == nil {
		t.Fatal("mixed legacy and recipe branch was saved")
	}
	if _, err := e.SaveWorkflow(branchRecipeWorkflow(map[string]any{"input": "payload", "equals": nil})); err != nil {
		t.Fatalf("legacy branch with null comparison was rejected: %v", err)
	}
}

func TestBranchConditionsRouteResolvedInputs(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal([]byte(`{"conditions":{"combinator":"all","rules":[{"combinator":"any","rules":[{"input":"payload","property":"user.role","operator":"equals","value":"admin"},{"input":"payload","property":"user.role","operator":"notEquals","value":null}]},{"input":"payload","property":"user.name","operator":"exists"},{"input":"payload","property":"user.missing","operator":"notExists"}]}}`), &config); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload map[string]any
		want    bool
	}{
		{"matched nested group", map[string]any{"user": map[string]any{"role": "admin", "name": "Ada"}}, true},
		{"alternate nested rule", map[string]any{"user": map[string]any{"role": "editor", "name": "Ada"}}, true},
		{"null role", map[string]any{"user": map[string]any{"role": nil, "name": "Ada"}}, false},
		{"missing name", map[string]any{"user": map[string]any{"role": "admin"}}, false},
		{"null name", map[string]any{"user": map[string]any{"role": "admin", "name": nil}}, false},
		{"present missing field", map[string]any{"user": map[string]any{"role": "admin", "name": "Ada", "missing": "here"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			workflow := branchRecipeWorkflow(config)
			if _, err := e.SaveWorkflow(workflow); err != nil {
				t.Fatal(err)
			}
			r := newRun(Project{ID: "project"}, Binding{}, workflow, nil, map[string]Value{"payload": {Type: "object", Value: tc.payload}}, "")
			e.mu.Lock()
			e.runs[r.Run.ID] = r
			for range 3 {
				if _, err := e.pumpLocked(); err != nil {
					e.mu.Unlock()
					t.Fatal(err)
				}
			}
			got := e.runs[r.Run.ID].Run
			e.mu.Unlock()
			if got.Nodes["gate"].Status != "succeeded" || got.Nodes["gate"].Outputs["result"].Value != tc.want {
				t.Fatalf("branch result: %+v", got.Nodes["gate"])
			}
			yes, no := got.Nodes["yes"].Status, got.Nodes["no"].Status
			if tc.want && (yes != "succeeded" || no != "skipped") || !tc.want && (yes != "skipped" || no != "succeeded") {
				t.Fatalf("incorrect true/false routing: yes=%s no=%s", yes, no)
			}
		})
	}
}

func TestBranchInvalidSnapshotFailsWithoutSelectingPath(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	workflow := branchRecipeWorkflow(map[string]any{"conditions": map[string]any{"combinator": "all", "rules": []any{}}})
	r := newRun(Project{ID: "project"}, Binding{}, workflow, nil, map[string]Value{"payload": {Type: "object", Value: map[string]any{}}}, "")
	e.mu.Lock()
	e.runs[r.Run.ID] = r
	_, err = e.pumpLocked()
	got := e.runs[r.Run.ID].Run.Nodes["gate"]
	e.mu.Unlock()
	if err != nil || got.Status != "failed" || !strings.Contains(got.Error, "Invalid branch conditions") || len(got.Outputs) != 0 {
		t.Fatalf("invalid condition succeeded or crashed scheduler: %+v, %v", got, err)
	}
}

func TestBranchConditionPresenceAndJSONNull(t *testing.T) {
	inputs := map[string]Input{"payload": {Type: "object"}}
	values := map[string]Value{"payload": {Type: "object", Value: map[string]any{"present": nil}}}
	for _, tc := range []struct {
		name, property, operator string
		value                    any
		withValue                bool
		want                     bool
	}{
		{"null equals null", "present", "equals", nil, true, true},
		{"null differs from string", "present", "notEquals", "text", true, true},
		{"null is absent for exists", "present", "exists", nil, false, false},
		{"null matches notExists", "present", "notExists", nil, false, true},
		{"missing does not equal null", "missing", "equals", nil, true, false},
		{"missing does not compare unequal", "missing", "notEquals", nil, true, false},
		{"missing matches notExists", "missing", "notExists", nil, false, true},
		{"non-object path absent", "present.child", "notExists", nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rule := map[string]any{"input": "payload", "property": tc.property, "operator": tc.operator}
			if tc.withValue {
				rule["value"] = tc.value
			}
			group := map[string]any{"combinator": "all", "rules": []any{rule}}
			if err := validateBranchConditions(group, inputs); err != nil {
				t.Fatal(err)
			}
			if got := evaluateBranchConditions(group, values); got != tc.want {
				t.Fatalf("condition result = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLegacyBranchRoutesEqualJSON(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	workflow := branchRecipeWorkflow(map[string]any{"input": "payload", "equals": map[string]any{"ready": true}})
	if _, err := e.SaveWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		value map[string]any
		want  bool
	}{
		{map[string]any{"ready": true}, true},
		{map[string]any{"ready": false}, false},
	} {
		r := newRun(Project{ID: "project"}, Binding{}, workflow, nil, map[string]Value{"payload": {Type: "object", Value: tc.value}}, "")
		e.mu.Lock()
		e.runs[r.Run.ID] = r
		for range 3 {
			if _, err := e.pumpLocked(); err != nil {
				e.mu.Unlock()
				t.Fatal(err)
			}
		}
		got := e.runs[r.Run.ID].Run.Nodes["gate"]
		e.mu.Unlock()
		if got.Status != "succeeded" || got.Outputs["result"].Value != tc.want {
			t.Fatalf("legacy branch result: %+v", got)
		}
	}
}

func TestRecipeActionsWaitForSuccessAndSkipWhenConditionsAreFalse(t *testing.T) {
	e, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	workflow := Workflow{
		ID: "ordered-recipe", Name: "Ordered recipe",
		Inputs: map[string]string{"project": "string"},
		Nodes: []WorkflowNode{
			{ID: "given", Kind: "branch", Inputs: map[string]Input{"project": {Type: "string", From: "inputs.project"}}, Config: map[string]any{"conditions": map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "project", "operator": "equals", "value": "Trellis"}}}}},
			{ID: "first", Kind: "parallel"},
			{ID: "after-success", Kind: "approval", Config: map[string]any{"message": "Review results"}},
		},
		Edges: []Edge{
			{ID: "then", Source: "given", Target: "first", When: "true"},
			{ID: "next", Source: "first", Target: "after-success"},
		},
	}
	if _, err := e.SaveWorkflow(workflow); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, project, first, after string
	}{
		{"matched recipe", "Trellis", "succeeded", "waiting"},
		{"unmatched recipe", "Other", "skipped", "skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRun(Project{ID: "project"}, Binding{}, workflow, nil, map[string]Value{"project": {Type: "string", Value: tc.project}}, "")
			e.mu.Lock()
			e.runs[r.Run.ID] = r
			for range 3 {
				if _, err := e.pumpLocked(); err != nil {
					e.mu.Unlock()
					t.Fatal(err)
				}
			}
			got := e.runs[r.Run.ID].Run.Nodes
			e.mu.Unlock()
			if got["given"].Status != "succeeded" || got["first"].Status != tc.first || got["after-success"].Status != tc.after {
				t.Fatalf("unexpected ordered recipe states: %+v", got)
			}
		})
	}
}
