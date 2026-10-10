package factory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

const maxDeclarationSteps = 256
const maxDeclarationNodes = 1024
const maxDeclarationBytes = 1 << 20

// Declaration version 1 is authoritative; Nodes and Edges are its server-derived projection.
type Declaration struct {
	Version  int                     `json:"version"`
	Defaults *DeclarationDefaults    `json:"defaults,omitempty"`
	Actions  map[string]WorkflowNode `json:"actions"`
	Given    *DeclarationCondition   `json:"given,omitempty"`
	Steps    []DeclarationStep       `json:"steps"`
}

type DeclarationDefaults struct {
	Model          string   `json:"model,omitempty"`
	TimeoutSeconds *float64 `json:"timeoutSeconds,omitempty"`
	MaxIterations  *float64 `json:"maxIterations,omitempty"`
}

type DeclarationStep struct {
	ID     string                `json:"id"`
	Run    string                `json:"run"`
	As     string                `json:"as,omitempty"`
	If     *DeclarationCondition `json:"if,omitempty"`
	Inputs map[string]Input      `json:"inputs,omitempty"`
}

type DeclarationCondition struct {
	All      []DeclarationCondition `json:"all,omitempty"`
	Any      []DeclarationCondition `json:"any,omitempty"`
	Path     string                 `json:"path,omitempty"`
	Operator string                 `json:"operator,omitempty"`
	Value    any                    `json:"value,omitempty"`
	hasValue bool
}

func strictDeclarationJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

func (d Declaration) MarshalJSON() ([]byte, error) {
	type plain Declaration
	type actionJSON struct {
		WorkflowNode
		Inputs *map[string]Input `json:"inputs,omitempty"`
	}
	type stepJSON struct {
		DeclarationStep
		Inputs *map[string]Input `json:"inputs,omitempty"`
	}
	actions := make(map[string]actionJSON, len(d.Actions))
	for name, action := range d.Actions {
		item := actionJSON{WorkflowNode: action}
		if action.Inputs != nil {
			item.Inputs = &action.Inputs
		}
		actions[name] = item
	}
	steps := make([]stepJSON, len(d.Steps))
	for i, step := range d.Steps {
		steps[i] = stepJSON{DeclarationStep: step}
		if step.Inputs != nil {
			steps[i].Inputs = &step.Inputs
		}
	}
	// An explicit empty input map disables action/automatic input inheritance,
	// so unlike WorkflowNode's legacy encoding it must survive persistence.
	return json.Marshal(struct {
		plain
		Actions map[string]actionJSON `json:"actions"`
		Steps   []stepJSON            `json:"steps"`
	}{plain(d), actions, steps})
}

func (d *DeclarationDefaults) UnmarshalJSON(data []byte) error {
	type plain DeclarationDefaults
	var value plain
	if err := strictDeclarationJSON(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for name, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("declaration default %s cannot be null", name)
		}
	}
	*d = DeclarationDefaults(value)
	return nil
}

