import { useEffect, useState } from "react";
import { errorMessage, href, isPlayground, json, parseObject, post } from "./api";
import { ErrorNotice, PageHeader } from "./components";
import { actionKinds, comparisons, conditionToUntil, declarationPreview, identifier, untilToCondition, validateCondition, validateMappings } from "./declarative";
import { ActionEditor, newAction } from "./workflowStageEditor";
import type { ActionDraft, ActionKind } from "./workflowStageEditor";
import { valueTypes } from "./types";
import type { Condition, Declaration, DeclarationStep, Input, Workflow, WorkflowNode } from "./types";

type Source = { name: string; type: string };
const freshCondition = (source = ""): Condition => ({ path: source, operator: "equals", value: "" });
const outputType = (node: WorkflowNode) => node.kind === "approval" ? "Approval" : node.outputType || "object";
function move<T>(rows: T[], index: number, direction: number): T[] {
  const result = [...rows];
  const target = index + direction;
  if (target < 0 || target >= rows.length) return rows;
  [result[index], result[target]] = [result[target], result[index]];
  return result;
}

function LiteralField({ value, type, onChange }: { value: unknown; type: string; onChange: (value: unknown) => void }) {
  const rendered = type === "string" ? String(value ?? "") : JSON.stringify(value) ?? "";
  const [text, setText] = useState(rendered);
  useEffect(() => setText(rendered), [rendered]);
  if (type === "boolean") return <select value={String(value ?? false)} onChange={(event) => onChange(event.target.value === "true")}><option value="true">true</option><option value="false">false</option></select>;
  return <input key={type} value={text} type={type === "number" ? "number" : "text"} step={type === "number" ? "any" : undefined} required={type !== "string"} onChange={(event) => {
    const raw = event.target.value;
    setText(raw);
    try {
      const parsed: unknown = type === "string" ? raw : JSON.parse(raw);
      if (type === "number" && typeof parsed !== "number") throw new Error("Enter a number.");
      if (type === "array" && !Array.isArray(parsed)) throw new Error("Enter a JSON list.");
      if (!["string", "number", "boolean", "array"].includes(type) && (!parsed || typeof parsed !== "object" || Array.isArray(parsed))) throw new Error("Enter a JSON object.");
      event.target.setCustomValidity("");
      onChange(parsed);
    } catch { event.target.setCustomValidity("Enter a valid value of the selected type."); }
  }} />;
}

