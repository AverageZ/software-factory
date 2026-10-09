package factory

import "testing"

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
