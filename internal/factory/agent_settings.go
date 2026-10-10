package factory

import (
	"fmt"
	"math"
	"strings"
)

const defaultAgentIterations = 3
const maximumAgentIterations = 20

func validateAgentSettings(n WorkflowNode) error {
	for _, key := range []string{"model", "maxIterations", "until"} {
		if _, exists := n.Config[key]; exists && n.Kind != "agent" {
			return fmt.Errorf("node %s: %s is only supported for agents", n.ID, key)
		}
	}
	if model, exists := n.Config["model"]; exists {
		text, ok := model.(string)
		if !ok || strings.TrimSpace(text) == "" {
			return fmt.Errorf("agent %s model must be a nonempty string", n.ID)
		}
	}
	if count, exists := n.Config["maxIterations"]; exists {
		number, ok := count.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || number < 1 || number > maximumAgentIterations {
			return fmt.Errorf("agent %s maxIterations must be an integer from 1 to %d", n.ID, maximumAgentIterations)
		}
	}
	if until, exists := n.Config["until"]; exists {
		if outputJSON, _ := n.Config["outputJson"].(bool); !outputJSON {
			return fmt.Errorf("agent %s completion condition requires outputJson", n.ID)
		}
		if err := validateBranchConditions(until, map[string]Input{"result": {Type: nodeOutputType(n)}}); err != nil {
			return fmt.Errorf("agent %s completion condition: %w", n.ID, err)
		}
		if _, exists := n.Inputs["previousResult"]; exists {
			return fmt.Errorf("agent %s input previousResult is reserved for completion loops", n.ID)
		}
	}
	return nil
}
