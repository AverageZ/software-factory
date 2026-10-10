import type { RuleGroupType, RuleType } from "react-querybuilder";
import type { Input, Workflow, WorkflowEdge, WorkflowNode } from "./types";
import { valueTypes } from "./types";

export type Fact = { name: string; type: string; from: string; property?: string };
export type StageRule = { id: string; name: string; conditional: boolean; condition: RuleGroupType; facts: Fact[]; execution: "sequence" | "parallel"; actions: WorkflowNode[] };
export type WorkflowStage = { id: string; name: string; rules: StageRule[] };
type Conditions = { combinator: "all" | "any"; rules: (Conditions | { input: string; property?: string; operator: string; value?: unknown })[] };
const identifier = /^[A-Za-z0-9_-]{1,128}$/;
const types: Record<string, true> = { string: true, number: true, boolean: true, object: true, array: true };
const operators: Record<string, true> = { equals: true, notEquals: true, exists: true, notExists: true, greaterThan: true, greaterOrEqual: true, lessThan: true, lessOrEqual: true };
const numeric: Record<string, true> = { greaterThan: true, greaterOrEqual: true, lessThan: true, lessOrEqual: true };
const uid = (prefix: string) => `${prefix}_${crypto.randomUUID().slice(0, 12)}`;
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);

export function newRule(): StageRule {
  return { id: uid("rule"), name: "Run tasks", conditional: false, condition: { combinator: "and", rules: [] }, facts: [], execution: "sequence", actions: [{ id: uid("action"), name: "Review the result", kind: "approval", config: { message: "Review the result. Approve it to continue, or reject it to stop the run." } }] };
}
export function newStage(name = "New step"): WorkflowStage {
  return { id: uid("stage"), name, rules: [newRule()] };
}
export function initialStages(): WorkflowStage[] {
  const stage = newStage("Do the work");
  stage.rules[0].actions.unshift({ id: uid("action"), name: "Do the work", kind: "agent", repository: "primary", config: { prompt: "" } });
  return [stage];
}

function comparison(value: unknown, fact: Fact): unknown {
  if (fact.type === "string") {
    if (typeof value !== "string") throw new Error(`Enter text to compare with ${fact.name}.`);
    return value;
  }
  if (fact.type === "number") {
    const number = typeof value === "number" ? value : typeof value === "string" && value.trim() ? Number(value) : NaN;
    if (!Number.isFinite(number)) throw new Error(`Enter a finite number to compare with ${fact.name}.`);
    return number;
  }
  let parsed = value;
  if (typeof value === "string") {
    try { parsed = JSON.parse(value); } catch { throw new Error(`Enter valid ${fact.type} JSON to compare with ${fact.name}.`); }
  }
  if (fact.type === "boolean" && typeof parsed !== "boolean" || fact.type === "object" && parsed !== null && !object(parsed) || fact.type === "array" && !Array.isArray(parsed)) throw new Error(`Enter a ${fact.type} value to compare with ${fact.name}.`);
  return parsed;
}

function compileConditions(query: RuleGroupType, facts: Map<string, Fact>, referenced: Set<string>, depth = 1, count = { value: 0 }): Conditions {
  if (depth > 8 || !["and", "or"].includes(query.combinator) || !query.rules.length || query.not) throw new Error("Use all or any conditions. Each condition group needs a condition. Use no more than 8 levels. Do not negate a group.");
  return { combinator: query.combinator === "and" ? "all" : "any", rules: query.rules.map((item) => {
    if (++count.value > 256) throw new Error("Use no more than 256 conditions in a task group.");
    if ("rules" in item) return compileConditions(item as RuleGroupType, facts, referenced, depth + 1, count);
    const rule = item as RuleType;
    const fact = facts.get(rule.field);
    if (!fact) throw new Error(`Select a value to check for each condition (${rule.field || "missing"}).`);
    if (!Object.hasOwn(operators, rule.operator)) throw new Error(`This comparison is not supported: ${rule.operator}.`);
    if (Object.hasOwn(numeric, rule.operator) && fact.type !== "number") throw new Error(`Select a number value to check for this comparison (${fact.name}).`);
    referenced.add(fact.name);
    const base = { input: fact.name, ...(fact.property ? { property: fact.property } : {}), operator: rule.operator };
    return rule.operator === "exists" || rule.operator === "notExists" ? base : { ...base, value: comparison(rule.value, fact) };
  }) };
}

