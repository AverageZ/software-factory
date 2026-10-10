package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func completionCondition() map[string]any {
	return map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "result", "property": "data.status", "operator": "equals", "value": "Complete"}}}
}

func TestAgentCompletionLoopBoundaries(t *testing.T) {
	for _, test := range []struct {
		name, body, wantError, calls string
		limit                        float64
	}{
		{"complete with feedback", `case "$FACTORY_INPUTS" in *previousResult*) printf '{"status":"Complete"}';; *) printf '{"status":"Incomplete"}';; esac`, "", "xx", 3},
		{"cutoff", `printf '{"status":"Incomplete"}'`, "not met after 2 iterations", "xx", 2},
		{"process failure is not retried", `printf '{"status":"Incomplete"}'; exit 7`, "exit status 7", "x", 3},
		{"invalid output is not retried", `printf 'not JSON'`, "complete JSON object", "x", 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binary := filepath.Join(root, "agent")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf x >> calls\n"+test.body+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			e := &Engine{data: t.TempDir()}
			r := savedRun{Run: Run{ID: "run"}, Project: Project{Harness: Harness{Binary: binary, Model: "default"}, Repositories: []Repository{{ID: "repo", Path: root}}}, Binding: Binding{Repositories: map[string]string{"source": "repo"}}}
			n := WorkflowNode{ID: "agent", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "Implement", "outputJson": true, "until": completionCondition(), "maxIterations": test.limit}}
			inputs := map[string]Value{"WorkItem": {Type: "object", Value: map[string]any{"kind": "Issue"}}}
			result := e.perform(context.Background(), r, n, inputs, 1)
			if test.wantError == "" {
				if result.err != nil {
					t.Fatal(result.err)
				}
				if !evaluateBranchConditions(completionCondition(), result.outputs) {
					t.Fatal("incomplete result reported success")
				}
			} else if result.err == nil || !strings.Contains(result.err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %s", result.err, test.wantError)
			}
			calls, err := os.ReadFile(filepath.Join(root, "calls"))
			if err != nil || string(calls) != test.calls {
				t.Fatalf("process calls = %q, %v, want %q", calls, err, test.calls)
			}
			if _, mutated := inputs["previousResult"]; mutated {
				t.Fatal("execution mutated snapshotted inputs")
			}
		})
	}
}

func TestAgentCompletionCollectsOnlyFinalArtifacts(t *testing.T) {
	// Execution receives canonical repository roots from project validation.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "agent")
	script := `#!/bin/sh
case "$FACTORY_INPUTS" in
  *previousResult*) printf final > evidence; printf '{"status":"Complete"}';;
  *) printf interim > evidence; printf '{"status":"Incomplete"}';;
esac
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	e := &Engine{data: t.TempDir()}
	r := savedRun{Run: Run{ID: "run"}, Project: Project{Harness: Harness{Binary: binary, Model: "default"}, Repositories: []Repository{{ID: "repo", Path: root}}}, Binding: Binding{Repositories: map[string]string{"source": "repo"}}}
	n := WorkflowNode{ID: "agent", Kind: "agent", Repository: "source", Config: map[string]any{"prompt": "Implement", "outputJson": true, "until": completionCondition(), "artifacts": []any{"evidence"}}}
	result := e.perform(context.Background(), r, n, nil, 1)
	if result.err != nil {
		t.Fatal(result.err)
	}
	data, err := os.ReadFile(filepath.Join(e.data, "runs", "run", "agent", "artifacts", "attempt-1-evidence"))
	if err != nil || string(data) != "final" {
		t.Fatalf("artifact = %q, %v", data, err)
	}
}

func TestMembershipConditionsFailClosed(t *testing.T) {
	for _, test := range []struct {
		value    any
		operator string
		expected bool
	}{
		{[]any{"Bug", "agent:run"}, "contains", true},
		{[]any{"Bugfix"}, "contains", false},
		{[]any{"Bugfix"}, "notContains", true},
		{nil, "notContains", false},
		{false, "notContains", false},
	} {
		condition := map[string]any{"combinator": "all", "rules": []any{map[string]any{"input": "item", "property": "labels", "operator": test.operator, "value": "Bug"}}}
		inputs := map[string]Value{"item": {Type: "object", Value: map[string]any{"labels": test.value}}}
		if got := evaluateBranchConditions(condition, inputs); got != test.expected {
			t.Fatalf("%v %s Bug = %v", test.value, test.operator, got)
		}
	}
}
