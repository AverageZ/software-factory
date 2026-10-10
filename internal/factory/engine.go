package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/google/uuid"
)

type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string   { return e.Message }
func invalid(err error) error       { return &APIError{400, err.Error()} }
func missing(what string) error     { return &APIError{404, what + " not found"} }
func conflict(message string) error { return &APIError{409, message} }

type activeExecution struct {
	cancel    context.CancelFunc
	projectID string
	attempt   int
}

// Engine serializes state transitions and commits them before publishing or executing.
// Executions never mutate state directly; their completion is checked against the
// persisted attempt and status under the same mutex as API controls.
type Engine struct {
	mu          sync.Mutex
	store       *store
	data        string
	projects    map[string]Project
	workflows   map[string]Workflow
	bindings    map[string]Binding
	layouts     map[string]Layout
	runs        map[string]savedRun
	active      map[string]activeExecution
	github      map[string]GitHubHealth
	githubBusy  map[string]bool
	intakes     map[string]intakeConfig
	issueJobs   map[string]savedIssueJob
	reviewJobs  map[string]savedReviewJob
	intakeBusy  map[string]bool
	issueActive map[string]bool
	intakeWake  chan struct{}
	ctx         context.Context
	stop        context.CancelFunc
	wake        chan struct{}
	errors      chan error
	fatal       error
	wg          sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
}

