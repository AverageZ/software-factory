import { useState } from "react";
import type { FormEvent } from "react";
import { errorMessage, href, post } from "./api";
import { ErrorNotice, PageHeader, RunTable } from "./components";
import { GitHubSetup } from "./github";
import type { Binding, Project, Run, Workflow } from "./types";

export function ProjectList({ projects }: { projects: Project[] }) {
  return (
    <>
      <PageHeader
        title="Projects"
        description="Local repositories, execution settings, and workflow bindings."
      >
        <a className="button primary" href={href("projects", "new")}>
          New project
        </a>
      </PageHeader>
      {projects.length ? (
        <div className="card-grid">
          {projects.map((project) => (
            <a
              className="project-card"
              key={project.id}
              href={href("projects", project.id)}
            >
              <h2>{project.name}</h2>
              <p>
                {project.repositories.length}{" "}
                {project.repositories.length === 1
                  ? "repository"
                  : "repositories"}{" "}
                · up to {project.policy.maxParallel} parallel nodes
              </p>
              <ul>
                {project.repositories.map((repo) => (
                  <li key={repo.id}>
                    <strong>{repo.name}</strong>
                    <code>{repo.path}</code>
                  </li>
                ))}
              </ul>
              <span className="muted">
                {project.harness.binary} · {project.harness.model}
              </span>
            </a>
          ))}
        </div>
      ) : (
        <section className="panel empty">
          <h2>Connect your first project</h2>
          <p>
            Attach existing local Git repositories, then bind a reusable
            workflow. Factory does not copy or manage repository rules, skills,
            or prompts.
          </p>
          <a className="button" href={href("projects", "new")}>
            Create project
          </a>
        </section>
      )}
    </>
  );
}