function ConditionEditor({ condition, sources, onChange, label }: { condition: Condition; sources: Source[]; onChange: (condition: Condition) => void; label: string }) {
  const group = "all" in condition ? "all" : "any" in condition ? "any" : "comparison";
  const children = "all" in condition ? condition.all : "any" in condition ? condition.any : [];
  function groupChange(next: Condition[]) { onChange(group === "all" ? { all: next } : { any: next }); }
  return <fieldset className="declaration-condition stack"><legend>{label}</legend>
    <label className="field"><span>Match</span><select value={group} onChange={(event) => {
      const next = event.target.value;
      onChange(next === "comparison" ? children[0] || freshCondition(sources[0]?.name) : next === "all" ? { all: group === "comparison" ? [condition] : children } : { any: group === "comparison" ? [condition] : children });
    }}><option value="comparison">A comparison</option><option value="all">All conditions (and)</option><option value="any">Any condition (or)</option></select></label>
    {"path" in condition ? <div className="stage-action-fields">
      <label className="field"><span>Value path</span><input required value={condition.path} placeholder="WorkItem.labels or Review.actionableFindings" onChange={(event) => onChange({ ...condition, path: event.target.value })} /><small>Available names: {sources.map((source) => source.name).join(", ") || "add start data first"}. Use dots for fields. JSON action results expose fields directly through their alias.</small></label>
      <label className="field"><span>Comparison</span><select value={condition.operator} onChange={(event) => {
        const operator = event.target.value;
        const { value, ...comparison } = condition;
        onChange({ ...comparison, operator, ...(!["exists", "notExists"].includes(operator) ? { value: value ?? "" } : {}) });
      }}>{comparisons.map(([key, title]) => <option key={key} value={key}>{title}</option>)}</select></label>
      {!["exists", "notExists"].includes(condition.operator) && <>
        <label className="field"><span>Comparison value type</span><select value={typeof condition.value === "number" ? "number" : typeof condition.value === "boolean" ? "boolean" : typeof condition.value === "object" ? Array.isArray(condition.value) ? "array" : "object" : "string"} onChange={(event) => onChange({ ...condition, value: event.target.value === "number" ? 0 : event.target.value === "boolean" ? false : event.target.value === "object" ? {} : event.target.value === "array" ? [] : "" })}>{["string", "number", "boolean", "object", "array"].map((type) => <option key={type}>{type}</option>)}</select></label>
        <label className="field"><span>Compared with</span><LiteralField value={condition.value} type={typeof condition.value === "number" ? "number" : typeof condition.value === "boolean" ? "boolean" : typeof condition.value === "object" ? Array.isArray(condition.value) ? "array" : "object" : "string"} onChange={(value) => onChange({ ...condition, value })} /></label>
      </>}
    </div> : <>
      {children.map((child, index) => <div className="stack" key={index}><ConditionEditor condition={child} sources={sources} label={`Condition ${index + 1}`} onChange={(next) => groupChange(children.map((entry, at) => at === index ? next : entry))} /><button type="button" className="small" onClick={() => groupChange(children.filter((_, at) => at !== index))}>Remove condition {index + 1}</button></div>)}
      <button type="button" onClick={() => groupChange([...children, freshCondition(sources[0]?.name)])}>Add condition</button>
    </>}
  </fieldset>;
}

function InputMappings({ inputs, sources, onChange }: { inputs: Record<string, Input>; sources: Source[]; onChange: (inputs: Record<string, Input>) => void }) {
  const rows = Object.entries(inputs);
  function update(index: number, name: string, input: Input) {
    if (rows.some(([key], at) => at !== index && key === name)) return;
    onChange(Object.fromEntries(rows.map((entry, at) => at === index ? [name, input] : entry)));
  }
  return <div className="stack">{rows.map(([name, input], index) => <div className="stage-action-fields" key={index}>
    <label className="field"><span>Input name</span><input required pattern="[A-Za-z0-9_-]{1,128}" value={name} onChange={(event) => update(index, event.target.value, input)} /></label>
    <label className="field"><span>Input type</span><select value={input.type} onChange={(event) => update(index, name, { type: event.target.value, ...(input.from ? { from: input.from } : { value: event.target.value === "string" ? "" : event.target.value === "number" ? 0 : event.target.value === "boolean" ? false : event.target.value === "array" ? [] : {} }) })}>{valueTypes.map((type) => <option key={type}>{type}</option>)}</select></label>
    <label className="field"><span>Input source</span><select value={input.from === undefined ? "@literal" : input.from} onChange={(event) => {
      const source = sources.find((entry) => entry.name === event.target.value);
      update(index, name, source ? { type: source.type, from: source.name } : { type: input.type, value: input.type === "string" ? "" : input.type === "number" ? 0 : input.type === "boolean" ? false : input.type === "array" ? [] : {} });
    }}><option value="@literal">Fixed value</option>{input.from && !sources.some((source) => source.name === input.from) && <option value={input.from}>{input.from} · unavailable</option>}{sources.map((source) => <option key={source.name} value={source.name}>{source.name} · {source.type}</option>)}</select></label>
    {input.from === undefined && <label className="field"><span>Literal value</span><LiteralField type={input.type} value={input.value} onChange={(value) => update(index, name, { type: input.type, value })} /></label>}
    <button type="button" className="small" onClick={() => onChange(Object.fromEntries(rows.filter((_, at) => at !== index)))}>Remove input {name}</button>
  </div>)}<button type="button" onClick={() => { let name = `input_${rows.length + 1}`; while (Object.hasOwn(inputs, name)) name += "_"; onChange({ ...inputs, [name]: { type: "string", value: "" } }); }}>Add input mapping</button></div>;
}