function validateAction(node: WorkflowNode) {
  if (!node.name.trim() || !object(node.config)) throw new Error("Each task needs a name and settings.");
  if (!["agent", "command", "tool", "validation", "approval", "integration", "workflow"].includes(node.kind)) throw new Error(`This task type is not supported: ${node.kind}.`);
  if (["agent", "command", "tool", "validation"].includes(node.kind)) {
    if (!node.repository || !identifier.test(node.repository)) throw new Error(`${node.name}: select a valid repository.`);
    if (node.kind === "agent") {
      if (typeof node.config.prompt !== "string" || !node.config.prompt.trim()) throw new Error(`${node.name}: enter instructions for the agent.`);
    } else if (!Array.isArray(node.config.command) || !node.config.command.length || node.config.command.some((arg) => typeof arg !== "string") || !String(node.config.command[0]).trim()) throw new Error(`${node.name}: enter an executable and its arguments.`);
    if (node.config.outputJson !== undefined && typeof node.config.outputJson !== "boolean") throw new Error(`${node.name}: set structured output to true or false.`);
  }
  if (node.kind === "approval" && (typeof node.config.message !== "string" || !node.config.message.trim())) throw new Error(`${node.name}: enter an approval message.`);
  if (node.kind === "workflow" && (typeof node.config.workflowId !== "string" || !node.config.workflowId)) throw new Error(`${node.name}: select a saved workflow.`);
  if (node.kind === "integration") {
    try { const url = new URL(String(node.config.url)); if (!["http:", "https:"].includes(url.protocol)) throw new Error(); } catch { throw new Error(`${node.name}: enter an HTTP or HTTPS URL.`); }
  }
  if (node.config.timeoutSeconds !== undefined && (typeof node.config.timeoutSeconds !== "number" || !Number.isFinite(node.config.timeoutSeconds) || node.config.timeoutSeconds <= 0)) throw new Error(`${node.name}: timeout must be a positive number.`);
}

function actionResultType(node: WorkflowNode): string {
  return node.kind === "approval" ? "Approval" : node.outputType || "object";
}

