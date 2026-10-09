import { useState } from "react";
import { errorMessage, href, json, parseObject, post, request } from "./api";
import { ErrorNotice, JsonField, PageHeader } from "./components";
import { valueTypes } from "./types";
import type { Workflow, WorkflowEdge, WorkflowNode } from "./types";

type Rule = { input: string; property?: string; operator: "equals" | "notEquals" | "exists" | "notExists"; value?: unknown; _text?: true };
type Group = { combinator: "all" | "any"; rules: (Group | Rule)[] };
type Condition = Group | Rule;
type Action = { node: WorkflowNode; configText: string; inputText: string };
const actionKinds = ["workflow", "agent", "command", "validation", "approval", "integration"] as const;
type ActionKind = (typeof actionKinds)[number];
const freshRule = (): Rule => ({ input: "", operator: "equals", value: true });
const freshGroup = (): Group => ({ combinator: "all", rules: [freshRule()] });
const isGroup = (item: Condition): item is Group => "rules" in item;
const identifier = /^[A-Za-z0-9_-]{1,128}$/;

// A recipe is a single condition gate followed by a linear sequence of actions.
// Do not project arbitrary graphs: fan-out, merges and false paths need the graph editor.
export function recipeFromWorkflow(workflow: Workflow): { condition: Group; actions: Action[] } | null {
  const gate = workflow.nodes.find((node) => node.kind === "branch" && node.config.conditions);
  if (!gate || !isConditionGroup(gate.config.conditions)) return null;
  if (workflow.nodes.length < 2 || workflow.nodes.filter((node) => node.kind === "branch").length !== 1) return null;
  const actions: Action[] = [];
  const seen = new Set([gate.id]);
  let source = gate.id;
  while (true) {
    const outgoing = workflow.edges.filter((edge) => edge.source === source);
    if (!outgoing.length) break;
    if (outgoing.length !== 1 || outgoing[0].when !== (source === gate.id ? "true" : undefined)) return null;
    const node = workflow.nodes.find((entry) => entry.id === outgoing[0].target);
    if (!node || seen.has(node.id) || !actionKinds.some((kind) => kind === node.kind)) return null;
    seen.add(node.id);
    actions.push({ node, configText: json(node.config), inputText: json(node.inputs || {}) });
    source = node.id;
  }
  if (seen.size !== workflow.nodes.length || workflow.edges.length !== actions.length) return null;
  return { condition: gate.config.conditions as Group, actions };
}

function isConditionGroup(value: unknown): value is Group {
  if (!value || typeof value !== "object" || Array.isArray(value)) return false;
  const group = value as Record<string, unknown>;
  return (group.combinator === "all" || group.combinator === "any") && Array.isArray(group.rules) && group.rules.length > 0 && group.rules.every((item) => {
    if (!item || typeof item !== "object" || Array.isArray(item)) return false;
    return "rules" in item ? isConditionGroup(item) : typeof (item as Rule).input === "string";
  });
}

function replaceAt(root: Group, path: number[], change: (item: Condition) => Condition | null): Group {
  function replace(group: Group, depth: number): Group {
    const index = path[depth];
    const rules = [...group.rules];
    if (depth === path.length - 1) {
      const updated = change(rules[index]);
      if (updated) rules[index] = updated;
      else rules.splice(index, 1);
    } else {
      rules[index] = replace(rules[index] as Group, depth + 1);
    }
    return { ...group, rules };
  }
  return replace(root, 0);
}

