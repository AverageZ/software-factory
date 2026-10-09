import { useEffect, useRef, useState } from "react";
import { errorMessage, post, request, timestamp } from "./api";
import { ErrorNotice, Status } from "./components";
import { IssueIntake } from "./intake";
import type {
  GitHubHealth,
  GitHubLabel,
  GitHubTarget,
  Project,
  Repository,
} from "./types";

const labelGroups: { id: GitHubLabel["group"]; name: string }[] = [
  { id: "authorization", name: "Authorization" },
  { id: "workflow", name: "Workflow" },
  { id: "mode", name: "Mode" },
  { id: "status", name: "Status" },
  { id: "phase", name: "Phase" },
];

type Action = "load" | "check" | "labels";

export function GitHubSetup({
  project,
  repositories,
  saving,
}: {
  project?: Project;
  repositories: Repository[];
  saving: boolean;
}) {
  return (
    <section className="panel github-setup">
      <h2>GitHub setup</h2>
      <p className="muted">
        Readiness covers the GitHub connection and label definitions, not a
        guarantee that an agent run will succeed. Creating definitions does not
        label issues or enable intake. Status labels are not authorization.
      </p>
      {!repositories.length && (
        <p className="muted">Add and save a repository to check its GitHub setup.</p>
      )}
      <div className="stack">
        {repositories.map((repository) => {
          const saved = project?.repositories.find(
            (entry) => entry.id === repository.id,
          );
          const pathSaved = saved?.path === repository.path;
          return (
            <article className="github-repository" key={repository.id}>
              <h3>{repository.name || "Unnamed repository"}</h3>
              <p className="github-local-path">
                <code>{repository.path || "No local path entered"}</code>
              </p>
              {project && pathSaved ? (
                <SavedGitHubRepository
                  key={`${project.id}:${repository.id}:${repository.path}`}
                  projectId={project.id}
                  repository={repository}
                  saving={saving}
                />
              ) : (
                <div className="notice warning" role="status">
                  Save this repository path before checking GitHub, creating
                  labels, or managing issue intake. Reports and actions for a
                  previous path are hidden while the path has unsaved changes.
                </div>
              )}
            </article>
          );
        })}
      </div>
    </section>
  );
}