/** Compile opinionated stage control flow into the daemon's existing fail-closed DAG. */
export function compileStages(name: string, inputs: Record<string, string>, stages: WorkflowStage[], id = ""): Workflow {
  if (!name.trim()) throw new Error("Enter a workflow name.");
  if (!stages.length) throw new Error("Add at least one step.");
  for (const [key, type] of Object.entries(inputs)) if (!identifier.test(key) || !valueTypes.some((allowed) => allowed === type)) throw new Error(`Enter a valid run input name and type (${key || "missing name"}).`);
  const nodes: WorkflowNode[] = [], edges: WorkflowEdge[] = [];
  const used = new Set<string>();
  const previousActions = new Map<string, WorkflowNode>();
  const add = (node: WorkflowNode) => {
    if (!identifier.test(node.id) || used.has(node.id)) throw new Error(`Use a valid, unique ID for each step, task group, and task (${node.id}).`);
    used.add(node.id); nodes.push(node);
  };
  const edge = (source: string, target: string, when?: "true" | "false") => edges.push({ id: `edge_${edges.length}`, source, target, ...(when ? { when } : {}) });
  let previous = "";
  for (const stage of stages) {
    if (!stage.name.trim() || !stage.rules.length) throw new Error("Each step needs a name and at least one task group.");
    add({ id: stage.id, name: stage.name, kind: "parallel", config: {} });
    if (previous) edge(previous, stage.id);
    let tail = stage.id;
    for (const rule of stage.rules) {
      if (!rule.name.trim() || !rule.actions.length || !["sequence", "parallel"].includes(rule.execution)) throw new Error(`${stage.name}: each task group needs a name, at least one task, and a task order.`);
      let gate = "";
      if (rule.conditional) {
        const facts = new Map<string, Fact>();
        for (const fact of rule.facts) {
          if (!identifier.test(fact.name) || facts.has(fact.name) || !Object.hasOwn(types, fact.type)) throw new Error(`${rule.name}: each value to check needs a valid, unique name and a comparison type.`);
          if (fact.property && !fact.property.split(".").every((part) => identifier.test(part))) throw new Error(`${fact.name}: use valid property names separated by dots.`);
          facts.set(fact.name, fact);
        }
        const referenced = new Set<string>();
        const conditions = compileConditions(rule.condition, facts, referenced);
        const bindings: Record<string, Input> = {};
        for (const key of referenced) {
          const fact = facts.get(key)!;
          const [source, result, extra] = fact.from.split(".");
          const prior = previousActions.get(source);
          const sourceType = source === "inputs" ? inputs[result] : result === "result" && prior ? actionResultType(prior) : undefined;
          if (extra || !sourceType) throw new Error(`${fact.name}: select a run input or a task result from an earlier step.`);
          if (!fact.property && fact.type !== (Object.hasOwn(types, sourceType) ? sourceType : "object")) throw new Error(`${fact.name}: use the source type (${sourceType}) or select a property to check.`);
          bindings[key] = { type: sourceType, from: fact.from };
        }
        gate = rule.id;
        add({ id: gate, name: rule.name, kind: "branch", outputType: "boolean", inputs: bindings, config: { conditions } });
        edge(tail, gate);
      } else {
        add({ id: rule.id, name: rule.name, kind: "parallel", config: {} });
        edge(tail, rule.id);
      }
      const start = gate || rule.id;
      let last = start;
      for (const action of rule.actions) {
        validateAction(action);
        // Facts and action inputs cannot create undeclared dependencies on later work.
        for (const [key, binding] of Object.entries(action.inputs || {})) {
          if (!identifier.test(key) || !binding.type) throw new Error(`${action.name}: enter a valid input name and type (${key}).`);
          if (binding.from) {
            const [source, output, extra] = binding.from.split(".");
            const earlier = previousActions.get(source) || (rule.execution === "sequence" ? rule.actions.slice(0, rule.actions.indexOf(action)).find((node) => node.id === source) : undefined);
            const sourceType = source === "inputs" ? inputs[output] : output === "result" && earlier ? actionResultType(earlier) : undefined;
            if (extra || !sourceType || sourceType !== binding.type) throw new Error(`${action.name}: select an earlier task result or run input with the same type for ${key}.`);
          }
        }
        add(structuredClone(action));
        const source = rule.execution === "parallel" ? start : last;
        edge(source, action.id, source === gate && gate ? "true" : undefined);
        last = action.id;
      }
      const merge = `${rule.id}_done`;
      add({ id: merge, name: `${rule.name} · Wait for all`, kind: "parallel", config: {} });
      if (rule.execution === "parallel") rule.actions.forEach((action) => edge(action.id, merge));
      else edge(last, merge);
      if (gate) edge(gate, merge, "false");
      tail = merge;
    }
    previous = tail;
    stage.rules.forEach((rule) => rule.actions.forEach((action) => previousActions.set(action.id, action)));
  }
  nodes[0].config.stageEditor = { version: 1, stages: structuredClone(stages) };
  return { id, name: name.trim(), inputs, nodes, edges };
}