function ConditionEditor({ group, inputs, onChange, depth = 0 }: {
  group: Group;
  inputs: Record<string, string>;
  onChange: (group: Group) => void;
  depth?: number;
}) {
  return (
    <div className="condition-group">
      <div className="recipe-row">
        <strong>{depth === 0 ? "GIVEN" : "GROUP"}</strong>
        <select aria-label={depth === 0 ? "Condition match" : "Group match"} value={group.combinator} onChange={(event) => onChange({ ...group, combinator: event.target.value as Group["combinator"] })}>
          <option value="all">ALL of these</option>
          <option value="any">ANY of these</option>
        </select>
      </div>
      {group.rules.map((item, index) => {
        const update = (next: Condition | null) => onChange(replaceAt(group, [index], () => next));
        return (
          <div className="recipe-condition" key={index}>
            {isGroup(item) ? (
              <ConditionEditor group={item} inputs={inputs} onChange={update} depth={depth + 1} />
            ) : (
              <div className="recipe-row">
                <span>{index ? (group.combinator === "all" ? "AND" : "OR") : "IF"}</span>
                <select aria-label="Condition input" value={item.input} onChange={(event) => update({ ...item, input: event.target.value })}>
                  <option value="">Select run input</option>
                  {Object.keys(inputs).map((name) => <option key={name} value={name}>{name}</option>)}
                </select>
                <input aria-label="Property path" placeholder="property (optional)" value={item.property || ""} onChange={(event) => update({ ...item, property: event.target.value || undefined })} />
                <select aria-label="Condition operator" value={item.operator} onChange={(event) => update({ ...item, operator: event.target.value as Rule["operator"] })}>
                  <option value="equals">is</option>
                  <option value="notEquals">is not</option>
                  <option value="exists">exists</option>
                  <option value="notExists">does not exist</option>
                </select>
                {(item.operator === "equals" || item.operator === "notEquals") && (
                  <input aria-label="Comparison JSON value" placeholder='JSON value, e.g. "Trellis"' value={item._text ? String(item.value) : json(item.value)} onChange={(event) => {
                    // Keep invalid intermediate text visible and reject it at save time.
                    update({ ...item, value: event.target.value, _text: true });
                  }} />
                )}
              </div>
            )}
            {depth > 0 || group.rules.length > 1 ? <button type="button" className="small" onClick={() => update(null)}>Remove condition</button> : null}
          </div>
        );
      })}
      <div className="actions">
        <button type="button" className="small" onClick={() => onChange({ ...group, rules: [...group.rules, freshRule()] })}>Add condition</button>
        {depth < 7 && <button type="button" className="small" onClick={() => onChange({ ...group, rules: [...group.rules, freshGroup()] })}>Add ALL / ANY group</button>}
      </div>
    </div>
  );
}

function defaultConfig(kind: ActionKind): Record<string, unknown> {
  switch (kind) {
    case "workflow": return { workflowId: "" };
    case "agent": return { prompt: "" };
    case "approval": return { message: "" };
    case "integration": return { url: "", method: "GET" };
    default: return { command: [] };
  }
}
function configField(text: string, key: string): string {
  try {
    const value = JSON.parse(text) as Record<string, unknown>;
    return typeof value[key] === "string" ? value[key] : "";
  } catch {
    return "";
  }
}

function updateConfigField(text: string, key: string, value: string): string {
  const config = parseObject<unknown>(text, "Action configuration");
  return json({ ...config, [key]: value });
}

function newAction(kind: ActionKind): Action {
  const node: WorkflowNode = { id: `step_${crypto.randomUUID().slice(0, 8)}`, name: kind, kind, config: defaultConfig(kind) };
  return { node, configText: json(node.config), inputText: "{}" };
}

function compiledConditions(group: Group, inputs: Record<string, string>): Group {
  if (!group.rules.length) throw new Error("A condition group needs at least one rule.");
  return { combinator: group.combinator, rules: group.rules.map((item): Condition => {
    if (isGroup(item)) return compiledConditions(item, inputs);
    if (!Object.hasOwn(inputs, item.input)) throw new Error(`Select a declared run input for every condition (${item.input || "missing"}).`);
    if (item.property && !item.property.split(".").every((part) => identifier.test(part))) throw new Error("Property paths must contain dot-separated names.");
    if (item.operator === "equals" || item.operator === "notEquals") {
      let value = item.value;
      if (item._text) {
        try { value = JSON.parse(String(item.value)); }
        catch { throw new Error(`Condition on ${item.input} needs a valid JSON comparison value.`); }
      }
      return { input: item.input, ...(item.property ? { property: item.property } : {}), operator: item.operator, value };
    }
    return { input: item.input, ...(item.property ? { property: item.property } : {}), operator: item.operator };
  }) };
}

