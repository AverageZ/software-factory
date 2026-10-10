import { useEffect, useState } from "react";
import { QueryBuilder } from "react-querybuilder";
import type { RuleGroupType } from "react-querybuilder";
import { errorMessage, href, isPlayground, json, parseObject, post, request } from "./api";
import { ErrorNotice, JsonField, PageHeader } from "./components";
import { valueTypes } from "./types";
import type { Input, Workflow, WorkflowNode } from "./types";
import { compileStages, initialStages, newRule, newStage, policyPreview, stagesFromWorkflow } from "./workflowStages";
import type { Fact, StageRule, WorkflowStage } from "./workflowStages";

const actionLabels = {
  agent: "Ask AI to do work",
  command: "Run a command",
  tool: "Run a project tool",
  validation: "Run a check",
  approval: "Wait for approval",
  integration: "Send a web request",
  workflow: "Run another workflow",
} as const;
export type ActionKind = keyof typeof actionLabels;
export type ActionDraft = { config?: string; inputs?: string; argv?: string };
type Source = { from: string; label: string; type: string };
const identifier = /^[A-Za-z0-9_-]{1,128}$/;
const typeLabels: Record<string, string> = { string: "Text", number: "Number", boolean: "True or false", object: "JSON object", array: "JSON list" };
const operators = [
  { name: "equals", label: "is" },
  { name: "notEquals", label: "is not" },
  { name: "exists", label: "exists", arity: "unary" as const },
  { name: "notExists", label: "does not exist", arity: "unary" as const },
  { name: "greaterThan", label: ">" },
  { name: "greaterOrEqual", label: "≥" },
  { name: "lessThan", label: "<" },
  { name: "lessOrEqual", label: "≤" },
];

function reordered<T>(items: T[], from: number, to: number): T[] {
  if (to < 0 || to >= items.length || from === to) return items;
  const next = [...items];
  next.splice(to, 0, next.splice(from, 1)[0]);
  return next;
}

export function newAction(kind: ActionKind): WorkflowNode {
  const config = kind === "agent" ? { prompt: "" }
    : kind === "approval" ? { message: "" }
    : kind === "integration" ? { url: "", method: "GET" }
    : kind === "workflow" ? { workflowId: "" } : { command: [] };
  return { id: `action_${crypto.randomUUID().slice(0, 8)}`, name: actionLabels[kind], kind, config, ...(["agent", "command", "tool", "validation"].includes(kind) ? { repository: "primary" } : {}) };
}

function MoveButtons({ label, index, count, onMove, onRemove }: {
  label: string; index: number; count: number; onMove: (to: number) => void; onRemove: () => void;
}) {
  return <div className="stage-move actions">
    <button type="button" className="small" aria-label={`Move ${label} up`} title="Move up" disabled={index === 0} onClick={() => onMove(index - 1)}>↑</button>
    <button type="button" className="small" aria-label={`Move ${label} down`} title="Move down" disabled={index === count - 1} onClick={() => onMove(index + 1)}>↓</button>
    <button type="button" className="small" aria-label={`Remove ${label}`} onClick={onRemove}>Remove</button>
  </div>;
}