function canonical(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(canonical).join(",")}]`;
  if (object(value)) return `{${Object.keys(value).filter((key) => value[key] !== undefined).sort().map((key) => `${JSON.stringify(key)}:${canonical(value[key])}`).join(",")}}`;
  return JSON.stringify(value);
}

/** Never replace an edited or arbitrary graph with stale presentation metadata. */
export function stagesFromWorkflow(workflow: Workflow): WorkflowStage[] | null {
  const metadata = workflow.nodes[0]?.config.stageEditor;
  if (object(metadata) && metadata.version === 1 && Array.isArray(metadata.stages)) {
    try {
      const stages = metadata.stages as WorkflowStage[];
      const compiled = compileStages(workflow.name, workflow.inputs || {}, stages, workflow.id);
      if (canonical(compiled.nodes) === canonical(workflow.nodes) && canonical(compiled.edges) === canonical(workflow.edges)) return structuredClone(stages);
    } catch { /* Invalid metadata stays in the graph editor, never guessed. */ }
    return null;
  }
  // Lossless migration of the previous single-gate recipe and simple linear workflows.
  const targets = new Set(workflow.edges.map((edge) => edge.target));
  const roots = workflow.nodes.filter((node) => !targets.has(node.id));
  if (roots.length !== 1) return null;
  let node: WorkflowNode | undefined = roots[0];
  const rule = newRule(); rule.actions = []; rule.name = "Run actions";
  const seen = new Set<string>();
  if (node.kind === "branch" && object(node.config.conditions)) {
    const branchInputs = node.inputs || {};
    const convert = (group: Conditions): RuleGroupType => ({ combinator: group.combinator === "all" ? "and" : "or", rules: group.rules.map((item) => {
      if ("rules" in item) return convert(item);
      const binding = branchInputs[item.input];
      if (!binding?.from) throw new Error("Use Advanced: edit connections for fixed condition inputs.");
      const type = typeof item.value === "string" ? "string" : typeof item.value === "number" ? "number" : typeof item.value === "boolean" ? "boolean" : Array.isArray(item.value) ? "array" : Object.hasOwn(types, binding.type) ? binding.type : "object";
      const name = `fact_${rule.facts.length + 1}`;
      rule.facts.push({ name, type, from: binding.from, ...(item.property ? { property: item.property } : {}) });
      return { field: name, operator: item.operator, value: item.value };
    }) });
    try {
      rule.condition = convert(node.config.conditions as unknown as Conditions);
      rule.conditional = true; rule.name = node.name;
      seen.add(node.id);
      const outgoing = workflow.edges.filter((edge) => edge.source === node!.id);
      if (outgoing.length !== 1 || outgoing[0].when !== "true") return null;
      node = workflow.nodes.find((entry) => entry.id === outgoing[0].target);
    } catch { return null; }
  }
  while (node) {
    if (seen.has(node.id) || !["agent", "command", "tool", "validation", "approval", "integration", "workflow"].includes(node.kind)) return null;
    seen.add(node.id); rule.actions.push(structuredClone(node));
    const outgoing = workflow.edges.filter((edge) => edge.source === node!.id);
    if (!outgoing.length) break;
    if (outgoing.length !== 1 || outgoing[0].when) return null;
    node = workflow.nodes.find((entry) => entry.id === outgoing[0].target);
    if (!node) return null;
  }
  if (seen.size !== workflow.nodes.length || workflow.edges.length !== workflow.nodes.length - 1 || !rule.actions.length) return null;
  const stage = newStage("Execution"); stage.rules = [rule];
  try { compileStages(workflow.name, workflow.inputs || {}, [stage], workflow.id); }
  catch { return null; }
  return [stage];
}

export function policyPreview(stages: WorkflowStage[]): string {
  const labels: Record<string, string> = { equals: "is", notEquals: "is not", exists: "exists", notExists: "does not exist", greaterThan: "is greater than", greaterOrEqual: "is at least", lessThan: "is less than", lessOrEqual: "is at most" };
  const describe = (query: RuleGroupType): string => query.rules.map((item) => "rules" in item ? `(${describe(item as RuleGroupType)})` : `${item.field || "[select a value to check]"} ${labels[item.operator] || item.operator}${["exists", "notExists"].includes(item.operator) ? "" : ` ${JSON.stringify(item.value) ?? "[enter a comparison value]"}`}`).join(query.combinator === "or" ? " or " : " and ");
  const describeTask = (action: WorkflowNode): string => {
    const instruction = action.kind === "approval" ? "Wait for approval" : action.kind === "validation" ? "Run a check" : action.kind === "agent" ? "Ask AI to do work" : action.kind === "workflow" ? "Run another workflow" : action.kind === "integration" ? "Send a web request" : action.kind === "tool" ? "Run a project tool" : "Run a command";
    return `${instruction}: “${action.name}”.`;
  };
  const steps = stages.map((stage, index) => {
    const groups = stage.rules.map((rule, groupIndex) => [
      `Task group ${groupIndex + 1}: ${rule.name}`,
      rule.conditional ? `Run this task group only if ${describe(rule.condition)}.\nIf the conditions do not match, skip this task group and continue.` : "Run this task group when the step reaches it.",
      rule.execution === "parallel" ? "Start the tasks together, within the project’s limit. Wait for all tasks to finish before the next task group or step." : "Run the tasks in order. Wait for each task to finish before the next task.",
      ...rule.actions.map((action, taskIndex) => `  ${taskIndex + 1}. ${describeTask(action)}`),
    ].join("\n"));
    return [`Step ${index + 1}: ${stage.name}`, "Run the task groups in order.", ...groups].join("\n\n");
  });
  return ["Run the steps in order. Saving does not start a run.", ...steps, "If a task fails or approval is rejected, stop the run. Do not start later tasks. Tasks that already started may finish."].join("\n\n");
}