export function ProjectPage({
  project,
  bindings,
  workflows,
  runs,
  onSaved,
}: {
  project?: Project;
  bindings: Binding[];
  workflows: Workflow[];
  runs: Run[];
  onSaved: (project: Project) => void;
}) {
  const [draft, setDraft] = useState<Project>(
    () =>
      project || {
        id: "",
        name: "",
        repositories: [{ id: crypto.randomUUID(), name: "", path: "" }],
        harness: { binary: "omp", model: "anthropic/claude-haiku-4-5" },
        policy: { maxParallel: 4 },
      },
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [message, setMessage] = useState("");
  const projectBindings = bindings.filter(
    (binding) => binding.projectId === project?.id,
  );

  async function save(event: FormEvent) {
    event.preventDefault();
    setSaving(true);
    setError("");
    setMessage("");
    try {
      const saved = await post<Project>("/projects", draft);
      setDraft(saved);
      onSaved(saved);
      setMessage("Project saved.");
      if (!project) window.location.hash = href("projects", saved.id);
    } catch (cause) {
      setError(errorMessage(cause));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title={project ? project.name : "New project"}
        description="Commands and agents run directly in these repositories with your local permissions."
      >
        <a className="button" href={href("projects")}>
          All projects
        </a>
      </PageHeader>
      <div className="project-columns">
        <section className="panel">
          <h2>Project settings</h2>
          <form className="stack" onSubmit={save}>
            <ErrorNotice message={error} />
            {message && (
              <div className="notice" role="status">
                {message}
              </div>
            )}
            <label className="field">
              <span>Project name</span>
              <input
                required
                value={draft.name}
                onChange={(event) =>
                  setDraft({ ...draft, name: event.target.value })
                }
              />
            </label>
            <fieldset>
              <legend>Repositories</legend>
              <p className="muted">
                Use absolute paths to existing local Git roots. Repository IDs
                remain stable when names or paths change.
              </p>
              <div className="stack">
                {draft.repositories.map((repo, index) => (
                  <div className="repository-fields" key={repo.id}>
                    <label className="field">
                      <span>Repository name</span>
                      <input
                        required
                        value={repo.name}
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            repositories: draft.repositories.map((entry, i) =>
                              i === index
                                ? { ...entry, name: event.target.value }
                                : entry,
                            ),
                          })
                        }
                      />
                    </label>
                    <label className="field">
                      <span>Local Git path</span>
                      <input
                        required
                        value={repo.path}
                        onChange={(event) =>
                          setDraft({
                            ...draft,
                            repositories: draft.repositories.map((entry, i) =>
                              i === index
                                ? { ...entry, path: event.target.value }
                                : entry,
                            ),
                          })
                        }
                        placeholder="Absolute repository path"
                      />
                    </label>
                    <button
                      type="button"
                      className="danger subtle"
                      onClick={() =>
                        setDraft({
                          ...draft,
                          repositories: draft.repositories.filter(
                            (_, i) => i !== index,
                          ),
                        })
                      }
                    >
                      Remove repository
                    </button>
                  </div>
                ))}
              </div>
              <button
                type="button"
                onClick={() =>
                  setDraft({
                    ...draft,
                    repositories: [
                      ...draft.repositories,
                      { id: crypto.randomUUID(), name: "", path: "" },
                    ],
                  })
                }
              >
                Add repository
              </button>
            </fieldset>
            <fieldset>
              <legend>Agent harness</legend>
              <div className="form-grid">
                <label className="field">
                  <span>Harness binary</span>
                  <input
                    required
                    value={draft.harness.binary}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        harness: {
                          ...draft.harness,
                          binary: event.target.value,
                        },
                      })
                    }
                  />
                </label>
                <label className="field">
                  <span>Model</span>
                  <input
                    required
                    value={draft.harness.model}
                    onChange={(event) =>
                      setDraft({
                        ...draft,
                        harness: {
                          ...draft.harness,
                          model: event.target.value,
                        },
                      })
                    }
                  />
                </label>
              </div>
              <p className="muted">
                Uses inherited authentication and normal repository discovery.
                Factory does not override home, skills, rules, or system
                prompts.
              </p>
            </fieldset>
            <label className="field">
              <span>Maximum parallel nodes</span>
              <input
                className="short-input"
                type="number"
                min={1}
                step={1}
                required
                value={draft.policy.maxParallel}
                onChange={(event) =>
                  setDraft({
                    ...draft,
                    policy: { maxParallel: Number(event.target.value) },
                  })
                }
              />
            </label>
            <div className="actions">
              <button className="primary" type="submit" disabled={saving}>
                {saving ? "Saving…" : "Save project"}
              </button>
            </div>
          </form>
        </section>
        <div className="stack">
          <section className="panel">
            <div className="section-heading">
              <h2>Workflow bindings</h2>
              {project && (
                <a
                  className="button"
                  href={`${href("bindings", "new")}?project=${encodeURIComponent(project.id)}`}
                >
                  New binding
                </a>
              )}
            </div>
            {!project ? (
              <p className="muted">
                Save the project before attaching workflows.
              </p>
            ) : projectBindings.length ? (
              <ul className="record-list">
                {projectBindings.map((binding) => (
                  <li key={binding.id}>
                    <a href={href("bindings", binding.id)}>{binding.name}</a>
                    <span className="muted">
                      {workflows.find(
                        (workflow) => workflow.id === binding.workflowId,
                      )?.name || binding.workflowId}
                    </span>
                    <a
                      className="button small"
                      href={href("bindings", binding.id)}
                    >
                      Configure & run
                    </a>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="empty">
                No workflow bindings. Create a workflow, then map its repository
                slots here.
              </p>
            )}
            <a href={href("workflows")}>Manage reusable workflows</a>
          </section>
          <section className="panel">
            <h2>Recent runs</h2>
            <RunTable
              runs={runs.filter((run) => run.projectId === project?.id)}
            />
          </section>
        </div>
      </div>
      <GitHubSetup
        project={project}
        repositories={draft.repositories}
        saving={saving}
      />
    </>
  );
}
