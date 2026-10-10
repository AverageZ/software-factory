import type { Condition, Declaration, Input, Workflow, WorkflowNode } from "./types";

export const comparisons = [
  ["equals", "is"], ["notEquals", "is not"], ["contains", "contains"], ["notContains", "does not contain"],
  ["exists", "exists"], ["notExists", "does not exist"], ["greaterThan", "is greater than"],
  ["greaterOrEqual", "is at least"], ["lessThan", "is less than"], ["lessOrEqual", "is at most"],
] as const;
export const identifier = /^[A-Za-z0-9_-]{1,128}$/;
export const actionKinds = ["agent", "command", "tool", "validation", "approval", "integration", "workflow"] as const;
export function conditionText(condition: Condition): string {
  if ("all" in condition) return `(${condition.all.map(conditionText).join(" and ")})`;
  if ("any" in condition) return `(${condition.any.map(conditionText).join(" or ")})`;
  const operator = comparisons.find(([key]) => key === condition.operator)?.[1] || condition.operator;
  return `${condition.path} ${operator}${["exists", "notExists"].includes(condition.operator) ? "" : ` ${JSON.stringify(condition.value)}`}`;
}
export function declarationPreview(declaration: Declaration): string {
  return [declaration.given ? `Given ${conditionText(declaration.given)}` : "Given any supplied start data",
    ...declaration.steps.map((step) => {
      const action = declaration.actions[step.run];
      const until = untilToCondition(action?.config.until);
      const loop = until ? ` (until ${conditionText(until)}; at most ${action.config.maxIterations ?? declaration.defaults?.maxIterations ?? 3} passes)` : "";
      return `${step.if ? `If ${conditionText(step.if)}, then` : "Then"} ${step.run}${step.as ? ` → ${step.as}` : ""}${loop}`;
    }),
  ].join("\n");
}
export function workflowActions(workflow: Workflow): WorkflowNode[] {
  if (workflow.declaration) return workflow.declaration.steps.flatMap((step) => {
    const action = workflow.declaration?.actions[step.run];
    return action ? [action] : [];
  });
  return workflow.nodes || [];
}
// The loop runtime uses branch rules; declarations use named property paths.
export function conditionToUntil(condition: Condition): Record<string, unknown> {
  function rule(value: Condition): Record<string, unknown> {
    if ("all" in value) return { combinator: "all", rules: value.all.map(rule) };
    if ("any" in value) return { combinator: "any", rules: value.any.map(rule) };
    const [input, ...property] = value.path.split(".");
    return { input, ...(property.length ? { property: property.join(".") } : {}), operator: value.operator, ...(!["exists", "notExists"].includes(value.operator) ? { value: value.value } : {}) };
  }
  const compiled = rule(condition);
  return "path" in condition ? { combinator: "all", rules: [compiled] } : compiled;
}
export function untilToCondition(raw: unknown): Condition | undefined {
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) return undefined;
  const rule = raw as Record<string, unknown>;
  if ((rule.combinator === "all" || rule.combinator === "any") && Array.isArray(rule.rules)) {
    const children = rule.rules.map(untilToCondition);
    if (children.some((child) => !child)) return undefined;
    return rule.combinator === "all" ? { all: children as Condition[] } : { any: children as Condition[] };
  }
  if (typeof rule.input !== "string") return undefined;
  const path = `${rule.input}${rule.property ? `.${String(rule.property)}` : ""}`;
  if (typeof rule.operator === "string") return { path, operator: rule.operator, ...(Object.hasOwn(rule, "value") ? { value: rule.value } : {}) };
  if (Object.hasOwn(rule, "equals")) return { path, operator: "equals", value: rule.equals };
  return undefined;
}
export function validateCondition(condition: Condition, sources: Set<string>, label: string): void {
  if ("all" in condition || "any" in condition) {
    const children = "all" in condition ? condition.all : condition.any;
    if (!children.length) throw new Error(`${label}: add at least one condition.`);
    children.forEach((child) => validateCondition(child, sources, label));
    return;
  }
  if (!condition.path || condition.path.split(".").some((part) => !part) || !sources.has(condition.path.split(".")[0])) throw new Error(`${label}: “${condition.path}” must start with start data or an earlier result name.`);
  if (!comparisons.some(([operator]) => operator === condition.operator)) throw new Error(`${label}: unsupported comparison.`);
  if (!["exists", "notExists"].includes(condition.operator) && condition.value === undefined) throw new Error(`${label}: enter a comparison value.`);
  if (["greaterThan", "greaterOrEqual", "lessThan", "lessOrEqual"].includes(condition.operator) && (typeof condition.value !== "number" || !Number.isFinite(condition.value))) throw new Error(`${label}: numerical comparisons need a number.`);
}
export function validateMappings(inputs: Record<string, Input> | undefined, sources: Map<string, string>, label: string): void {
  for (const [name, input] of Object.entries(inputs || {})) {
    if (!identifier.test(name) || !input || !input.type) throw new Error(`${label}: each input needs a valid name and type.`);
    if (input.from !== undefined) {
      if (Object.hasOwn(input, "value")) throw new Error(`${label}.${name}: choose a source or a literal, not both.`);
      if (!sources.has(input.from)) throw new Error(`${label}.${name}: choose start data or an earlier result.`);
      if (sources.get(input.from) !== input.type) throw new Error(`${label}.${name}: source type must match ${input.type}.`);
    } else if (!Object.hasOwn(input, "value")) throw new Error(`${label}.${name}: enter a literal value or select a source.`);
  }
}