const guardExamples: Record<string, Condition> = {
  "Bug routing": { all: [{ path: "WorkItem.kind", operator: "equals", value: "Issue" }, { path: "WorkItem.labels", operator: "contains", value: "Bug" }] },
  "Small bug": { all: [{ path: "WorkItem.kind", operator: "equals", value: "SmolBug" }, { path: "WorkItem.state", operator: "equals", value: "Open" }, { path: "WorkItem.prExists", operator: "equals", value: false }] },
  "Full bug": { all: [{ path: "WorkItem.kind", operator: "equals", value: "NormalBug" }, { path: "WorkItem.state", operator: "equals", value: "Open" }] },
  "PR review": { all: [{ path: "WorkItem.kind", operator: "equals", value: "PR" }, { path: "WorkItem.labels", operator: "contains", value: "agent/workflow:review" }] },
  "Feedback only": { all: [{ path: "WorkItem.kind", operator: "equals", value: "PR" }, { path: "WorkItem.labels", operator: "contains", value: "agent/workflow:feedback" }] },
  "Issue readiness": { all: [{ path: "WorkItem.kind", operator: "equals", value: "Issue" }, { path: "WorkItem.labels", operator: "contains", value: "agent/workflow:review" }] },
  "Dependency PR": { all: [{ path: "WorkItem.kind", operator: "equals", value: "PR" }, { any: [{ path: "WorkItem.author", operator: "equals", value: "dependabot[bot]" }, { path: "WorkItem.author", operator: "equals", value: "renovate[bot]" }] }] },
  "Delivery": { all: [{ path: "WorkItem.kind", operator: "equals", value: "PR" }, { path: "WorkItem.approved", operator: "equals", value: true }, { path: "WorkItem.labels", operator: "contains", value: "agent/status:complete" }, { path: "WorkItem.classification", operator: "equals", value: "Safe" }] },
};