function AddTask({ onAdd }: { onAdd: (kind: ActionKind) => void }) {
  const [kind, setKind] = useState<ActionKind>("agent");
  return <details className="stage-advanced">
    <summary>Add another task</summary>
    <div className="stage-add-actions">
      <label className="field"><span>New task type</span><select value={kind} onChange={(event) => setKind(event.target.value as ActionKind)}>{Object.entries(actionLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
      <button type="button" onClick={() => onAdd(kind)}>Add task</button>
    </div>
  </details>;
}

function FactEditor({ facts, sources, onChange }: { facts: Fact[]; sources: Source[]; onChange: (facts: Fact[]) => void }) {
  function update(index: number, change: Partial<Fact>) {
    onChange(facts.map((fact, at) => at === index ? { ...fact, ...change } : fact));
  }
  return <details className="stage-facts" open>
    <summary>Values to check · {facts.length}</summary>
    <p className="muted">Name each value. Choose data supplied at the start or a result from an earlier step. If the source is missing, the run stops.</p>
    {facts.map((fact, index) => <div className="stage-fact" key={index}>
      <label className="field"><span>Value name</span><input value={fact.name} placeholder="test_count" onChange={(event) => update(index, { name: event.target.value })} /></label>
      <label className="field"><span>Source</span><select value={fact.from} onChange={(event) => {
        const source = sources.find((entry) => entry.from === event.target.value);
        update(index, { from: event.target.value, ...(source ? { type: ["string", "number", "boolean", "object", "array"].includes(source.type) ? source.type : "object" } : {}) });
      }}>
        <option value="">Choose a source</option>
        {fact.from && !sources.some((source) => source.from === fact.from) && <option value={fact.from}>{fact.from} · unavailable source</option>}
        {sources.map((source) => <option key={source.from} value={source.from}>{source.label}</option>)}
      </select></label>
      <label className="field"><span>Field in the result (optional)</span><input value={fact.property || ""} placeholder="data.test_count" onChange={(event) => update(index, { property: event.target.value || undefined })} /><small>Use dots for fields inside other fields. For example: <code>data.test_count</code>.</small></label>
      <label className="field"><span>Value type</span><select value={fact.type} onChange={(event) => update(index, { type: event.target.value })}>{["string", "number", "boolean", "object", "array"].map((type) => <option key={type} value={type}>{typeLabels[type]}</option>)}</select></label>
      <button type="button" className="small" aria-label={`Remove value ${fact.name || index + 1}`} onClick={() => onChange(facts.filter((_, at) => at !== index))}>Remove value</button>
    </div>)}
    <button type="button" className="small" onClick={() => onChange([...facts, { name: `value_${facts.length + 1}`, type: "string", from: "" }])}>Add value</button>
    <p className="muted">To check JSON from a task, enable “Read the task output as JSON” in that task’s advanced settings. Choose its result here. For a field named <code>test_count</code>, enter <code>data.test_count</code>.</p>
  </details>;
}

export function ActionEditor({ node, draft, workflows, onChange, onDraft, namedInputs = false }: {
  node: WorkflowNode; draft: ActionDraft; workflows: Workflow[]; namedInputs?: boolean;
  onChange: (node: WorkflowNode) => void; onDraft: (draft: ActionDraft) => void;
}) {
  const kind = node.kind as ActionKind;
  const repositoryAction = ["agent", "command", "tool", "validation"].includes(kind);
  const commandAction = ["command", "tool", "validation"].includes(kind);
  let config = node.config;
  let invalidConfig = "";
  if (draft.config !== undefined) {
    try { config = parseObject(draft.config, "Task settings"); }
    catch (cause) { invalidConfig = errorMessage(cause); }
  }
  function configure(key: string, value: unknown) {
    const next = { ...config, [key]: value };
    onChange({ ...node, config: next });
    onDraft({ ...draft, config: json(next), ...(key === "command" ? { argv: undefined } : {}) });
  }
  const text = (key: string) => typeof config[key] === "string" ? config[key] as string : "";
  return <div className="stack">
    {invalidConfig && <ErrorNotice message={`${invalidConfig} Correct the JSON in advanced settings first.`} />}
    <fieldset className="stage-config stack" disabled={!!invalidConfig}>
      <legend className="sr-only">{node.name} settings</legend>
      {kind === "agent" && <label className="field"><span>What must the AI do?</span><textarea rows={4} value={text("prompt")} placeholder="Describe the change. Say how to check that it works." onChange={(event) => configure("prompt", event.target.value)} /><small>Write the task instructions. The AI also follows the project’s instructions.</small></label>}
      {commandAction && <label className="field"><span>Command · put each argument on a separate line</span><textarea className="code-input" rows={4} spellCheck={false} value={draft.argv ?? (Array.isArray(config.command) ? config.command.join("\n") : "")} placeholder={"go\ntest\n./..."} onChange={(event) => {
        const argv = event.target.value;
        const next = { ...config, command: argv === "" ? [] : argv.split("\n") };
        onChange({ ...node, config: next });
        onDraft({ ...draft, argv, config: json(next) });
      }} /><small>Put the program on the first line. Put each argument on a new line. Spaces on a line stay together. For shell features, use a shell program such as <code>sh</code> with <code>-c</code>.</small></label>}
      {kind === "approval" && <label className="field"><span>What must the person review?</span><textarea rows={3} value={text("message")} placeholder="Review the changes and check results." onChange={(event) => configure("message", event.target.value)} /><small>The run waits here. Approval lets it continue. Rejection stops the run. It does not undo earlier changes.</small></label>}
      {kind === "integration" && <div className="stage-action-fields">
        <label className="field"><span>Web address (URL)</span><input type="url" value={text("url")} placeholder="https://example.com/api" onChange={(event) => configure("url", event.target.value)} /></label>
        <label className="field"><span>Method</span><select value={text("method") || "GET"} onChange={(event) => configure("method", event.target.value)}>{["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].map((method) => <option key={method}>{method}</option>)}{text("method") && !["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"].includes(text("method")) && <option>{text("method")}</option>}</select></label>
      </div>}
      {kind === "workflow" && <label className="field"><span>Workflow to run</span><select value={text("workflowId")} onChange={(event) => configure("workflowId", event.target.value)}><option value="">Choose a saved workflow</option>{text("workflowId") && !workflows.some((entry) => entry.id === text("workflowId")) && <option value={text("workflowId")}>{text("workflowId")} · unavailable</option>}{workflows.map((entry) => <option key={entry.id} value={entry.id}>{entry.name}</option>)}</select><small>This task waits for the other workflow to finish. It uses the same project folders. Task inputs supply its start data.</small></label>}
    </fieldset>
    <details className="stage-advanced"><summary>Advanced task settings</summary>
    <div className="stage-action-fields">
      <label className="field"><span>Task name</span><input value={node.name} onChange={(event) => onChange({ ...node, name: event.target.value })} /></label>
      <label className="field"><span>Task type</span><select value={kind} onChange={(event) => {
        const replacement = newAction(event.target.value as ActionKind);
        onChange({ ...replacement, id: node.id, name: node.name, repository: ["agent", "command", "tool", "validation"].includes(replacement.kind) ? node.repository || "primary" : undefined, inputs: node.inputs, outputType: replacement.kind === "approval" ? undefined : node.outputType });
        onDraft({ inputs: draft.inputs });
      }}>{Object.entries(actionLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></label>
    </div>
      {repositoryAction && <label className="field"><span>Project folder name</span><input value={node.repository || ""} placeholder="primary" onChange={(event) => onChange({ ...node, repository: event.target.value || undefined })} /><small>Keep <code>primary</code> for one folder. When you connect this workflow to a project, choose the repository for this name.</small></label>}
      {repositoryAction && <label className="stage-checkbox"><input type="checkbox" checked={config.outputJson === true} onChange={(event) => configure("outputJson", event.target.checked)} />Read the task output as JSON<small>The full standard output (stdout) must be one JSON object. Factory stores it in <code>result.data</code>. Invalid or incomplete JSON stops the task.</small></label>}
      {kind === "approval" ? <p className="muted">Result type: <code>Approval</code>. Factory sets this type.</p> : <label className="field"><span>Result type</span><select value={node.outputType || "object"} onChange={(event) => onChange({ ...node, outputType: event.target.value })}>{valueTypes.filter((type) => !["string", "number", "boolean", "array"].includes(type)).map((type) => <option key={type} value={type}>{typeLabels[type] || type}</option>)}</select></label>}
      {!namedInputs && <small className="stage-result">Result reference: <code>{node.id}.result</code></small>}
      {!namedInputs && <JsonField label="Task inputs (JSON)" value={draft.inputs ?? json(node.inputs || {})} rows={5} help={'Example: {"context":{"type":"string","from":"inputs.context"}}. To use an earlier task result, use its ID followed by .result.'} onChange={(value) => {
        onDraft({ ...draft, inputs: value });
        try { onChange({ ...node, inputs: parseObject<Input>(value, "Task inputs") }); } catch { /* Preserve incomplete JSON until it can be parsed. */ }
      }} />}
      <JsonField label="Task settings (JSON)" value={draft.config ?? json(node.config)} rows={5} help="Set time limits, saved file paths, or web request headers and body here. Invalid JSON prevents saving." onChange={(value) => {
        onDraft({ ...draft, config: value, argv: undefined });
        try { onChange({ ...node, config: parseObject(value, "Task settings") }); } catch { /* Preserve incomplete JSON and report it in schema/save validation. */ }
      }} />
    </details>
  </div>;
}

export function StageEditor({ workflow, workflows, onSaved, onGraph }: {
  workflow?: Workflow; workflows: Workflow[]; onSaved: (workflow: Workflow) => void; onGraph: () => void;
}) {
  const [name, setName] = useState(workflow?.name || "");
  const [inputRows, setInputRows] = useState(() => Object.entries(workflow?.inputs || {}).map(([key, type]) => ({ key, type })));
  const [stages, setStages] = useState<WorkflowStage[]>(() => workflow ? stagesFromWorkflow(workflow) || [] : initialStages());
  const [hasStageModel] = useState(() => !workflow || stages.length > 0);
  const [selectedId, setSelectedId] = useState(() => stages[0]?.id || "");
  const [drafts, setDrafts] = useState<Record<string, ActionDraft>>({});
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const selectedIndex = stages.findIndex((stage) => stage.id === selectedId);
  const stage = stages[selectedIndex];
  function edited() { setDirty(true); setMessage(""); }
  function updateStage(id: string, change: (stage: WorkflowStage) => WorkflowStage) {
    edited(); setStages((current) => current.map((entry) => entry.id === id ? change(entry) : entry));
  }
  function updateRule(id: string, change: (rule: StageRule) => StageRule) {
    updateStage(selectedId, (entry) => ({ ...entry, rules: entry.rules.map((rule) => rule.id === id ? change(rule) : rule) }));
  }
  useEffect(() => {
    if (!dirty) return;
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", prevent);
    return () => window.removeEventListener("beforeunload", prevent);
  }, [dirty]);
  function leaveGraph() {
    if (!dirty || window.confirm("Discard unsaved changes and open the saved connections?")) onGraph();
  }
  function build(): Workflow {
    if (!name.trim()) throw new Error("Enter a workflow name.");
    for (const row of inputRows) {
      if (!identifier.test(row.key)) throw new Error(`Start data name “${row.key || "empty"}” must use 1–128 letters, numbers, hyphens, or underscores.`);
      if (!valueTypes.some((type) => type === row.type)) throw new Error(`Select a supported type for ${row.key}.`);
    }
    const inputs = Object.fromEntries(inputRows.map(({ key, type }) => [key, type]));
    if (Object.keys(inputs).length !== inputRows.length) throw new Error("Use a different name for each start value.");
    const configured = stages.map((entry) => ({ ...entry, rules: entry.rules.map((rule) => ({ ...rule, actions: rule.actions.map((node) => {
      const draft = drafts[node.id];
      const inputs = draft?.inputs !== undefined ? parseObject<Input>(draft.inputs, `Inputs for ${node.name}`) : node.inputs;
      for (const [key, input] of Object.entries(inputs || {})) {
        if (!identifier.test(key) || !input || typeof input !== "object" || Array.isArray(input) || !valueTypes.some((type) => type === input.type)) throw new Error(`Task input ${key} for ${node.name} needs a valid name and type.`);
        if (input.from !== undefined && (typeof input.from !== "string" || !input.from)) throw new Error(`Enter a source for ${node.name}.${key}.`);
        if (input.from !== undefined && Object.hasOwn(input, "value")) throw new Error(`Input ${node.name}.${key} must use a source or a fixed value, not both.`);
      }
      return { ...node, inputs, config: draft?.config !== undefined ? parseObject(draft.config, `Settings for ${node.name}`) : node.config };
    }) })) }));
    return compileStages(name.trim(), inputs, configured, workflow?.id);
  }
  let schema: Workflow | undefined;
  let schemaError = "";
  try { schema = build(); } catch (cause) { schemaError = errorMessage(cause); }
  let preview = "";
  try { preview = policyPreview(stages); } catch (cause) { preview = `Preview unavailable: ${errorMessage(cause)}`; }
  const sources: Source[] = [
    ...inputRows.filter((row) => row.key).map((row) => ({ from: `inputs.${row.key}`, label: `Start data · ${row.key}`, type: row.type })),
    ...stages.slice(0, Math.max(0, selectedIndex)).flatMap((entry) => entry.rules.flatMap((rule) => rule.actions.map((action) => ({ from: `${action.id}.result`, label: `${entry.name} · ${action.name} result${rule.conditional ? " (can be skipped)" : ""}`, type: action.kind === "approval" ? "Approval" : action.outputType || "object" })))),
  ];
  async function save() {
    setError(""); setMessage(""); setSaving(true);
    try {
      const compiled = build();
      const saved = await post<Workflow>("/workflows", compiled);
      setDirty(false);
      try {
        const positions: Record<string, { x: number; y: number }> = {};
        let y = 70;
        stages.forEach((entry) => {
          positions[entry.id] = { x: 70, y };
          y += 150;
          entry.rules.forEach((rule) => {
            positions[rule.id] = { x: 70, y };
            y += 150;
            rule.actions.forEach((action, index) => {
              positions[action.id] = { x: rule.execution === "parallel" ? 360 + index * 280 : 360, y: rule.execution === "parallel" ? y : y + index * 150 };
            });
            y += Math.max(1, rule.execution === "parallel" ? 1 : rule.actions.length) * 150;
            positions[`${rule.id}_done`] = { x: 70, y };
            y += 170;
          });
        });
        await request(`/workflows/${encodeURIComponent(saved.id)}/layout`, { method: "PUT", body: JSON.stringify({ nodes: positions }) });
        setMessage(isPlayground ? "Workflow saved in this browser. This playground cannot run tasks." : "Workflow saved. Choose a project and connect it to this workflow before you start a run.");
      } catch (cause) { setError(`Workflow saved, but the connection positions were not saved: ${errorMessage(cause)}`); }
      onSaved(saved);
      if (!workflow) window.location.hash = href("workflows", saved.id);
    } catch (cause) { setError(errorMessage(cause)); }
    finally { setSaving(false); }
  }

  return <>
    <PageHeader title={workflow ? workflow.name : "New workflow"} description="A workflow is saved instructions for Factory. Each task tells Factory what to do. Tasks can ask AI to do work, run a command, or wait for approval.">
      <a className="button" href={href("workflows")} onClick={(event) => { if (dirty && !window.confirm("Discard unsaved changes?")) event.preventDefault(); }}>All workflows</a>
      <button type="button" onClick={leaveGraph}>Advanced: edit connections</button>
    </PageHeader>
    <ErrorNotice message={error} />
    {message && <div className="notice" role="status">{message}</div>}
    {workflow && !hasStageModel ? <section className="panel stack"><h2>This workflow has custom connections</h2><p>The simple editor cannot keep its run order. Use the connection editor to make changes.</p><button type="button" onClick={onGraph}>Edit connections</button></section> : <fieldset className="stage-write-area" disabled={saving}>
      <section className="panel stage-workflow-settings">
        <div className="stage-heading"><h2>1. Name this workflow</h2><span className={`stage-readiness ${schema ? "ready" : "draft"}`}>{schema ? "Ready to save" : "Needs details"}{dirty ? " · not saved" : ""}</span></div>
        <label className="field"><span>Workflow name</span><input value={name} placeholder="Update the help page" onChange={(event) => { edited(); setName(event.target.value); }} /></label>
        <details className="stage-inputs"><summary>Optional: data to supply when you start · {inputRows.length}</summary>
          <p className="muted">Skip this for a fixed task. Add values here only if they must change between runs. Choose their values when you connect the workflow to a project.</p>
          {inputRows.map((row, index) => <div className="stage-input-row" key={index}>
            <label className="field"><span>Start data name</span><input value={row.key} onChange={(event) => { edited(); setInputRows((current) => current.map((entry, at) => at === index ? { ...entry, key: event.target.value } : entry)); }} /></label>
            <label className="field"><span>Value type</span><select value={row.type} onChange={(event) => { edited(); setInputRows((current) => current.map((entry, at) => at === index ? { ...entry, type: event.target.value } : entry)); }}>{valueTypes.map((type) => <option key={type} value={type}>{typeLabels[type] || type}</option>)}</select></label>
            <button type="button" className="small" aria-label={`Remove input ${row.key || index + 1}`} onClick={() => { edited(); setInputRows((current) => current.filter((_, at) => at !== index)); }}>Remove input</button>
          </div>)}
          <button type="button" className="small" onClick={() => { edited(); setInputRows((current) => [...current, { key: "", type: "string" }]); }}>Add start data</button>
        </details>
        <p className="stage-scope-note">Tasks run in the project folder you select. They can change files there. Approval does not undo changes. Saving does not start a run or publish a pull request.</p>
        <details className="stage-advanced"><summary>About GitHub work</summary><p className="muted">GitHub issue and review jobs have separate permission checks. Step names do not create separate working folders, retry failures, or merge changes. This page sets only the tasks shown below.</p></details>
      </section>
      <div className="stage-setup-heading"><h2>2. Set the tasks</h2><p className="muted">{workflow ? "Select a step to edit its tasks. Add more tasks only if you need them." : "Start with one AI task and a review. Write the AI instructions below. Add more tasks only if you need them."}</p></div>
      <div className="stage-editor">
        <aside className="panel stage-overview" aria-label="Workflow steps">
          <h2>Steps <span className="muted">{stages.length}</span></h2>
          <ol>{stages.map((entry, index) => <li key={entry.id} className={entry.id === selectedId ? "selected" : ""}>
            <button type="button" className="stage-select" aria-current={entry.id === selectedId ? "step" : undefined} onClick={() => setSelectedId(entry.id)}>
              <span className="stage-number">{index + 1}</span><span><strong>{entry.name || "Unnamed step"}</strong><small>{entry.rules.reduce((count, rule) => count + rule.actions.length, 0)} tasks{entry.rules.some((rule) => rule.conditional) ? " · has conditions" : ""}{entry.rules.some((rule) => rule.execution === "parallel") ? " · some run together" : ""}</small><small>{entry.rules.flatMap((rule) => rule.actions.map((action) => action.name)).join(" → ") || "No tasks yet"}</small></span>
            </button>
            <MoveButtons label={`step ${entry.name}`} index={index} count={stages.length} onMove={(to) => { edited(); setStages((current) => reordered(current, index, to)); }} onRemove={() => {
              if (!window.confirm(`Remove step “${entry.name}” and all its tasks?`)) return;
              edited(); setStages((current) => current.filter((item) => item.id !== entry.id));
              if (selectedId === entry.id) setSelectedId(stages[index + 1]?.id || stages[index - 1]?.id || "");
            }} />
          </li>)}</ol>
          <button type="button" onClick={() => { const added = newStage(`Step ${stages.length + 1}`); edited(); setStages((current) => [...current, added]); setSelectedId(added.id); }}>Add step</button>
        </aside>
        <section className="panel stage-detail" aria-label="Selected step">
          {stage ? <>
            <div className="stage-heading"><h2>Step {selectedIndex + 1}</h2></div>
            <label className="field"><span>Step name</span><input value={stage.name} onChange={(event) => updateStage(stage.id, (entry) => ({ ...entry, name: event.target.value }))} /></label>
            <p className="muted">{selectedIndex === 0 ? "This step starts when you start a run." : `This step starts after “${stages[selectedIndex - 1].name || "the previous step"}” finishes.`} Tasks run one at a time unless you select “Together”. If a task fails, later tasks do not start.</p>
            {stage.rules.map((rule, ruleIndex) => <article className="stage-rule-card" key={rule.id}>
              <div className="stage-heading"><h3>{stage.rules.length === 1 ? "Tasks" : `Task group ${ruleIndex + 1}: ${rule.name}`}</h3></div>
              <details className="stage-advanced" open={rule.conditional || rule.execution === "parallel"}>
                <summary>Optional: conditions and task order{rule.conditional ? " · conditions on" : ""}{rule.execution === "parallel" ? " · tasks run together" : ""}</summary>
                <label className="field"><span>Task group name</span><input value={rule.name} onChange={(event) => updateRule(rule.id, (entry) => ({ ...entry, name: event.target.value }))} /></label>
                <MoveButtons label={`task group ${rule.name}`} index={ruleIndex} count={stage.rules.length} onMove={(to) => updateStage(stage.id, (entry) => ({ ...entry, rules: reordered(entry.rules, ruleIndex, to) }))} onRemove={() => updateStage(stage.id, (entry) => ({ ...entry, rules: entry.rules.filter((item) => item.id !== rule.id) }))} />
                <div className="stage-when"><label className="stage-checkbox"><input type="checkbox" checked={rule.conditional} onChange={(event) => updateRule(rule.id, (entry) => ({ ...entry, conditional: event.target.checked }))} />Run this group only if conditions match</label></div>
              {rule.conditional ? <div className="stage-conditions">
                <FactEditor facts={rule.facts} sources={sources} onChange={(facts) => updateRule(rule.id, (entry) => ({ ...entry, facts }))} />
                {rule.facts.length ? <QueryBuilder
                  fields={rule.facts.map((fact) => ({ name: fact.name, label: fact.name || "Unnamed value", inputType: "text", valueEditorType: fact.type === "boolean" ? "select" as const : fact.type === "string" || fact.type === "number" ? "text" as const : "textarea" as const, values: fact.type === "boolean" ? [{ name: "true", label: "true" }, { name: "false", label: "false" }] : undefined }))}
                  query={rule.condition}
                  onQueryChange={(condition: RuleGroupType) => updateRule(rule.id, (entry) => ({ ...entry, condition }))}
                  operators={operators}
                  getOperators={(field) => rule.facts.find((fact) => fact.name === field)?.type === "number" ? operators : operators.slice(0, 4)}
                  combinators={[{ name: "and", label: "Match all conditions" }, { name: "or", label: "Match any condition" }]}
                  autoSelectField={false}
                  autoSelectOperator={false}
                  resetOnFieldChange
                  resetOnOperatorChange
                  showNotToggle={false}
                  showShiftActions
                  translations={{ addRule: { label: "+ Condition", title: "Add a condition" }, addGroup: { label: "+ Condition group", title: "Add a condition group" } }}
                  controlClassnames={{ queryBuilder: "stage-query-builder", addRule: "small", addGroup: "small", removeRule: "small", removeGroup: "small" }}
                /> : <p className="muted">Add a value to check. Then add a condition.</p>}
                <small className="muted">Text must match exactly. Use JSON for objects and lists. You can put condition groups inside other groups.</small>
                <p className="muted">If conditions do not match, Factory skips this group. It then tries the next group or step. If a data source is missing, the run stops.</p>
              </div> : <p className="muted">This group always runs. Conditions stay saved when you turn them off.</p>}
              <div className="stage-then"><label className="field"><span>Task order</span><select value={rule.execution} onChange={(event) => updateRule(rule.id, (entry) => ({ ...entry, execution: event.target.value as StageRule["execution"] }))}><option value="sequence">One at a time</option><option value="parallel">Together · wait for all</option></select></label></div>
              {rule.execution === "parallel" && <p className="muted">Factory can start these tasks together, within the project’s limit. It waits until all succeed. These tasks cannot use each other’s results. If one fails, later tasks do not start.</p>}
              </details>
              {rule.actions.map((action, actionIndex) => <section className="stage-action-card" key={action.id}>
                <div className="stage-heading"><h4>{actionIndex + 1}. {actionLabels[action.kind as ActionKind] || action.kind}</h4><MoveButtons label={`task ${action.name}`} index={actionIndex} count={rule.actions.length} onMove={(to) => updateRule(rule.id, (entry) => ({ ...entry, actions: reordered(entry.actions, actionIndex, to) }))} onRemove={() => updateRule(rule.id, (entry) => ({ ...entry, actions: entry.actions.filter((item) => item.id !== action.id) }))} /></div>
                <ActionEditor node={action} draft={drafts[action.id] || {}} workflows={workflows.filter((entry) => entry.id !== workflow?.id)} onChange={(node) => updateRule(rule.id, (entry) => ({ ...entry, actions: entry.actions.map((item) => item.id === node.id ? node : item) }))} onDraft={(draft) => { edited(); setDrafts((current) => ({ ...current, [action.id]: draft })); }} />
              </section>)}
              {!rule.actions.length && <p className="stage-draft-note">Add a task and fill in its details before you save.</p>}
              <AddTask onAdd={(kind) => updateRule(rule.id, (entry) => ({ ...entry, actions: [...entry.actions, newAction(kind)] }))} />
            </article>)}
            {!stage.rules.length && <p className="stage-draft-note">Add a task group to this step.</p>}
            <details className="stage-advanced"><summary>Add a task group</summary><p className="muted">Use a separate group if some tasks need different conditions. Factory tries each group in order, not just the first match.</p><button type="button" onClick={() => updateStage(stage.id, (entry) => ({ ...entry, rules: [...entry.rules, newRule()] }))}>Add group</button></details>
          </> : <div className="empty">Add a step to start.</div>}
        </section>
      </div>
      <section className="panel stage-previews">
        <div className="stage-heading"><div><h2>3. Save and choose a project</h2><p className="muted">Saving keeps these instructions. It does not run them.</p></div><button type="button" className="primary" disabled={saving || !schema} onClick={save}>{saving ? "Saving…" : "Save workflow"}</button></div>
        {!schema && <p className="stage-draft-note" role="status">{schemaError}</p>}
        <details className="stage-schema"><summary>Read the run order</summary><pre className="json-output stage-policy">{preview}</pre></details>
        <details className="stage-schema"><summary>Advanced: workflow JSON</summary>{schema ? <pre className="json-output">{json(schema)}</pre> : <ErrorNotice message={schemaError} />}</details>
        {isPlayground ? <p className="muted">This playground saves in this browser only. It cannot run tasks. To run a workflow, open Factory with the local server running.</p> : <p className="muted">Next: open <a href={href("projects")}>Projects</a>. Choose a project. Under “Workflow bindings”, select “New binding”. Choose this workflow and its repository. Select “Save binding &amp; start run” when you are ready. A binding connects saved instructions to a project folder.</p>}
      </section>
    </fieldset>}
  </>;
}
