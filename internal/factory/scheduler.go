package factory

import (
	"context"
	"fmt"
	"sort"
)

func (e *Engine) schedule() {
	defer e.wg.Done()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-e.wake:
		}
		for {
			e.mu.Lock()
			if e.ctx.Err() != nil {
				e.mu.Unlock()
				return
			}
			changed, err := e.pumpLocked()
			if err != nil {
				e.fatalLocked(err)
			}
			e.mu.Unlock()
			if err != nil || !changed {
				break
			}
		}
	}
}
func (e *Engine) pumpLocked() (bool, error) {
	ids := make([]string, 0, len(e.runs))
	for id := range e.runs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return e.runs[ids[i]].Run.CreatedAt < e.runs[ids[j]].Run.CreatedAt })
	for _, id := range ids {
		original := e.runs[id]
		if original.IssueJobID != "" || terminal(original.Run.Status) {
			continue
		}
		r := copyJSON(original)
		for _, n := range r.Workflow.Nodes {
			x := r.Run.Nodes[n.ID]
			if x.Status == "waiting" && n.Kind == "workflow" {
				child, ok := e.runs[x.ChildRunID]
				if !ok {
					x.Status = "failed"
					x.Error = "Recorded child run is missing."
				} else if terminal(child.Run.Status) {
					x.Status = child.Run.Status
					if x.Status != "succeeded" {
						x.Error = "Child run " + child.Run.ID + ": " + child.Run.Status
						if child.Run.Error != "" {
							x.Error += " — " + child.Run.Error
						}
					}
					x.Outputs = map[string]Value{"result": {Type: nodeOutputType(n), Value: map[string]any{"runId": child.Run.ID, "status": child.Run.Status}}}
				} else {
					continue
				}
				x.FinishedAt = now()
				r.Run.Nodes[n.ID] = x
				addEvent(&r.Run, n.ID, x.Status, "Child workflow "+x.ChildRunID+" finished: "+x.Status)
				deriveRunStatus(&r)
				return true, e.commitRunsLocked(r)
			}
			if x.Status != "pending" {
				continue
			}
			ready, selected, dependencyError, dependencyStatus := dependencies(r, n.ID)
			if !ready {
				continue
			}
			if dependencyError != "" || !selected {
				x.Status = "skipped"
				message := "Unselected branch path."
				if dependencyError != "" {
					x.Status = dependencyStatus
					x.Error = dependencyError
					message = dependencyError
				}
				x.FinishedAt = now()
				r.Run.Nodes[n.ID] = x
				addEvent(&r.Run, n.ID, x.Status, message)
				deriveRunStatus(&r)
				return true, e.commitRunsLocked(r)
			}
			inputs, err := resolveInputs(&r, n)
			if err != nil {
				x.Status = "failed"
				x.Error = err.Error()
				x.FinishedAt = now()
				r.Run.Nodes[n.ID] = x
				addEvent(&r.Run, n.ID, "failed", x.Error)
				deriveRunStatus(&r)
				return true, e.commitRunsLocked(r)
			}
			external := n.Kind == "agent" || n.Kind == "command" || n.Kind == "tool" || n.Kind == "validation" || n.Kind == "integration"
			if external {
				count := 0
				for _, active := range e.active {
					if active.projectID == r.Run.ProjectID {
						count++
					}
				}
				if count >= r.Project.Policy.MaxParallel {
					continue
				}
			}
			x.Attempt++
			x.StartedAt = now()
			x.FinishedAt = ""
			x.Error = ""
			x.Status = "running"
			if external {
				x.LogPath = logRelative(r.Run.ID, n.ID, x.Attempt)
				r.Run.Nodes[n.ID] = x
				r.Run.Status = "running"
				addEvent(&r.Run, n.ID, "running", fmt.Sprintf("Started %s attempt %d.", n.Kind, x.Attempt))
				if err := e.commitRunsLocked(r); err != nil {
					return false, err
				}
				ctx, cancel := context.WithCancel(e.ctx)
				key := id + "/" + n.ID
				e.active[key] = activeExecution{cancel: cancel, projectID: r.Run.ProjectID, attempt: x.Attempt}
				e.wg.Add(1)
				go e.execute(ctx, r, n, inputs, x.Attempt)
				return true, nil
			}
			switch n.Kind {
			case "approval":
				x.Status = "waiting"
				addEvent(&r.Run, n.ID, "waiting", configString(n, "message"))
			case "decision":
				x.Status = "unavailable"
				x.Error = "Decision nodes are unavailable: no decision provider is configured. No branch was selected."
				addEvent(&r.Run, n.ID, "unavailable", x.Error)
			case "branch":
				var selected bool
				if conditions, exists := n.Config["conditions"]; exists {
					_, hasInput := n.Config["input"]
					_, hasEquals := n.Config["equals"]
					if hasInput || hasEquals {
						x.Status = "failed"
						x.Error = "Invalid branch configuration: conditions cannot be mixed with input/equals."
						addEvent(&r.Run, n.ID, "failed", x.Error)
						break
					}
					if err := validateBranchConditions(conditions, n.Inputs); err != nil {
						x.Status = "failed"
						x.Error = fmt.Sprintf("Invalid branch conditions: %v", err)
						addEvent(&r.Run, n.ID, "failed", x.Error)
						break
					}
					selected = evaluateBranchConditions(conditions.(map[string]any), inputs)
				} else {
					input := configString(n, "input")
					value, ok := inputs[input]
					_, hasEquals := n.Config["equals"]
					if !ok || !hasEquals {
						x.Status = "failed"
						x.Error = "Invalid legacy branch configuration."
						addEvent(&r.Run, n.ID, "failed", x.Error)
						break
					}
					selected = equalJSON(value.Value, n.Config["equals"])
				}
				x.Status = "succeeded"
				x.Outputs = map[string]Value{"result": {Type: "boolean", Value: selected}}
				addEvent(&r.Run, n.ID, "succeeded", fmt.Sprintf("Branch selected %t.", selected))
			case "parallel":
				predecessors := []string{}
				seen := map[string]bool{}
				for _, edge := range r.Workflow.Edges {
					if edge.Target == n.ID && r.Run.Nodes[edge.Source].Status == "succeeded" && edgeSelected(r, edge) && !seen[edge.Source] {
						predecessors = append(predecessors, edge.Source)
						seen[edge.Source] = true
					}
				}
				sort.Strings(predecessors)
				x.Status = "succeeded"
				x.Outputs = map[string]Value{"result": {Type: nodeOutputType(n), Value: map[string]any{"predecessors": predecessors}}}
				addEvent(&r.Run, n.ID, "succeeded", "Parallel barrier completed.")
			case "workflow":
				var child savedRun
				if x.ChildRunID != "" {
					var ok bool
					child, ok = e.runs[x.ChildRunID]
					if !ok {
						return false, fmt.Errorf("child run %s is missing", x.ChildRunID)
					}
				} else {
					definition := r.Definitions[configString(n, "workflowId")]
					if err := validateInputs(definition, inputs, true); err != nil {
						return false, err
					}
					child = newRun(r.Project, r.Binding, definition, r.Definitions, inputs, r.Run.ID)
					x.ChildRunID = child.Run.ID
				}
				x.Status = "waiting"
				r.Run.Nodes[n.ID] = x
				addEvent(&r.Run, n.ID, "waiting", "Waiting for child workflow "+child.Run.ID+".")
				deriveRunStatus(&r)
				return true, e.commitRunsLocked(child, r)
			default:
				return false, fmt.Errorf("unknown snapshotted node kind %s", n.Kind)
			}
			if terminal(x.Status) {
				x.FinishedAt = now()
			}
			r.Run.Nodes[n.ID] = x
			deriveRunStatus(&r)
			return true, e.commitRunsLocked(r)
		}
		deriveRunStatus(&r)
		if r.Run.Status != original.Run.Status || r.Run.Error != original.Run.Error {
			return true, e.commitRunsLocked(r)
		}
	}
	return false, nil
}
func edgeSelected(r savedRun, edge Edge) bool {
	if edge.When == "" {
		return true
	}
	value, ok := r.Run.Nodes[edge.Source].Outputs["result"].Value.(bool)
	return ok && fmt.Sprint(value) == edge.When
}