export function RecipeEditor({ workflow, workflows, onSaved, onGraph }: {
  workflow?: Workflow;
  workflows: Workflow[];
  onSaved: (workflow: Workflow) => void;
  onGraph: () => void;
}) {
  const parsed = workflow ? recipeFromWorkflow(workflow) : null;
  const [name, setName] = useState(workflow?.name || "");
  const [inputRows, setInputRows] = useState(Object.entries(workflow?.inputs || {}).map(([key, type]) => ({ key, type })));
  const [condition, setCondition] = useState<Group>(parsed?.condition || freshGroup());
  const [actions, setActions] = useState<Action[]>(() => parsed?.actions || [newAction("agent")]);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const [dragging, setDragging] = useState<number | null>(null);
  const inputs = Object.fromEntries(inputRows.map(({ key, type }) => [key, type]));
  const [gateId] = useState(() => workflow?.nodes.find((node) => node.kind === "branch" && node.config.conditions)?.id || `given_${crypto.randomUUID().slice(0, 8)}`);

  function updateAction(index: number, action: Action) {
    setActions((current) => current.map((entry, at) => at === index ? action : entry));
    setMessage("");
  }
  function changeActionConfig(index: number, action: Action, key: string, value: string) {
    try {
      updateAction(index, { ...action, configText: updateConfigField(action.configText, key, value) });
      setError("");
    } catch (cause) {
      setError(errorMessage(cause));
    }
  }
  function moveAction(from: number, to: number) {
    if (from === to || to < 0 || to >= actions.length) return;
    setActions((current) => { const next = [...current]; next.splice(to, 0, next.splice(from, 1)[0]); return next; });
    setMessage("");
  }
  async function save() {
    setError(""); setMessage(""); setSaving(true);
    try {
      if (!name.trim()) throw new Error("Enter a workflow name.");
      if (!actions.length) throw new Error("Add at least one THEN action.");
      for (const row of inputRows) if (!identifier.test(row.key)) throw new Error(`Invalid run input name: ${row.key || "empty"}.`);
      if (Object.keys(inputs).length !== inputRows.length) throw new Error("Run input names must be unique.");
      const conditions = compiledConditions(condition, inputs);
      const referenced = new Set<string>();
      const collect = (group: Group) => group.rules.forEach((item) => isGroup(item) ? collect(item) : referenced.add(item.input));
      collect(conditions);
      const gate: WorkflowNode = { id: gateId, name: "Given conditions", kind: "branch", outputType: "boolean", inputs: Object.fromEntries([...referenced].map((key) => [key, { type: inputs[key], from: `inputs.${key}` }])), config: { conditions } };
      const nodes = [gate, ...actions.map(({ node, configText, inputText }) => ({ ...node, config: parseObject(configText, `Configuration for ${node.name}`), inputs: parseObject(inputText, `Inputs for ${node.name}`) }))];
      const edges: WorkflowEdge[] = actions.map((action, index) => ({ id: `recipe_${action.node.id}`, source: index ? actions[index - 1].node.id : gateId, target: action.node.id, ...(index === 0 ? { when: "true" as const } : {}) }));
      const saved = await post<Workflow>("/workflows", { id: workflow?.id || "", name, inputs, nodes, edges });
      onSaved(saved);
      try {
        await request(`/workflows/${encodeURIComponent(saved.id)}/layout`, { method: "PUT", body: JSON.stringify({ nodes: Object.fromEntries(nodes.map((node, index) => [node.id, { x: 70 + index * 280, y: 70 }] )) }) });
        setMessage("Recipe saved. Start it through a project binding.");
      } catch (cause) { setError(`Recipe saved, but graph layout was not saved: ${errorMessage(cause)}`); }
      if (!workflow) window.location.hash = href("workflows", saved.id);
    } catch (cause) { setError(errorMessage(cause)); }
    finally { setSaving(false); }
  }

  return <>
    <PageHeader title={workflow ? workflow.name : "New recipe"} description="Configure a run-start recipe without arranging a graph. Conditions gate ordered actions; a false condition skips them. Failures stop the run for inspection or explicit retry.">
      <a className="button" href={href("workflows")}>All workflows</a>
      <button onClick={onGraph}>Graph editor</button>
    </PageHeader>
    <ErrorNotice message={error} />
    {message && <div className="notice" role="status">{message}</div>}
    <div className="recipe-editor">
      <section className="panel stack">
        <h2>WHEN · A binding is started</h2>
        <p className="muted">No automatic GitHub event trigger or authorization is created. Bind this workflow to a project and start that binding explicitly.</p>
        <label className="field"><span>Workflow name</span><input value={name} onChange={(event) => setName(event.target.value)} /></label>
        <h3>Run inputs</h3>
        {inputRows.map((row, index) => <div className="recipe-row" key={index}>
          <input aria-label="Run input name" placeholder="Input name" value={row.key} onChange={(event) => setInputRows((current) => current.map((entry, at) => at === index ? { ...entry, key: event.target.value } : entry))} />
          <select aria-label="Run input type" value={row.type} onChange={(event) => setInputRows((current) => current.map((entry, at) => at === index ? { ...entry, type: event.target.value } : entry))}>{valueTypes.map((type) => <option key={type}>{type}</option>)}</select>
          <button className="small" onClick={() => setInputRows((current) => current.filter((_, at) => at !== index))}>Remove input</button>
        </div>)}
        <button onClick={() => setInputRows((current) => [...current, { key: "", type: "string" }])}>Add run input</button>
      </section>
      <section className="panel stack"><h2>GIVEN · Conditions</h2><ConditionEditor group={condition} inputs={inputs} onChange={setCondition} /><p className="muted">Compare typed run inputs or a dotted object property. Values use JSON syntax: strings need quotes; null is distinct from a missing property.</p></section>
      <section className="panel stack"><h2>THEN · Ordered actions</h2>
        {actions.map((action, index) => <div className="recipe-action" key={action.node.id} draggable onDragStart={() => setDragging(index)} onDragEnd={() => setDragging(null)} onDragOver={(event) => event.preventDefault()} onDrop={(event) => { event.preventDefault(); if (dragging !== null) moveAction(dragging, index); setDragging(null); }}>
          <div className="recipe-row"><strong>{index + 1}. {index === 0 ? "THEN" : "AFTER SUCCESS"}</strong>
            <select aria-label={`Action ${index + 1} kind`} value={action.node.kind} onChange={(event) => {
              const kind = event.target.value as ActionKind;
              updateAction(index, {
                node: {
                  ...action.node,
                  kind,
                  name: action.node.name === action.node.kind ? kind : action.node.name,
                  repository: ["agent", "command", "validation"].includes(kind) ? action.node.repository : undefined,
                },
                configText: json(defaultConfig(kind)),
                inputText: "{}",
              });
            }}>
              {actionKinds.map((kind) => <option key={kind} value={kind}>{kind === "workflow" ? "Run workflow" : kind}</option>)}
            </select>
            <input aria-label={`Action ${index + 1} name`} value={action.node.name} onChange={(event) => updateAction(index, { ...action, node: { ...action.node, name: event.target.value } })} />
            <button className="small" disabled={index === 0} onClick={() => moveAction(index, index - 1)}>Up</button>
            <button className="small" disabled={index === actions.length - 1} onClick={() => moveAction(index, index + 1)}>Down</button>
            <button className="small danger" onClick={() => setActions((current) => current.filter((_, at) => at !== index))}>Remove</button>
          </div>
          {action.node.kind === "workflow" ? <label className="field"><span>Child workflow</span>
            <select value={(parseConfigId(action.configText))} onChange={(event) => updateAction(index, { ...action, configText: json({ workflowId: event.target.value }) })}>
              <option value="">Select workflow</option>{workflows.filter((item) => item.id !== workflow?.id).map((item) => <option value={item.id} key={item.id}>{item.name}</option>)}
            </select></label> : null}
          {["agent", "command", "validation"].includes(action.node.kind) && <label className="field"><span>Repository slot</span><input value={action.node.repository || ""} onChange={(event) => updateAction(index, { ...action, node: { ...action.node, repository: event.target.value || undefined } })} placeholder="Mapped by the project binding" /></label>}
          {action.node.kind === "agent" && <label className="field"><span>Workflow prompt</span><textarea rows={5} value={configField(action.configText, "prompt")} onChange={(event) => changeActionConfig(index, action, "prompt", event.target.value)} /></label>}
          {action.node.kind === "approval" && <label className="field"><span>Approval message</span><input value={configField(action.configText, "message")} onChange={(event) => changeActionConfig(index, action, "message", event.target.value)} /></label>}
          {action.node.kind === "integration" && <label className="field"><span>HTTP URL</span><input value={configField(action.configText, "url")} onChange={(event) => changeActionConfig(index, action, "url", event.target.value)} /></label>}
          <details>
            <summary>Advanced action inputs and configuration</summary>
            <div className="stack">
              {action.node.kind !== "workflow" && <JsonField label="Action configuration (JSON)" value={action.configText} onChange={(configText) => updateAction(index, { ...action, configText })} rows={4} />}
              <JsonField label="Action inputs (JSON)" value={action.inputText} onChange={(inputText) => updateAction(index, { ...action, inputText })} rows={3} help='Use typed inputs such as {"request":{"type":"string","from":"inputs.request"}}. Child workflows require all declared inputs.' />
            </div>
          </details>
        </div>)}
        <button onClick={() => setActions((current) => [...current, newAction("agent")])}>Add action</button>
        <p className="muted">Drag rows or use Up/Down to reorder. A failed action stops the run; this editor does not promise an ON FAILURE branch, wait, retry or parallel semantics.</p>
      </section>
      <div className="actions"><button className="primary" disabled={saving} onClick={() => void save()}>{saving ? "Saving…" : "Save recipe"}</button></div>
    </div>
  </>;
}

function parseConfigId(text: string): string {
  try { const value: unknown = JSON.parse(text); return value && typeof value === "object" && "workflowId" in value && typeof value.workflowId === "string" ? value.workflowId : ""; }
  catch { return ""; }
}