func Open(data string) (*Engine, error) {
	if strings.HasPrefix(data, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		data = filepath.Join(home, data[2:])
	}
	data, err := filepath.Abs(data)
	if err != nil {
		return nil, err
	}
	s, err := openStore(data)
	if err != nil {
		return nil, err
	}
	e := &Engine{store: s, data: data, active: map[string]activeExecution{}, wake: make(chan struct{}, 1), errors: make(chan error, 1)}
	fail := func(err error) (*Engine, error) { return nil, errors.Join(err, s.close()) }
	if e.projects, err = loadRecords[Project](s, "project"); err != nil {
		return fail(err)
	}
	if e.workflows, err = loadRecords[Workflow](s, "workflow"); err != nil {
		return fail(err)
	}
	for id, workflow := range e.workflows {
		compiled, err := compileDeclaration(workflow)
		if err != nil {
			return fail(fmt.Errorf("stored workflow %s: %w", id, err))
		}
		e.workflows[id] = compiled
	}
	if e.bindings, err = loadRecords[Binding](s, "binding"); err != nil {
		return fail(err)
	}
	if e.layouts, err = loadRecords[Layout](s, "layout"); err != nil {
		return fail(err)
	}
	if e.runs, err = loadRecords[savedRun](s, "run"); err != nil {
		return fail(err)
	}
	if e.github, err = loadRecords[GitHubHealth](s, "github"); err != nil {
		return fail(err)
	}
	e.githubBusy = make(map[string]bool)
	if e.intakes, err = loadRecords[intakeConfig](s, "intake"); err != nil {
		return fail(err)
	}
	if e.issueJobs, err = loadRecords[savedIssueJob](s, "issue-job"); err != nil {
		return fail(err)
	}
	if e.reviewJobs, err = loadRecords[savedReviewJob](s, "review-job"); err != nil {
		return fail(err)
	}
	for _, config := range e.intakes {
		if config.Enabled && config.ReviewBindingID == "" && !config.ReviewDisabled {
			if _, err := e.ensureDefaultReviewBindingLocked(config); err != nil {
				return fail(err)
			}
		}
	}
	e.intakeBusy = make(map[string]bool)
	e.issueActive = make(map[string]bool)
	e.intakeWake = make(chan struct{}, 1)
	e.ctx, e.stop = context.WithCancel(context.Background())
	for id, r := range e.runs {
		changed := false
		interrupted := false
		for _, n := range r.Workflow.Nodes {
			x := r.Run.Nodes[n.ID]
			if x.Status == "running" {
				if n.Kind == "workflow" && x.ChildRunID != "" {
					x.Status = "waiting"
				} else {
					x.Status = "interrupted"
					x.Error = "Daemon stopped during execution; side effects may have occurred. Explicit retry is required."
					x.FinishedAt = now()
					interrupted = true
				}
				r.Run.Nodes[n.ID] = x
				addEvent(&r.Run, n.ID, x.Status, x.Error)
				changed = true
			}
			if x.Status == "interrupted" {
				interrupted = true
			}
		}
		if interrupted && r.Run.Status != "cancelled" {
			r.Run.Status = "interrupted"
			r.Run.Error = "Execution was interrupted; explicit retry is required."
			changed = true
		}
		if changed {
			if err := s.save(record{"run", id, r}); err != nil {
				e.stop()
				return fail(err)
			}
			e.runs[id] = r
		}
	}
	for id, job := range e.issueJobs {
		if job.Status == "working" {
			job.Status = "interrupted"
			job.Error = "Daemon stopped during local execution; workspace retained. Inspect local work and any surviving processes before recovery."
			job.UpdatedAt = now()
			if err := s.save(record{"issue-job", id, job}); err != nil {
				e.stop()
				return fail(err)
			}
			e.issueJobs[id] = job
		}
	}
	for id, job := range e.reviewJobs {
		if job.Status == "queued" || job.Status == "working" || job.Status == "publishing" {
			job.Status = "interrupted"
			job.Error = "Daemon stopped during revision admission, execution, or publication; inspect the PR head and retained worktree before recovery."
			job.UpdatedAt = now()
			if err := s.save(record{"review-job", id, job}); err != nil {
				e.stop()
				return fail(err)
			}
			e.reviewJobs[id] = job
		}
	}
	e.wg.Add(1)
	go e.intakeLoop()
	e.wg.Add(1)
	go e.schedule()
	e.notify()
	return e, nil
}
func (e *Engine) Errors() <-chan error { return e.errors }
func (e *Engine) Close() error {
	e.closeOnce.Do(func() {
		e.mu.Lock()
		e.stop()
		e.mu.Unlock()
		e.wg.Wait()
		e.mu.Lock()
		defer e.mu.Unlock()
		e.closeErr = errors.Join(e.fatal, e.store.close())
	})
	return e.closeErr
}
func (e *Engine) notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}
func (e *Engine) healthyLocked() error {
	if e.fatal != nil {
		return fmt.Errorf("persistence failed: %w", e.fatal)
	}
	if e.ctx.Err() != nil {
		return &APIError{503, "daemon is stopping"}
	}
	return nil
}
func (e *Engine) fatalLocked(err error) {
	if e.fatal != nil {
		return
	}
	e.fatal = err
	e.errors <- err
	e.stop()
}
func (e *Engine) commitRunsLocked(runs ...savedRun) error {
	records := make([]record, 0, len(runs))
	for _, r := range runs {
		records = append(records, record{"run", r.Run.ID, r})
	}
	if err := e.store.save(records...); err != nil {
		return err
	}
	for _, r := range runs {
		e.runs[r.Run.ID] = r
	}
	return nil
}
func valuesSorted[T any](items map[string]T) []T {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]T, 0, len(items))
	for _, key := range keys {
		values = append(values, copyJSON(items[key]))
	}
	return values
}
func (e *Engine) Projects() []Project {
	e.mu.Lock()
	defer e.mu.Unlock()
	return valuesSorted(e.projects)
}
func (e *Engine) Workflows() []Workflow {
	e.mu.Lock()
	defer e.mu.Unlock()
	return valuesSorted(e.workflows)
}
func (e *Engine) Bindings() []Binding {
	e.mu.Lock()
	defer e.mu.Unlock()
	return valuesSorted(e.bindings)
}
func (e *Engine) SaveProject(p Project) (Project, error) {
	p = copyJSON(p)
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	for i := range p.Repositories {
		if p.Repositories[i].ID == "" {
			p.Repositories[i].ID = uuid.NewString()
		}
	}
	if err := validateProject(&p); err != nil {
		return p, invalid(err)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return p, err
	}
	if err := e.checkGitHubProjectUpdateLocked(p); err != nil {
		return p, err
	}
	if err := e.checkIntakeProjectUpdateLocked(p); err != nil {
		return p, err
	}
	for _, b := range e.bindings {
		if b.ProjectID == p.ID {
			if err := validateBinding(b, p, e.workflows, false); err != nil {
				return p, invalid(fmt.Errorf("existing binding %s: %w", b.ID, err))
			}
		}
	}
	if err := e.store.save(record{"project", p.ID, p}); err != nil {
		return p, err
	}
	e.projects[p.ID] = p
	return copyJSON(p), nil
}
func (e *Engine) SaveWorkflow(w Workflow) (Workflow, error) {
	w = copyJSON(w)
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	compiled, err := compileDeclaration(w)
	if err != nil {
		return w, invalid(err)
	}
	w = compiled
	if w.Edges == nil {
		w.Edges = []Edge{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return w, err
	}
	definitions := make(map[string]Workflow, len(e.workflows)+1)
	for id, item := range e.workflows {
		definitions[id] = item
	}
	definitions[w.ID] = w
	if err := validateDefinitions(definitions); err != nil {
		return w, invalid(err)
	}
	for _, b := range e.bindings {
		if err := validateBinding(b, e.projects[b.ProjectID], definitions, false); err != nil {
			return w, invalid(fmt.Errorf("existing binding %s: %w", b.ID, err))
		}
	}
	if err := e.store.save(record{"workflow", w.ID, w}); err != nil {
		return w, err
	}
	e.workflows[w.ID] = w
	return copyJSON(w), nil
}
func (e *Engine) SaveBinding(b Binding) (Binding, error) {
	b = copyJSON(b)
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.Repositories == nil {
		b.Repositories = map[string]string{}
	}
	if b.Inputs == nil {
		b.Inputs = map[string]Value{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return b, err
	}
	p, ok := e.projects[b.ProjectID]
	if !ok {
		return b, missing("project")
	}
	if err := validateBinding(b, p, e.workflows, false); err != nil {
		return b, invalid(err)
	}
	if err := e.store.save(record{"binding", b.ID, b}); err != nil {
		return b, err
	}
	e.bindings[b.ID] = b
	return copyJSON(b), nil
}
func (e *Engine) Layout(id string) (Layout, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.workflows[id]; !ok {
		return Layout{}, missing("workflow")
	}
	layout, ok := e.layouts[id]
	if !ok {
		layout = Layout{Nodes: map[string]Position{}}
	}
	return copyJSON(layout), nil
}
func (e *Engine) SaveLayout(id string, layout Layout) (Layout, error) {
	layout = copyJSON(layout)
	if layout.Nodes == nil {
		layout.Nodes = map[string]Position{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return layout, err
	}
	w, ok := e.workflows[id]
	if !ok {
		return layout, missing("workflow")
	}
	nodes := map[string]bool{}
	for _, n := range w.Nodes {
		nodes[n.ID] = true
	}
	for id := range layout.Nodes {
		if !nodes[id] {
			return layout, invalid(fmt.Errorf("layout references unknown node %s", id))
		}
	}
	if layout.Viewport != nil && layout.Viewport.Zoom <= 0 {
		return layout, invalid(fmt.Errorf("viewport zoom must be positive"))
	}
	if err := e.store.save(record{"layout", id, layout}); err != nil {
		return layout, err
	}
	e.layouts[id] = layout
	return copyJSON(layout), nil
}
func newRun(p Project, b Binding, w Workflow, definitions map[string]Workflow, inputs map[string]Value, parent string) savedRun {
	stamp := now()
	r := Run{ID: uuid.NewString(), ProjectID: p.ID, BindingID: b.ID, WorkflowID: w.ID, Status: "queued", CreatedAt: stamp, UpdatedAt: stamp, Inputs: inputs, Nodes: map[string]NodeExecution{}, Events: []Event{}, ParentRunID: parent}
	for _, n := range w.Nodes {
		r.Nodes[n.ID] = NodeExecution{NodeID: n.ID, Status: "pending", Outputs: map[string]Value{}, Artifacts: []string{}}
	}
	addEvent(&r, "", "queued", "Run queued with immutable project, binding and workflow snapshots.")
	return savedRun{Run: r, Project: p, Binding: b, Workflow: w, Definitions: definitions}
}
func (e *Engine) Start(bindingID string, inputs map[string]Value) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return Run{}, err
	}
	b, ok := e.bindings[bindingID]
	if !ok {
		return Run{}, missing("binding")
	}
	b = copyJSON(b)
	p := copyJSON(e.projects[b.ProjectID])
	definitions := copyJSON(e.workflows)
	if err := validateDefinitions(definitions); err != nil {
		return Run{}, invalid(err)
	}
	for name, value := range inputs {
		b.Inputs[name] = copyJSON(value)
	}
	if err := validateBinding(b, p, definitions, true); err != nil {
		return Run{}, invalid(err)
	}
	// Revalidate the local roots at execution admission, not only project creation.
	if err := validateProject(&p); err != nil {
		return Run{}, invalid(err)
	}
	r := newRun(p, b, definitions[b.WorkflowID], definitions, copyJSON(b.Inputs), "")
	if err := e.commitRunsLocked(r); err != nil {
		return Run{}, err
	}
	e.notify()
	return copyJSON(r.Run), nil
}
func (e *Engine) Runs(projectID string) []Run {
	e.mu.Lock()
	defer e.mu.Unlock()
	runs := []Run{}
	for _, r := range e.runs {
		if projectID == "" || r.Run.ProjectID == projectID {
			runs = append(runs, copyJSON(r.Run))
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].CreatedAt > runs[j].CreatedAt })
	return runs
}
func (e *Engine) Run(id string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runs[id]
	if !ok {
		return Run{}, missing("run")
	}
	return copyJSON(r.Run), nil
}
func workflowNode(w Workflow, id string) (WorkflowNode, bool) {
	for _, n := range w.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return WorkflowNode{}, false
}
func (e *Engine) Approve(id, nodeID string, approved bool) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return Run{}, err
	}
	current, ok := e.runs[id]
	if !ok {
		return Run{}, missing("run")
	}
	r := copyJSON(current)
	n, ok := workflowNode(r.Workflow, nodeID)
	if !ok {
		return Run{}, missing("node")
	}
	x := r.Run.Nodes[nodeID]
	if n.Kind != "approval" || x.Status != "waiting" || terminal(r.Run.Status) {
		return Run{}, conflict("node is not awaiting approval")
	}
	x.Status = "succeeded"
	message := "Approved explicitly by user."
	if !approved {
		x.Status = "failed"
		x.Error = "Approval rejected by user."
		message = x.Error
	}
	x.FinishedAt = now()
	x.Outputs = map[string]Value{"result": {Type: "Approval", Value: map[string]any{"approved": approved}}}
	r.Run.Nodes[nodeID] = x
	r.Run.Status = "running"
	addEvent(&r.Run, nodeID, x.Status, message)
	if err := e.commitRunsLocked(r); err != nil {
		return Run{}, err
	}
	e.notify()
	return copyJSON(r.Run), nil
}
func retryable(status string) bool {
	return status == "failed" || status == "interrupted" || status == "cancelled" || status == "unavailable"
}
func resetExecution(x NodeExecution) NodeExecution {
	x.Status = "pending"
	x.StartedAt = ""
	x.FinishedAt = ""
	x.Outputs = map[string]Value{}
	x.Error = ""
	return x
}
func resetDescendants(r *savedRun, nodeID string) {
	selected := map[string]bool{nodeID: true}
	changed := true
	for changed {
		changed = false
		for _, edge := range r.Workflow.Edges {
			if selected[edge.Source] && !selected[edge.Target] {
				selected[edge.Target] = true
				changed = true
			}
		}
	}
	for id := range selected {
		x := r.Run.Nodes[id]
		if x.Status != "succeeded" && x.Status != "running" && x.Status != "waiting" {
			r.Run.Nodes[id] = resetExecution(x)
		}
	}
	r.Run.Status = "queued"
	r.Run.Error = ""
	addEvent(&r.Run, nodeID, "retry", "Explicit retry requested; completed nodes are retained and side effects may repeat.")
}
func (e *Engine) Retry(id, nodeID string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return Run{}, err
	}
	current, ok := e.runs[id]
	if !ok {
		return Run{}, missing("run")
	}
	if current.IssueJobID != "" {
		return Run{}, conflict("managed issue runs cannot be replayed through node retry; inspect the retained worktree and intake report")
	}
	r := copyJSON(current)
	x, ok := r.Run.Nodes[nodeID]
	if !ok {
		return Run{}, missing("node")
	}
	if !retryable(x.Status) {
		return Run{}, conflict("only failed, interrupted, unavailable or cancelled nodes can be retried")
	}
	if _, active := e.active[id+"/"+nodeID]; active {
		return Run{}, conflict("previous execution is still stopping")
	}
	updates := []savedRun{}
	var resumeChild func(string) error
	resumeChild = func(childID string) error {
		original, ok := e.runs[childID]
		if !ok {
			return fmt.Errorf("child run %s is missing", childID)
		}
		child := copyJSON(original)
		for _, n := range child.Workflow.Nodes {
			execution := child.Run.Nodes[n.ID]
			if retryable(execution.Status) {
				if _, active := e.active[childID+"/"+n.ID]; active {
					return conflict("child execution is still stopping")
				}
				if execution.ChildRunID != "" {
					if err := resumeChild(execution.ChildRunID); err != nil {
						return err
					}
				}
				resetDescendants(&child, n.ID)
			}
		}
		if child.Run.Status == "succeeded" {
			return nil
		}
		child.Run.Status = "queued"
		child.Run.Error = ""
		updates = append(updates, child)
		return nil
	}
	if x.ChildRunID != "" {
		if err := resumeChild(x.ChildRunID); err != nil {
			return Run{}, err
		}
	}
	resetDescendants(&r, nodeID)
	updates = append(updates, r)
	// A direct retry inside a child also reconnects its failed ancestor workflow nodes.
	parentID := r.Run.ParentRunID
	for parentID != "" {
		parent := copyJSON(e.runs[parentID])
		found := false
		for _, n := range parent.Workflow.Nodes {
			execution := parent.Run.Nodes[n.ID]
			if execution.ChildRunID == r.Run.ID && retryable(execution.Status) {
				resetDescendants(&parent, n.ID)
				found = true
				break
			}
		}
		if !found {
			break
		}
		updates = append(updates, parent)
		r = parent
		parentID = parent.Run.ParentRunID
	}
	if err := e.commitRunsLocked(updates...); err != nil {
		return Run{}, err
	}
	e.notify()
	return copyJSON(e.runs[id].Run), nil
}
func (e *Engine) Cancel(id string) (Run, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.healthyLocked(); err != nil {
		return Run{}, err
	}
	root, ok := e.runs[id]
	if !ok {
		return Run{}, missing("run")
	}
	if root.Run.Status == "succeeded" || root.Run.Status == "cancelled" {
		return Run{}, conflict("run is already complete")
	}
	updates := []savedRun{}
	keys := []string{}
	var cancelRun func(string)
	cancelRun = func(id string) {
		r := copyJSON(e.runs[id])
		if r.Run.Status == "succeeded" || r.Run.Status == "cancelled" {
			return
		}
		if r.IssueJobID != "" {
			keys = append(keys, "issue/"+r.IssueJobID)
			for _, job := range e.reviewJobs {
				if job.RunID == id {
					keys = append(keys, "review/"+job.ID)
				}
			}
		}
		for nodeID, x := range r.Run.Nodes {
			if x.ChildRunID != "" {
				cancelRun(x.ChildRunID)
			}
			if !terminal(x.Status) {
				x.Status = "cancelled"
				x.Error = "Cancelled by user; running side effects may already have occurred."
				x.FinishedAt = now()
				r.Run.Nodes[nodeID] = x
				addEvent(&r.Run, nodeID, "cancelled", x.Error)
			}
			keys = append(keys, id+"/"+nodeID)
		}
		r.Run.Status = "cancelled"
		r.Run.Error = "Cancelled by user."
		addEvent(&r.Run, "", "cancelled", r.Run.Error)
		updates = append(updates, r)
	}
	cancelRun(id)
	if err := e.commitRunsLocked(updates...); err != nil {
		return Run{}, err
	}
	for _, key := range keys {
		if active, ok := e.active[key]; ok {
			active.cancel()
		}
	}
	e.notify()
	return copyJSON(e.runs[id].Run), nil
}