export function DeclarationEditor({ workflow, workflows, onSaved }: { workflow?: Workflow; workflows: Workflow[]; onSaved: (workflow: Workflow) => void }) {
  const [name, setName] = useState(workflow?.name || "");
  const [inputRows, setInputRows] = useState(() => Object.entries(workflow?.inputs || {}).map(([key, type]) => ({ key, type })));
  const [declaration, setDeclaration] = useState<Declaration>(() => structuredClone(workflow?.declaration || { version: 1, defaults: {}, actions: { Agent: { ...newAction("agent"), id: "Agent", name: "Agent" } }, steps: [{ id: "step_1", run: "Agent", as: "Result" }] }));
  const [drafts, setDrafts] = useState<Record<string, ActionDraft>>({});
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const [newKind, setNewKind] = useState<ActionKind>("agent");
  function edited() { setDirty(true); setMessage(""); }
  function change(next: Declaration) { edited(); setDeclaration(next); }
  function changeAction(key: string, action: WorkflowNode) { change({ ...declaration, actions: { ...declaration.actions, [key]: action } }); }
  function configure(key: string, config: Record<string, unknown>) {
    changeAction(key, { ...declaration.actions[key], config });
    setDrafts((current) => ({ ...current, [key]: { ...current[key], config: json(config) } }));
  }
  function changeStep(index: number, step: DeclarationStep) { change({ ...declaration, steps: declaration.steps.map((entry, at) => at === index ? step : entry) }); }
  function sourcesBefore(index: number): Source[] {
    return [...inputRows.filter((row) => row.key).map((row) => ({ name: row.key, type: row.type })), ...declaration.steps.slice(0, index).flatMap((step) => step.as && declaration.actions[step.run] ? [{ name: step.as, type: outputType(declaration.actions[step.run]) }] : [])];
  }
  useEffect(() => {
    if (!dirty) return;
    const prevent = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ""; };
    window.addEventListener("beforeunload", prevent);
    return () => window.removeEventListener("beforeunload", prevent);
  }, [dirty]);
  function build(): Workflow {
    if (!name.trim()) throw new Error("Enter a workflow name.");
    const inputs = Object.fromEntries(inputRows.map((row) => [row.key, row.type]));
    if (Object.keys(inputs).length !== inputRows.length || inputRows.some((row) => !identifier.test(row.key))) throw new Error("Use unique start data names with 1–128 letters, numbers, hyphens, or underscores.");
    const actions = Object.fromEntries(Object.entries(declaration.actions).map(([key, action]) => {
      if (!identifier.test(key)) throw new Error(`Action name “${key}” is invalid.`);
      const config = drafts[key]?.config === undefined ? action.config : parseObject(drafts[key].config!, `Settings for ${key}`);
      if (!actionKinds.some((kind) => kind === action.kind)) throw new Error(`Choose a supported action type for ${key}.`);
      if (action.kind === "agent" && (typeof config.prompt !== "string" || !config.prompt.trim())) throw new Error(`Write task instructions for ${key}.`);
      if (["command", "tool", "validation"].includes(action.kind) && (!Array.isArray(config.command) || !config.command.length || config.command.some((arg) => typeof arg !== "string") || !config.command[0])) throw new Error(`Enter the program and arguments for ${key}.`);
      if (action.kind === "workflow" && (!config.workflowId || config.workflowId === workflow?.id || !workflows.some((entry) => entry.id === config.workflowId))) throw new Error(`Choose another saved workflow for ${key}.`);
      if (action.kind === "integration" && (typeof config.url !== "string" || !/^https?:\/\//.test(config.url))) throw new Error(`Enter an HTTP or HTTPS URL for ${key}.`);
      for (const settings of [declaration.defaults || {}, config]) {
        if (settings.maxIterations !== undefined && (typeof settings.maxIterations !== "number" || !Number.isInteger(settings.maxIterations) || settings.maxIterations < 1 || settings.maxIterations > 20)) throw new Error("Maximum iterations must be an integer from 1 to 20.");
        if (settings.timeoutSeconds !== undefined && (typeof settings.timeoutSeconds !== "number" || !Number.isInteger(settings.timeoutSeconds) || settings.timeoutSeconds < 1)) throw new Error("Timeout must be a positive whole number of seconds.");
      }
      if (config.until !== undefined) {
        const until = untilToCondition(config.until);
        if (!until) throw new Error(`Correct the until condition for ${key}.`);
        if (action.kind !== "agent" || config.outputJson !== true) throw new Error(`Semantic completion for ${key} requires an agent with JSON output enabled.`);
        validateCondition(until, new Set(["result"]), `Until ${key}`);
      }
      return [key, { ...action, id: key, config }];
    }));
    if (!declaration.steps.length) throw new Error("Add at least one Then step.");
    const sources = new Map(Object.entries(inputs));
    if (declaration.given) validateCondition(declaration.given, new Set(sources.keys()), "Given");
    const ids = new Set<string>();
    for (const step of declaration.steps) {
      if (!identifier.test(step.id) || ids.has(step.id)) throw new Error("Step IDs must be valid and unique.");
      ids.add(step.id);
      const action = actions[step.run];
      if (!action) throw new Error(`Choose a named action for ${step.id}.`);
      if (step.if) validateCondition(step.if, new Set(sources.keys()), `If ${step.id}`);
      const mappings = step.inputs ?? action.inputs;
      validateMappings(mappings, sources, step.id);
      if (action.kind === "workflow") {
        const child = workflows.find((entry) => entry.id === action.config.workflowId);
        for (const [key, type] of Object.entries(child?.inputs || {})) {
          const input = mappings === undefined ? inputs[key] ? { type: inputs[key], from: key } : undefined : mappings[key];
          if (!input || input.type !== type) throw new Error(`${step.id}: map the child workflow input ${key} (${type}).`);
        }
      }
      if (step.as) {
        if (!identifier.test(step.as) || sources.has(step.as)) throw new Error(`Result name “${step.as}” must be valid, unique, and not shadow start data.`);
        sources.set(step.as, outputType(action));
      }
    }
    return { id: workflow?.id || "", name: name.trim(), inputs, nodes: [], edges: [], declaration: { ...declaration, actions } };
  }
  async function save() {
    setError(""); setMessage(""); setSaving(true);
    try {
      const saved = await post<Workflow>("/workflows", build());
      setDirty(false); setMessage(isPlayground ? "Workflow saved in this browser. Execution is unavailable in the playground." : "Workflow saved.");
      onSaved(saved);
      if (!workflow) window.location.hash = href("workflows", saved.id);
    } catch (cause) { setError(errorMessage(cause)); }
    finally { setSaving(false); }
  }
  let preview = "";
  try { preview = declarationPreview(declaration); } catch { preview = "Correct conditions to show the policy preview."; }
  return <>
    <PageHeader title={workflow ? workflow.name : "New workflow"} description="Given start data, evaluate each If in order, then run configured actions and name their results."><a className="button" href={href("workflows")} onClick={(event) => { if (dirty && !window.confirm("Discard unsaved changes?")) event.preventDefault(); }}>All workflows</a></PageHeader>
    <div className="notice">Engine authoring only — these policies do not subscribe to GitHub events or grant authorization. Configure real agents, commands, HTTP requests, approvals, or saved child workflows. Names such as MergePR do not implement merging or safety checks. Existing managed intake and publication boundaries are unchanged.{isPlayground && " This UI-only playground saves locally and does not execute actions."}</div>
    <form className="stack declaration-editor" onSubmit={(event) => { event.preventDefault(); void save(); }}>
      <fieldset className="stage-config stack" disabled={saving}>
      <ErrorNotice message={error} />{message && <div className="notice" role="status">{message}</div>}
      <section className="panel stack"><h2>Declarative workflow</h2>
        <label className="field"><span>Workflow name</span><input required value={name} onChange={(event) => { edited(); setName(event.target.value); }} /></label>
        <h3>Start data</h3><p className="muted">Typed workflow inputs are the named values available to Given and every step. Supply them when starting the workflow.</p>
        {inputRows.map((row, index) => <div className="stage-action-fields" key={index}><label className="field"><span>Start data name</span><input required pattern="[A-Za-z0-9_-]{1,128}" value={row.key} onChange={(event) => { edited(); setInputRows(inputRows.map((entry, at) => at === index ? { ...entry, key: event.target.value } : entry)); }} /></label><label className="field"><span>Start data type</span><select value={row.type} onChange={(event) => { edited(); setInputRows(inputRows.map((entry, at) => at === index ? { ...entry, type: event.target.value } : entry)); }}>{valueTypes.map((type) => <option key={type}>{type}</option>)}</select></label><button type="button" onClick={() => { edited(); setInputRows(inputRows.filter((_, at) => at !== index)); }}>Remove start data {row.key}</button></div>)}
        <button type="button" onClick={() => { edited(); setInputRows([...inputRows, { key: "", type: "object" }]); }}>Add start data</button>
        <h3>Workflow defaults</h3><div className="stage-action-fields">
          <label className="field"><span>Default model</span><input value={declaration.defaults?.model || ""} placeholder="Use project harness model" onChange={(event) => change({ ...declaration, defaults: { ...declaration.defaults, model: event.target.value || undefined } })} /></label>
          <label className="field"><span>Default timeout (seconds)</span><input type="number" min={1} step={1} value={declaration.defaults?.timeoutSeconds ?? ""} placeholder="3600" onChange={(event) => change({ ...declaration, defaults: { ...declaration.defaults, timeoutSeconds: event.target.value === "" ? undefined : Number(event.target.value) } })} /></label>
          <label className="field"><span>Default maximum iterations</span><input type="number" min={1} max={20} step={1} value={declaration.defaults?.maxIterations ?? ""} placeholder="3" onChange={(event) => change({ ...declaration, defaults: { ...declaration.defaults, maxIterations: event.target.value === "" ? undefined : Number(event.target.value) } })} /></label>
        </div><small>Model and iterations apply to agents. Timeout also applies to process and HTTP actions. Without an Until condition an agent runs once, regardless of maximum iterations.</small>
      </section>
      <section className="panel stack"><h2>Given</h2>
        <label className="field"><span>Example guard</span><select value="" onChange={(event) => {
          const given = guardExamples[event.target.value];
          if (!given || (declaration.given && !window.confirm("Replace the current Given condition with this example guard?"))) return;
          if (!inputRows.some((row) => row.key === "WorkItem")) setInputRows([...inputRows, { key: "WorkItem", type: "object" }]);
          change({ ...declaration, given: structuredClone(given) });
        }}><option value="">Choose a condition example (not an automation)</option>{Object.keys(guardExamples).map((key) => <option key={key}>{key}</option>)}</select><small>Only supplies a Given condition and WorkItem input. Then actions remain your real configured actions; no simulated GitHub operations are added.</small></label>
        <label className="stage-checkbox"><input type="checkbox" checked={!!declaration.given} onChange={(event) => change({ ...declaration, given: event.target.checked ? freshCondition(inputRows[0]?.key) : undefined })} />Only run when Given matches</label>
        {declaration.given && <ConditionEditor label="Given conditions" condition={declaration.given} sources={sourcesBefore(0)} onChange={(given) => change({ ...declaration, given })} />}
        <p className="muted">All/any groups can be nested. “contains” checks exact label membership in a list (or a substring in text). Missing result sources fail rather than pretending a skipped action produced a result.</p>
      </section>
      <section className="panel stack"><h2>Named actions</h2><p className="muted">Define each action once, then select it in one or more Then steps. Renaming an action updates its steps.</p>
        {Object.entries(declaration.actions).map(([key, action]) => {
          const draft = drafts[key] || {};
          const until = untilToCondition(action.config.until);
          const invalidDraft = (() => { try { if (draft.config !== undefined) parseObject(draft.config, "Settings"); return false; } catch { return true; } })();
          return <details className="declaration-action" key={key} open><summary>{key} · {action.kind}</summary>
            <label className="field"><span>Action name</span><input required pattern="[A-Za-z0-9_-]{1,128}" defaultValue={key} onBlur={(event) => {
              const next = event.target.value;
              if (next === key) return;
              if (!identifier.test(next) || Object.hasOwn(declaration.actions, next)) { event.target.value = key; setError("Action names must be valid and unique."); return; }
              const actions = Object.fromEntries(Object.entries(declaration.actions).map(([name, entry]) => name === key ? [next, { ...entry, id: next, name: entry.name === key ? next : entry.name }] : [name, entry]));
              const nextDrafts = { ...drafts, [next]: drafts[key] }; delete nextDrafts[key]; setDrafts(nextDrafts);
              change({ ...declaration, actions, steps: declaration.steps.map((step) => step.run === key ? { ...step, run: next } : step) });
            }} /></label>
            <ActionEditor node={action} draft={draft} workflows={workflows.filter((entry) => entry.id !== workflow?.id)} namedInputs onChange={(next) => changeAction(key, next)} onDraft={(next) => { edited(); setDrafts((current) => ({ ...current, [key]: next })); }} />
            <fieldset className="stack stage-config" disabled={invalidDraft}><legend>Action overrides</legend>
              {action.kind === "agent" && <label className="field"><span>Model override</span><input value={typeof action.config.model === "string" ? action.config.model : ""} placeholder="Use workflow default" onChange={(event) => configure(key, { ...action.config, model: event.target.value || undefined })} /></label>}
              {["agent", "command", "tool", "validation", "integration"].includes(action.kind) && <label className="field"><span>Timeout override (seconds)</span><input type="number" min={1} step={1} placeholder="Use workflow default" value={typeof action.config.timeoutSeconds === "number" ? action.config.timeoutSeconds : ""} onChange={(event) => configure(key, { ...action.config, timeoutSeconds: event.target.value === "" ? undefined : Number(event.target.value) })} /></label>}
              {action.kind === "agent" && <>
                <label className="field"><span>Maximum iterations override</span><input type="number" min={1} max={20} step={1} placeholder="Use workflow default" value={typeof action.config.maxIterations === "number" ? action.config.maxIterations : ""} onChange={(event) => configure(key, { ...action.config, maxIterations: event.target.value === "" ? undefined : Number(event.target.value) })} /></label>
                <label className="stage-checkbox"><input type="checkbox" checked={action.config.until !== undefined} onChange={(event) => configure(key, { ...action.config, until: event.target.checked ? conditionToUntil({ path: "result.data.status", operator: "equals", value: "Complete" }) : undefined, ...(event.target.checked ? { outputJson: true } : {}) })} />Repeat until semantic completion</label>
                {until && <ConditionEditor label="Until conditions" condition={until} sources={[{ name: "result", type: "object" }]} onChange={(next) => configure(key, { ...action.config, until: conditionToUntil(next) })} />}
                {action.config.until !== undefined && !until && <ErrorNotice message="This Until configuration needs correction in Task settings (JSON) before structured editing." />}
                <small>Until reads the current result envelope: use result.data.status for structured output. Each successful pass must return a JSON object; the next pass receives previousResult. Factory adds JSON-output instructions automatically. Errors stop immediately. Timeout covers the entire action, including all passes. Reaching the iteration limit without completion fails and blocks downstream actions. Only final-pass artifacts are collected.</small>
              </>}
            </fieldset>
            <details className="stage-advanced"><summary>Default action inputs</summary><p className="muted">Omitted inputs forward workflow start data. Step mappings override these defaults. Earlier aliases must exist at every step using this action.</p><label className="stage-checkbox"><input type="checkbox" checked={action.inputs !== undefined} onChange={(event) => changeAction(key, { ...action, inputs: event.target.checked ? {} : undefined })} />Customize default inputs</label>{action.inputs !== undefined && <InputMappings inputs={action.inputs} sources={sourcesBefore(declaration.steps.length)} onChange={(inputs) => changeAction(key, { ...action, inputs })} />}</details>
            <button type="button" className="small" disabled={declaration.steps.some((step) => step.run === key)} onClick={() => { const actions = { ...declaration.actions }; delete actions[key]; change({ ...declaration, actions }); }}>Remove action {key}</button><small>Remove its Then steps before removing an action.</small>
          </details>;
        })}
        <div className="stage-action-fields"><label className="field"><span>New action type</span><select value={newKind} onChange={(event) => setNewKind(event.target.value as ActionKind)}>{actionKinds.map((kind) => <option key={kind}>{kind}</option>)}</select></label><button type="button" onClick={() => {
          let key = `Action_${Object.keys(declaration.actions).length + 1}`; while (Object.hasOwn(declaration.actions, key)) key += "_";
          change({ ...declaration, actions: { ...declaration.actions, [key]: { ...newAction(newKind), id: key, name: key } } });
        }}>Add named action</button></div>
      </section>
      <section className="panel stack"><h2>If / Then steps</h2><p className="muted">Steps run in order. Every matching If executes, not just the first match. A false If skips only that step and continues. Use a child workflow for a grouped policy. An alias from a skipped step is unavailable; referencing it fails.</p>
        {declaration.steps.map((step, index) => {
          const action = declaration.actions[step.run];
          const child = action?.kind === "workflow" ? workflows.find((entry) => entry.id === action.config.workflowId) : undefined;
          return <fieldset className="declaration-step stack" key={step.id}><legend>Step {index + 1}</legend>
            <div className="actions"><button type="button" className="small" disabled={index === 0} onClick={() => change({ ...declaration, steps: move(declaration.steps, index, -1) })}>Move step {index + 1} up</button><button type="button" className="small" disabled={index === declaration.steps.length - 1} onClick={() => change({ ...declaration, steps: move(declaration.steps, index, 1) })}>Move step {index + 1} down</button><button type="button" className="small" onClick={() => change({ ...declaration, steps: declaration.steps.filter((_, at) => at !== index) })}>Remove step {index + 1}</button></div>
            <label className="stage-checkbox"><input type="checkbox" checked={!!step.if} onChange={(event) => changeStep(index, { ...step, if: event.target.checked ? freshCondition(sourcesBefore(index)[0]?.name) : undefined })} />If — only run this step when conditions match</label>
            {step.if && <ConditionEditor label={`If step ${index + 1}`} condition={step.if} sources={sourcesBefore(index)} onChange={(condition) => changeStep(index, { ...step, if: condition })} />}
            <div className="stage-action-fields"><label className="field"><span>Then run action</span><select required value={step.run} onChange={(event) => changeStep(index, { ...step, run: event.target.value })}><option value="">Choose a named action</option>{Object.keys(declaration.actions).map((key) => <option key={key}>{key}</option>)}</select></label><label className="field"><span>Save result as</span><input value={step.as || ""} pattern="[A-Za-z0-9_-]{1,128}" placeholder="Review, Diagnosis, Result…" onChange={(event) => changeStep(index, { ...step, as: event.target.value || undefined })} /><small>Unique result name for later conditions and input mappings. {action && `Type: ${outputType(action)}.`}</small></label></div>
            {child && <p>Child workflow inputs: {Object.entries(child.inputs || {}).map(([key, type]) => `${key}: ${type}`).join(", ") || "none"}</p>}
            <label className="stage-checkbox"><input type="checkbox" checked={step.inputs !== undefined} onChange={(event) => changeStep(index, { ...step, inputs: event.target.checked ? structuredClone(action?.inputs || {}) : undefined })} />Customize step inputs</label>
            {step.inputs !== undefined && <><InputMappings inputs={step.inputs} sources={sourcesBefore(index)} onChange={(inputs) => changeStep(index, { ...step, inputs })} />{child && <button type="button" onClick={() => changeStep(index, { ...step, inputs: Object.fromEntries(Object.entries(child.inputs || {}).map(([key, type]) => [key, step.inputs?.[key]?.type === type ? step.inputs[key] : sourcesBefore(index).some((source) => source.name === key && source.type === type) ? { type, from: key } : { type, value: type === "string" ? "" : type === "number" ? 0 : type === "boolean" ? false : type === "array" ? [] : {} }])) })}>Fill child input fields</button>}</>}
            <small>Step ID: {step.id}. Without an override, use the action’s default inputs or forward workflow start data.</small>
          </fieldset>;
        })}
        <button type="button" onClick={() => change({ ...declaration, steps: [...declaration.steps, { id: `step_${crypto.randomUUID().slice(0, 8)}`, run: Object.keys(declaration.actions)[0] || "" }] })}>Add Then step</button>
      </section>
      <section className="panel stack"><h2>Policy preview</h2><pre className="declaration-preview">{preview}</pre><p className="muted">This describes configured execution, not live GitHub subscription or a guarantee that an action is safe.</p><details><summary>Advanced declaration preview (JSON)</summary><pre className="json-output declaration-preview">{json(declaration)}</pre><p className="muted">Edit the structured controls above. Action settings JSON remains available as an escape hatch.</p></details></section>
      <div className="actions"><button type="submit" className="primary" disabled={saving}>{saving ? "Saving…" : "Save workflow"}</button>{dirty && <span className="muted">Unsaved changes</span>}</div>
      </fieldset>
    </form>
  </>;
}
