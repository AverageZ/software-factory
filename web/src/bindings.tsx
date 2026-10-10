import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { errorMessage, href, post } from "./api";
import {
  ErrorNotice,
  PageHeader,
  TypedValues,
  typedValues,
  valueText,
} from "./components";
import type { Binding, Project, Run, Workflow } from "./types";
import { workflowActions } from "./declarative";

function repositorySlots(
  workflow: Workflow | undefined,
  workflows: Workflow[],
  seen = new Set<string>(),
): string[] {
  if (!workflow || seen.has(workflow.id)) return [];
  seen.add(workflow.id);
  const slots = new Set<string>();
  for (const node of workflowActions(workflow)) {
    if (node.repository) slots.add(node.repository);
    if (node.kind === "workflow") {
      const child = workflows.find(
        (entry) => entry.id === node.config.workflowId,
      );
      for (const slot of repositorySlots(child, workflows, seen))
        slots.add(slot);
    }
  }
  return [...slots].sort();
}

export function BindingPage({
  binding,
  initialProjectId,
  projects,
  workflows,
  onSaved,
  onRun,
}: {
  binding?: Binding;
  initialProjectId: string;
  projects: Project[];
  workflows: Workflow[];
  onSaved: (binding: Binding) => void;
  onRun: (run: Run) => void;
}) {
  const [draft, setDraft] = useState<Binding>(
    () =>
      binding || {
        id: "",
        name: "",
        projectId: initialProjectId,
        workflowId: "",
        repositories: {},
        inputs: {},
      },
  );
  const [values, setValues] = useState(() => valueText(binding?.inputs || {}));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const project = projects.find((entry) => entry.id === draft.projectId);
  const workflow = workflows.find((entry) => entry.id === draft.workflowId);
  const slots = useMemo(
    () => repositorySlots(workflow, workflows),
    [workflow, workflows],
  );

  async function save(startRun: boolean) {
    setBusy(true);
    setError("");
    setMessage("");
    try {
      if (!project || !workflow)
        throw new Error("Select a project and a workflow.");
      if (!draft.name.trim()) throw new Error("Enter a binding name.");
      for (const slot of slots)
        if (
          !project.repositories.some(
            (repository) => repository.id === draft.repositories[slot],
          )
        )
          throw new Error(
            `Map repository slot “${slot}” to a project repository.`,
          );
      const saved = await post<Binding>("/bindings", {
        ...draft,
        repositories: Object.fromEntries(
          slots.map((slot) => [slot, draft.repositories[slot]]),
        ),
        inputs: typedValues(workflow.inputs || {}, values),
      });
      setDraft(saved);
      onSaved(saved);
      setMessage("Binding saved.");
      if (startRun) {
        const run = await post<Run>("/runs", { bindingId: saved.id });
        onRun(run);
        window.location.hash = href("runs", run.id);
      } else if (!binding) window.location.hash = href("bindings", saved.id);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  function submit(event: FormEvent) {
    event.preventDefault();
    void save(false);
  }

  return (
    <>
      <PageHeader
        title={binding ? binding.name : "New workflow binding"}
        description="Map a reusable workflow to this project's repositories and typed run inputs."
      >
        {project && (
          <a className="button" href={href("projects", project.id)}>
            Back to project
          </a>
        )}
      </PageHeader>
      <form className="panel narrow stack" onSubmit={submit}>
        <ErrorNotice message={error} />
        {message && (
          <div className="notice" role="status">
            {message}
          </div>
        )}
        <label className="field">
          <span>Binding name</span>
          <input
            required
            value={draft.name}
            onChange={(event) =>
              setDraft({ ...draft, name: event.target.value })
            }
          />
        </label>
        <div className="form-grid">
          <label className="field">
            <span>Project</span>
            <select
              required
              value={draft.projectId}
              onChange={(event) =>
                setDraft({
                  ...draft,
                  projectId: event.target.value,
                  repositories: {},
                })
              }
            >
              <option value="">Select a project</option>
              {projects.map((entry) => (
                <option key={entry.id} value={entry.id}>
                  {entry.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field">
            <span>Workflow</span>
            <select
              required
              value={draft.workflowId}
              onChange={(event) => {
                setDraft({
                  ...draft,
                  workflowId: event.target.value,
                  repositories: {},
                });
                setValues({});
              }}
            >
              <option value="">Select a workflow</option>
              {workflows.map((entry) => (
                <option key={entry.id} value={entry.id}>
                  {entry.name}
                </option>
              ))}
            </select>
          </label>
        </div>
        {!projects.length && (
          <p className="notice">
            Create a <a href={href("projects", "new")}>project</a> first.
          </p>
        )}
        {!workflows.length && (
          <p className="notice">
            Create a <a href={href("workflows", "new")}>workflow</a> first.
          </p>
        )}
        {workflow && (
          <>
            <a href={href("workflows", workflow.id)}>Edit workflow graph</a>
            <fieldset>
              <legend>Repository slot mapping</legend>
              {slots.length ? (
                <div className="stack">
                  {slots.map((slot) => (
                    <label className="field" key={slot}>
                      <span>
                        Slot: <code>{slot}</code>
                      </span>
                      <select
                        required
                        value={draft.repositories[slot] || ""}
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            repositories: {
                              ...draft.repositories,
                              [slot]: event.target.value,
                            },
                          })
                        }
                      >
                        <option value="">Select a repository</option>
                        {project?.repositories.map((repo) => (
                          <option key={repo.id} value={repo.id}>
                            {repo.name} — {repo.path}
                          </option>
                        ))}
                      </select>
                    </label>
                  ))}
                </div>
              ) : (
                <p className="muted">
                  No repository slots in this workflow or its child workflows.
                </p>
              )}
              <p className="muted">Child workflows inherit these mappings.</p>
            </fieldset>
            <fieldset>
              <legend>Typed run inputs</legend>
              <TypedValues
                declarations={workflow.inputs || {}}
                values={values}
                onChange={setValues}
              />
            </fieldset>
            {workflowActions(workflow).some((node) => node.kind === "decision") && (
              <div className="notice warning">
                This workflow contains an unavailable decision node. It cannot
                complete successfully until that node is replaced.
              </div>
            )}
          </>
        )}
        <div className="notice">
          Execution uses real local files, processes, and network access.
          Starting a run saves this binding first; the daemon snapshots its
          configuration. There are no simulated runs.
        </div>
        <div className="actions">
          <button type="submit" disabled={busy}>
            {busy ? "Working…" : "Save binding"}
          </button>
          <button
            type="button"
            className="primary"
            disabled={busy || !project || !workflow}
            onClick={() => void save(true)}
          >
            {busy ? "Working…" : "Save binding & start run"}
          </button>
        </div>
      </form>
    </>
  );
}