func (d *Declaration) UnmarshalJSON(data []byte) error {
	type plain Declaration
	var value plain
	if err := strictDeclarationJSON(data, &value); err != nil {
		return err
	}
	// Input has a custom decoder for explicit null literals. Check its field
	// names here too, because Decoder.DisallowUnknownFields cannot inspect it.
	var raw struct {
		Actions map[string]struct {
			Inputs map[string]json.RawMessage `json:"inputs"`
		} `json:"actions"`
		Steps []struct {
			Inputs map[string]json.RawMessage `json:"inputs"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	checkInputs := func(inputs map[string]json.RawMessage) error {
		for name, data := range inputs {
			var input struct {
				Type  string          `json:"type"`
				From  string          `json:"from"`
				Value json.RawMessage `json:"value"`
			}
			if err := strictDeclarationJSON(data, &input); err != nil {
				return fmt.Errorf("input %s: %w", name, err)
			}
		}
		return nil
	}
	for _, action := range raw.Actions {
		if err := checkInputs(action.Inputs); err != nil {
			return err
		}
	}
	for _, step := range raw.Steps {
		if err := checkInputs(step.Inputs); err != nil {
			return err
		}
	}
	*d = Declaration(value)
	return nil
}

func (c *DeclarationCondition) UnmarshalJSON(data []byte) error {
	type plain DeclarationCondition
	var value plain
	if err := strictDeclarationJSON(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	_, all := fields["all"]
	_, any := fields["any"]
	if all || any {
		if len(fields) != 1 || all && len(value.All) == 0 || any && len(value.Any) == 0 {
			return fmt.Errorf("condition group must contain only a nonempty all or any array")
		}
	} else if value.Path == "" || value.Operator == "" {
		return fmt.Errorf("condition requires path and operator")
	}
	*c = DeclarationCondition(value)
	_, c.hasValue = fields["value"]
	return nil
}

func (c DeclarationCondition) MarshalJSON() ([]byte, error) {
	fields := map[string]any{}
	if c.All != nil {
		fields["all"] = c.All
	}
	if c.Any != nil {
		fields["any"] = c.Any
	}
	if c.Path != "" {
		fields["path"] = c.Path
	}
	if c.Operator != "" {
		fields["operator"] = c.Operator
	}
	if c.hasValue || c.Value != nil {
		fields["value"] = c.Value
	}
	return json.Marshal(fields)
}

type declarationSource struct {
	input      Input
	structured bool
}

func declarationProcess(kind string) bool {
	return kind == "agent" || kind == "command" || kind == "tool" || kind == "validation"
}

func compileDeclaration(w Workflow) (Workflow, error) {
	if w.Declaration == nil {
		return w, nil
	}
	if w.Declaration.Version != 1 {
		return Workflow{}, fmt.Errorf("declaration version must be 1")
	}
	if len(w.Declaration.Steps) == 0 || len(w.Declaration.Steps) > maxDeclarationSteps {
		return Workflow{}, fmt.Errorf("declaration requires 1..%d steps", maxDeclarationSteps)
	}
	if len(w.Declaration.Actions) == 0 || len(w.Declaration.Actions) > maxDeclarationSteps {
		return Workflow{}, fmt.Errorf("declaration requires 1..%d actions", maxDeclarationSteps)
	}
	// Normalize JSON numbers and copy mutable config maps before inheriting defaults.
	data, err := json.Marshal(w.Declaration)
	if err != nil {
		return Workflow{}, fmt.Errorf("declaration: %w", err)
	}
	if len(data) > maxDeclarationBytes {
		return Workflow{}, fmt.Errorf("declaration exceeds maximum size")
	}
	var d Declaration
	if err := json.Unmarshal(data, &d); err != nil {
		return Workflow{}, fmt.Errorf("declaration: %w", err)
	}
	model, timeout, iterations := "", float64(3600), float64(3)
	if d.Defaults != nil {
		model = d.Defaults.Model
		if d.Defaults.TimeoutSeconds != nil {
			timeout = *d.Defaults.TimeoutSeconds
		}
		if d.Defaults.MaxIterations != nil {
			iterations = *d.Defaults.MaxIterations
		}
	}
	if math.IsNaN(timeout) || math.IsInf(timeout, 0) || timeout <= 0 || timeout > 604800 {
		return Workflow{}, fmt.Errorf("declaration timeoutSeconds must be positive and at most 604800")
	}
	if math.IsNaN(iterations) || iterations < 1 || iterations > 20 || math.Trunc(iterations) != iterations {
		return Workflow{}, fmt.Errorf("declaration maxIterations must be an integer in 1..20")
	}
	for name, action := range d.Actions {
		if err := validID(name); err != nil {
			return Workflow{}, fmt.Errorf("action: %w", err)
		}
		switch action.Kind {
		case "agent", "command", "tool", "validation", "approval", "integration", "workflow":
		default:
			return Workflow{}, fmt.Errorf("action %s has unsupported kind %q", name, action.Kind)
		}
	}
	sources := make(map[string]declarationSource, len(w.Inputs)+len(d.Steps))
	for name, typ := range w.Inputs {
		if err := validID(name); err != nil {
			return Workflow{}, err
		}
		if !validType(typ) {
			return Workflow{}, fmt.Errorf("unknown workflow input type %q", typ)
		}
		sources[name] = declarationSource{input: Input{Type: typ, From: "inputs." + name}}
	}
	// Reserve all user IDs before allocating compiler IDs so generated nodes
	// never collide with a step, even when a user chooses our usual prefix.
	ids := map[string]bool{"inputs": true}
	for _, step := range d.Steps {
		if err := validID(step.ID); err != nil {
			return Workflow{}, err
		}
		if ids[step.ID] {
			return Workflow{}, fmt.Errorf("duplicate or reserved step ID %q", step.ID)
		}
		ids[step.ID] = true
	}
	sequence := 0
	generatedID := func() string {
		for {
			sequence++
			id := fmt.Sprintf("declaration-%d", sequence)
			if !ids[id] {
				ids[id] = true
				return id
			}
		}
	}
	w.Nodes = make([]WorkflowNode, 0, len(d.Steps)*3+1)
	w.Edges = make([]Edge, 0, len(d.Steps)*4)
	w.Declaration = &d
	connect := func(source, target, when string) {
		if source != "" {
			w.Edges = append(w.Edges, Edge{ID: fmt.Sprintf("declaration-edge-%d", len(w.Edges)+1), Source: source, Target: target, When: when})
		}
	}
	previous, when := "", ""
	if d.Given != nil {
		gate, err := compileDeclarationCondition(*d.Given, sources)
		if err != nil {
			return Workflow{}, fmt.Errorf("given: %w", err)
		}
		gate.ID, gate.Name = generatedID(), "Given"
		w.Nodes = append(w.Nodes, gate)
		previous, when = gate.ID, "true"
	}
	for _, step := range d.Steps {
		action, ok := d.Actions[step.Run]
		if !ok {
			return Workflow{}, fmt.Errorf("step %s references unknown action %q", step.ID, step.Run)
		}
		action.ID = step.ID
		if action.Name == "" {
			action.Name = step.Run
		}
		// Each invocation gets its own map; defaults never mutate the source action.
		config := make(map[string]any, len(action.Config)+3)
		for key, value := range action.Config {
			config[key] = value
		}
		action.Config = config
		if declarationProcess(action.Kind) || action.Kind == "integration" {
			if _, exists := config["timeoutSeconds"]; !exists {
				config["timeoutSeconds"] = timeout
			}
		}
		if action.Kind == "agent" {
			if _, exists := config["model"]; !exists && model != "" {
				config["model"] = model
			}
			if _, exists := config["maxIterations"]; !exists {
				config["maxIterations"] = iterations
			}
		}
		inputs := action.Inputs
		if step.Inputs != nil {
			inputs = step.Inputs
		}
		if inputs == nil {
			inputs = make(map[string]Input, len(w.Inputs))
			for name, typ := range w.Inputs {
				inputs[name] = Input{Type: typ, From: name}
			}
		}
		action.Inputs, err = compileDeclarationInputs(inputs, sources)
		if err != nil {
			return Workflow{}, fmt.Errorf("step %s: %w", step.ID, err)
		}
		if step.If == nil {
			connect(previous, action.ID, when)
			w.Nodes = append(w.Nodes, action)
			previous = action.ID
		} else {
			gate, err := compileDeclarationCondition(*step.If, sources)
			if err != nil {
				return Workflow{}, fmt.Errorf("step %s if: %w", step.ID, err)
			}
			gate.ID, gate.Name = generatedID(), "If "+step.ID
			join := WorkflowNode{ID: generatedID(), Name: "Continue after " + step.ID, Kind: "parallel"}
			connect(previous, gate.ID, when)
			connect(gate.ID, action.ID, "true")
			connect(gate.ID, join.ID, "false")
			connect(action.ID, join.ID, "")
			w.Nodes = append(w.Nodes, gate, action, join)
			previous = join.ID
		}
		when = ""
		if step.As != "" {
			if err := validID(step.As); err != nil {
				return Workflow{}, fmt.Errorf("step %s alias: %w", step.ID, err)
			}
			if _, exists := sources[step.As]; exists {
				return Workflow{}, fmt.Errorf("step %s alias %q duplicates a result or workflow input", step.ID, step.As)
			}
			structured, _ := action.Config["outputJson"].(bool)
			sources[step.As] = declarationSource{input: Input{Type: nodeOutputType(action), From: action.ID + ".result"}, structured: declarationProcess(action.Kind) && structured}
		}
	}
	if len(w.Nodes) > maxDeclarationNodes {
		return Workflow{}, fmt.Errorf("declaration exceeds maximum compiled graph size")
	}
	return w, nil
}

func compileDeclarationInputs(inputs map[string]Input, sources map[string]declarationSource) (map[string]Input, error) {
	result := make(map[string]Input, len(inputs))
	for name, input := range inputs {
		if err := validID(name); err != nil {
			return nil, err
		}
		if !validType(input.Type) {
			return nil, fmt.Errorf("input %s has unknown type", name)
		}
		literal := input.literal || input.Value != nil
		if (input.From != "") == literal {
			return nil, fmt.Errorf("input %s must have exactly one of value/from", name)
		}
		if literal {
			if err := validateValue(Value{Type: input.Type, Value: input.Value}); err != nil {
				return nil, fmt.Errorf("input %s: %w", name, err)
			}
		} else {
			source, ok := sources[input.From]
			if !ok {
				return nil, fmt.Errorf("input %s references unknown or future source %q", name, input.From)
			}
			if source.input.Type != input.Type {
				return nil, fmt.Errorf("input %s type does not match source %s", name, input.From)
			}
			input.From = source.input.From
		}
		result[name] = input
	}
	return result, nil
}

func compileDeclarationCondition(condition DeclarationCondition, sources map[string]declarationSource) (WorkflowNode, error) {
	node := WorkflowNode{Kind: "branch", Inputs: map[string]Input{}, Config: map[string]any{}}
	count := 0
	var compile func(DeclarationCondition, int) (map[string]any, error)
	compile = func(c DeclarationCondition, depth int) (map[string]any, error) {
		group := c.All != nil || c.Any != nil
		if depth > 1 || !group {
			count++
		}
		if count > maxConditionRules || group && depth > maxConditionDepth {
			return nil, fmt.Errorf("condition exceeds maximum size or nesting depth")
		}
		if group {
			if c.All != nil && c.Any != nil || c.Path != "" || c.Operator != "" || c.hasValue || c.Value != nil {
				return nil, fmt.Errorf("condition must have exactly one of all, any, or path/operator")
			}
			children, combinator := c.All, "all"
			if c.Any != nil {
				children, combinator = c.Any, "any"
			}
			if len(children) == 0 {
				return nil, fmt.Errorf("condition group must be nonempty")
			}
			rules := make([]any, 0, len(children))
			for _, child := range children {
				rule, err := compile(child, depth+1)
				if err != nil {
					return nil, err
				}
				rules = append(rules, rule)
			}
			return map[string]any{"combinator": combinator, "rules": rules}, nil
		}
		parts := strings.Split(c.Path, ".")
		source, ok := sources[parts[0]]
		if !ok {
			return nil, fmt.Errorf("condition references unknown or future source %q", parts[0])
		}
		for _, part := range parts {
			if part == "" {
				return nil, fmt.Errorf("condition path must have nonempty components")
			}
		}
		node.Inputs[parts[0]] = source.input
		rule := map[string]any{"input": parts[0], "operator": c.Operator}
		if len(parts) > 1 {
			property := strings.Join(parts[1:], ".")
			if source.structured {
				property = "data." + property
			}
			rule["property"] = property
		}
		if c.hasValue || c.Value != nil {
			rule["value"] = c.Value
		}
		return rule, nil
	}
	conditions, err := compile(condition, 1)
	if err != nil {
		return WorkflowNode{}, err
	}
	if _, group := conditions["rules"]; !group {
		conditions = map[string]any{"combinator": "all", "rules": []any{conditions}}
	}
	if err := validateBranchConditions(conditions, node.Inputs); err != nil {
		return WorkflowNode{}, err
	}
	node.Config["conditions"] = conditions
	return node, nil
}
