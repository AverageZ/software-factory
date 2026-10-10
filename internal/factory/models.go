package factory

import (
	"encoding/json"
	"fmt"
	"time"
)

type Repository struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}
type Harness struct {
	Binary string `json:"binary"`
	Model  string `json:"model"`
}
type Policy struct {
	MaxParallel int `json:"maxParallel"`
}
type Project struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Repositories []Repository `json:"repositories"`
	Harness      Harness      `json:"harness"`
	Policy       Policy       `json:"policy"`
}
type Value struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}
type Input struct {
	Type    string `json:"type"`
	Value   any    `json:"value,omitempty"`
	From    string `json:"from,omitempty"`
	literal bool
}

func (i *Input) UnmarshalJSON(data []byte) error {
	type plain Input
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*i = Input(p)
	_, i.literal = fields["value"]
	return nil
}
func (i Input) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": i.Type}
	if i.From != "" {
		m["from"] = i.From
	}
	if i.literal || i.Value != nil {
		m["value"] = i.Value
	}
	return json.Marshal(m)
}

type Binding struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	ProjectID    string            `json:"projectId"`
	WorkflowID   string            `json:"workflowId"`
	Repositories map[string]string `json:"repositories"`
	Inputs       map[string]Value  `json:"inputs"`
}
type WorkflowNode struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Kind       string           `json:"kind"`
	Repository string           `json:"repository,omitempty"`
	Inputs     map[string]Input `json:"inputs,omitempty"`
	OutputType string           `json:"outputType,omitempty"`
	Config     map[string]any   `json:"config"`
}
type Edge struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	When   string `json:"when,omitempty"`
}
type Workflow struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Nodes       []WorkflowNode    `json:"nodes"`
	Edges       []Edge            `json:"edges"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	Declaration *Declaration      `json:"declaration,omitempty"`
}
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}
type Viewport struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Zoom float64 `json:"zoom"`
}
type Layout struct {
	Nodes    map[string]Position `json:"nodes"`
	Viewport *Viewport           `json:"viewport,omitempty"`
}
type NodeExecution struct {
	NodeID     string           `json:"nodeId"`
	Status     string           `json:"status"`
	Attempt    int              `json:"attempt"`
	StartedAt  string           `json:"startedAt,omitempty"`
	FinishedAt string           `json:"finishedAt,omitempty"`
	Outputs    map[string]Value `json:"outputs"`
	Error      string           `json:"error,omitempty"`
	LogPath    string           `json:"logPath,omitempty"`
	Artifacts  []string         `json:"artifacts"`
	ChildRunID string           `json:"childRunId,omitempty"`
}
type Event struct {
	Time    string `json:"time"`
	NodeID  string `json:"nodeId,omitempty"`
	Type    string `json:"type"`
	Message string `json:"message"`
}
type Run struct {
	ID          string                   `json:"id"`
	ProjectID   string                   `json:"projectId"`
	BindingID   string                   `json:"bindingId"`
	WorkflowID  string                   `json:"workflowId"`
	Status      string                   `json:"status"`
	CreatedAt   string                   `json:"createdAt"`
	UpdatedAt   string                   `json:"updatedAt"`
	Inputs      map[string]Value         `json:"inputs"`
	Nodes       map[string]NodeExecution `json:"nodes"`
	Events      []Event                  `json:"events"`
	Error       string                   `json:"error,omitempty"`
	ParentRunID string                   `json:"parentRunId,omitempty"`
}

// All execution settings, including nested definitions, are immutable snapshots.
type savedRun struct {
	Run         Run                 `json:"run"`
	Project     Project             `json:"project"`
	Binding     Binding             `json:"binding"`
	Workflow    Workflow            `json:"workflow"`
	Definitions map[string]Workflow `json:"definitions"`
	IssueJobID  string              `json:"issueJobId,omitempty"`
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
func addEvent(r *Run, node, kind, message string) {
	r.UpdatedAt = now()
	r.Events = append(r.Events, Event{Time: r.UpdatedAt, NodeID: node, Type: kind, Message: message})
}
func copyJSON[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("invalid internal JSON: %v", err))
	}
	var copy T
	if err := json.Unmarshal(data, &copy); err != nil {
		panic(err)
	}
	return copy
}
func terminal(status string) bool {
	switch status {
	case "succeeded", "failed", "skipped", "interrupted", "cancelled", "unavailable":
		return true
	}
	return false
}
func nodeOutputType(n WorkflowNode) string {
	switch n.Kind {
	case "approval":
		return "Approval"
	case "branch":
		return "boolean"
	}
	if n.OutputType != "" {
		return n.OutputType
	}
	return "object"
}