// A merge waits for every predecessor. Skipped predecessors are inactive,
// rather than blockers; any selected predecessor can activate the merge.
func dependencies(r savedRun, nodeID string) (ready, selected bool, message, status string) {
	ready = true
	count := 0
	for _, edge := range r.Workflow.Edges {
		if edge.Target != nodeID {
			continue
		}
		count++
		x := r.Run.Nodes[edge.Source]
		if !terminal(x.Status) {
			ready = false
			continue
		}
		if x.Status == "skipped" {
			continue
		}
		if x.Status != "succeeded" {
			if message == "" {
				message = "Upstream node " + edge.Source + " ended " + x.Status + "."
				status = "failed"
				if x.Status == "interrupted" {
					status = "interrupted"
				}
			}
			continue
		}
		if edgeSelected(r, edge) {
			selected = true
		}
	}
	if count == 0 {
		selected = true
	}
	return
}
func deriveRunStatus(r *savedRun) {
	if r.Run.Status == "cancelled" {
		return
	}
	pending, running, waiting := false, false, false
	failure, interrupted := "", ""
	for _, n := range r.Workflow.Nodes {
		x := r.Run.Nodes[n.ID]
		switch x.Status {
		case "pending":
			pending = true
		case "running":
			running = true
		case "waiting":
			waiting = true
		case "interrupted":
			if interrupted == "" {
				interrupted = "Node " + n.ID + ": " + x.Error
			}
		case "failed", "unavailable", "cancelled":
			if failure == "" {
				failure = "Node " + n.ID + ": " + x.Error
			}
		}
	}
	status := "succeeded"
	message := ""
	switch {
	case running:
		status = "running"
	case interrupted != "":
		status = "interrupted"
		message = interrupted
	case pending:
		status = "running"
	case waiting:
		status = "waiting"
	case failure != "":
		status = "failed"
		message = failure
	}
	// A pending node behind an approval/child is waiting, not actively running.
	if !running && waiting && interrupted == "" {
		status = "waiting"
	}
	if status != r.Run.Status {
		addEvent(&r.Run, "", status, "Run "+status+".")
	}
	r.Run.Status = status
	r.Run.Error = message
}
func (e *Engine) execute(ctx context.Context, r savedRun, n WorkflowNode, inputs map[string]Value, attempt int) {
	defer e.wg.Done()
	result := e.perform(ctx, r, n, inputs, attempt)
	e.mu.Lock()
	defer e.mu.Unlock()
	key := r.Run.ID + "/" + n.ID
	active, ok := e.active[key]
	if ok && active.attempt == attempt {
		active.cancel()
		delete(e.active, key)
	}
	current := copyJSON(e.runs[r.Run.ID])
	x := current.Run.Nodes[n.ID]
	if x.Status != "running" || x.Attempt != attempt {
		e.notify()
		return
	}
	x.Status = "succeeded"
	x.FinishedAt = now()
	x.Outputs = result.outputs
	x.Artifacts = append(x.Artifacts, result.artifacts...)
	if result.err != nil {
		x.Status = "failed"
		x.Error = result.err.Error()
	}
	if e.ctx.Err() != nil {
		x.Status = "interrupted"
		x.Error = "Daemon stopped during execution; side effects may have occurred. Explicit retry is required."
	}
	current.Run.Nodes[n.ID] = x
	message := "Execution succeeded."
	if x.Error != "" {
		message = x.Error
	}
	addEvent(&current.Run, n.ID, x.Status, message)
	deriveRunStatus(&current)
	if err := e.commitRunsLocked(current); err != nil {
		e.fatalLocked(err)
	}
	e.notify()
}