function SavedGitHubRepository({
  projectId,
  repository,
  saving,
}: {
  projectId: string;
  repository: Repository;
  saving: boolean;
}) {
  const endpoint = `/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repository.id)}/github`;
  const [report, setReport] = useState<GitHubHealth | null>(null);
  const [busy, setBusy] = useState<Action | null>("load");
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [intakeBusy, setIntakeBusy] = useState(false);
  const mounted = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    mounted.current = true;
    request<GitHubHealth>(endpoint, { signal: controller.signal })
      .then((health) => {
        if (!controller.signal.aborted) setReport(health);
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted)
          setError(`Could not load the saved GitHub report. ${errorMessage(cause)}`);
      })
      .finally(() => {
        if (!controller.signal.aborted) setBusy(null);
      });
    return () => {
      mounted.current = false;
      controller.abort();
    };
  }, [endpoint]);

  const health = report?.repositoryPath === repository.path ? report : null;
  const connection = health?.connection;
  const missing = health?.labels.filter((label) => !label.present) ?? [];
  const checkedAt = health?.checkedAt;
  const checked = Boolean(checkedAt && health?.status !== "unchecked");
  const canCreate = Boolean(
    checked &&
      connection?.canManageLabels &&
      !connection.archived &&
      missing.length,
  );
  const disabled = busy !== null || intakeBusy || saving;

  async function perform(action: Action) {
    if (disabled) return;
    if (action === "labels" && (!confirming || !canCreate)) return;
    setBusy(action);
    setError("");
    setConfirming(false);
    try {
      let next: GitHubHealth;
      if (action === "load") {
        next = await request<GitHubHealth>(endpoint);
      } else if (action === "check") {
        next = await post<GitHubHealth>(`${endpoint}/check`);
      } else {
        if (!connection) return;
        const target: GitHubTarget = {
          host: connection.host,
          repositoryId: connection.id,
        };
        next = await post<GitHubHealth>(`${endpoint}/labels`, target);
      }
      if (mounted.current) setReport(next);
    } catch (cause) {
      if (!mounted.current) return;
      const message = errorMessage(cause);
      if (message.startsWith("409:")) {
        setError(
          `${message} Another GitHub action is in progress for this repository. Wait for it to finish, then reload the saved report or check again.`,
        );
      } else if (action === "labels") {
        setError(
          `Could not receive the label setup result. Some labels may have been created; check GitHub before retrying. ${message}`,
        );
      } else {
        setError(
          `${action === "load" ? "Could not reload the saved report." : "Could not refresh GitHub health."} Any report below is historical. ${message}`,
        );
      }
    } finally {
      if (mounted.current) setBusy(null);
    }
  }

  return (
    <div className="stack github-health" aria-busy={busy !== null}>
      <div className="github-report-heading" aria-live="polite">
        <span>GitHub setup</span>
        <Status status={health?.status ?? "not loaded"} />
        <span className="muted">
          {checked ? (
            <>
              Last checked{" "}
              <time dateTime={checkedAt} title={checkedAt}>
                {timestamp(checkedAt)}
              </time>
            </>
          ) : health ? (
            "Never checked for this saved path"
          ) : (
            "No matching report loaded"
          )}
        </span>
      </div>
      <p className="muted">
        This is a saved, historical report, not live monitoring. Checking GitHub
        is read-only; labels are created only after explicit confirmation.
      </p>
      <ErrorNotice message={error} />
      {report && !health && (
        <div className="notice warning" role="status">
          The saved report belongs to a different local path and is hidden.
          Check GitHub connection to create a report for this saved path.
        </div>
      )}
      <div className="actions">
        <button
          type="button"
          className="primary"
          disabled={disabled}
          onClick={() => void perform("check")}
        >
          {busy === "check" ? "Checking GitHub…" : "Check GitHub connection"}
        </button>
        <button
          type="button"
          disabled={disabled || !canCreate || confirming}
          onClick={() => setConfirming(true)}
        >
          {busy === "labels" ? "Creating missing labels…" : "Create missing labels"}
        </button>
        <button
          type="button"
          className="subtle"
          disabled={disabled}
          onClick={() => void perform("load")}
        >
          {busy === "load" ? "Loading saved report…" : "Reload saved report"}
        </button>
      </div>
      {saving && (
        <p className="muted">GitHub actions pause while the project saves.</p>
      )}
      {!checked ? (
        <p className="muted">
          Check the connection to discover the actual origin repository,
          authenticated account, permissions, and label presence.
        </p>
      ) : !connection ? (
        <p className="muted">
          Label setup is unavailable until a check identifies the GitHub repository.
        </p>
      ) : connection.archived ? (
        <p className="muted">
          Label setup is unavailable for an archived repository.
        </p>
      ) : !connection.canManageLabels ? (
        <p className="muted">
          The connected account does not report permission to manage labels.
        </p>
      ) : !missing.length ? (
        <p className="muted">No missing label definitions were reported.</p>
      ) : null}
      {confirming && canCreate && connection && (
        <div className="notice warning github-confirmation" role="alert">
          <h4>Confirm label creation</h4>
          <p>
            Create {missing.length} missing label definitions in{" "}
            <strong>
              {connection.owner}/{connection.name}
            </strong>{" "}
            on{" "}
            <strong>{connection.host}</strong> (repository ID{" "}
            <code>{connection.id}</code>)?
          </p>
          <p>
            Factory rechecks this exact target before writing and creates only
            names still missing. Existing labels, colors, and descriptions stay
            unchanged. No issues will be labeled and no workflows will start.
          </p>
          <div className="actions">
            <button
              type="button"
              className="primary"
              disabled={disabled}
              onClick={() => void perform("labels")}
            >
              Confirm: create {missing.length} missing labels
            </button>
            <button
              type="button"
              disabled={disabled}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </button>
          </div>
        </div>
      )}
      {health?.setupError && (
        <ErrorNotice message={`Label setup did not finish: ${health.setupError}`} />
      )}
      {health && health.createdLabels.length > 0 && (
        <div className="notice" role="status">
          <strong>Labels created in this report</strong>
          <ul className="github-created-labels">
            {health.createdLabels.map((name) => (
              <li key={name}>
                <code>{name}</code>
              </li>
            ))}
          </ul>
          {health?.setupError && (
            <p>
              These creations were retained despite the failure. A new confirmed
              attempt will create only labels that are still missing.
            </p>
          )}
        </div>
      )}
      {health && (
        <div className="github-report-columns">
          <div>
            {connection && (
              <section className="github-connection">
                <h4>Actual GitHub target</h4>
                <a href={connection.url} target="_blank" rel="noreferrer">
                  {connection.owner}/{connection.name}
                </a>
                <dl>
                  <dt>Host / repository ID</dt>
                  <dd>
                    {connection.host} / <code>{connection.id}</code>
                  </dd>
                  <dt>Connected account</dt>
                  <dd>{connection.account}</dd>
                  <dt>Default branch</dt>
                  <dd>
                    <code>{connection.defaultBranch || "Not reported"}</code>
                  </dd>
                  <dt>Push permission</dt>
                  <dd>{connection.canPush ? "Reported" : "Not reported"}</dd>
                  <dt>Manage labels</dt>
                  <dd>
                    {connection.canManageLabels ? "Reported" : "Not reported"}
                  </dd>
                  <dt>Archived</dt>
                  <dd>{connection.archived ? "Yes" : "No"}</dd>
                  <dt>Issues enabled</dt>
                  <dd>{connection.issuesEnabled ? "Yes" : "No"}</dd>
                </dl>
                <p className="muted">
                  Repository permissions do not prove every token scope or
                  branch-protection requirement.
                </p>
              </section>
            )}
            <section>
              <h4>Connection checks</h4>
              {health.checks.length ? (
                <ul className="github-checks">
                  {health.checks.map((check) => (
                    <li key={check.id}>
                      <Status status={check.status} />
                      <span>{check.message}</span>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="muted">No checks recorded for this saved path.</p>
              )}
            </section>
          </div>
          <section>
            <h4>Label definitions</h4>
            <p className="muted">
              {checked
                ? `${health.labels.length - missing.length} of ${health.labels.length} label names confirmed present.`
                : "Label presence has not been checked."}{" "}
              Existing definitions are preserved, even if their colors or
              descriptions differ.
            </p>
            {health.status === "blocked" && (
              <p className="muted">
                Failed checks can leave missing names unverified. Review the
                connection checks before attempting setup.
              </p>
            )}
            <div className="github-label-groups">
              {labelGroups.map((group) => {
                const labels = health.labels.filter(
                  (label) => label.group === group.id,
                );
                const absent = labels.filter((label) => !label.present).length;
                return (
                  <details
                    className="github-label-group"
                    key={group.id}
                    open={checked && absent > 0}
                  >
                    <summary>
                      {group.name}{" "}
                      <span className="muted">
                        {checked
                          ? absent
                            ? `${absent} ${health.status === "blocked" ? "missing or unverified" : "missing"} / ${labels.length}`
                            : `All ${labels.length} present`
                          : `${labels.length} not checked`}
                      </span>
                    </summary>
                    <ul>
                      {labels.map((label) => (
                        <li key={label.name}>
                          <code title={label.description}>{label.name}</code>
                          <span className="muted">
                            {!checked
                              ? "Not checked"
                              : label.present
                                ? "Present"
                                : health.status === "blocked"
                                  ? "Unverified"
                                  : "Missing"}
                          </span>
                        </li>
                      ))}
                    </ul>
                  </details>
                );
              })}
            </div>
          </section>
        </div>
      )}
      <IssueIntake
        projectId={projectId}
        repositoryId={repository.id}
        connection={checked ? connection : undefined}
        disabled={busy !== null || saving}
        onBusyChange={setIntakeBusy}
      />
    </div>
  );
}
