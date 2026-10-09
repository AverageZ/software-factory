import { useEffect, useRef, useState } from "react";
import { errorMessage, href, post, request, timestamp } from "./api";
import { ErrorNotice, Status } from "./components";
import type { Binding, GitHubConnection, IntakeReport, IssueJob } from "./types";

type Action = "load" | "enable" | "disable" | "poll" | "review";

export function IssueIntake({
  projectId,
  repositoryId,
  connection,
  disabled,
  onBusyChange,
}: {
  projectId: string;
  repositoryId: string;
  connection?: GitHubConnection;
  disabled: boolean;
  onBusyChange: (busy: boolean) => void;
}) {
  const endpoint = `/projects/${encodeURIComponent(projectId)}/repositories/${encodeURIComponent(repositoryId)}/intake`;
  const [report, setReport] = useState<IntakeReport | null>(null);
  const [busy, setBusy] = useState<Action | null>("load");
  const [bindings, setBindings] = useState<Binding[]>([]);
  const [reviewBinding, setReviewBinding] = useState("");
  const [error, setError] = useState("");
  const mounted = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    mounted.current = true;
    onBusyChange(true);
    request<IntakeReport>(endpoint, { signal: controller.signal })
      .then((next) => {
        if (!controller.signal.aborted) {
          setReport(next);
          setReviewBinding(next.reviewBindingId ?? "");
        }
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted)
          setError(`Could not load issue intake. ${errorMessage(cause)}`);
      })
      .finally(() => {
        if (!controller.signal.aborted) {
          setBusy(null);
          onBusyChange(false);
        }
      });
    request<Binding[]>("/bindings", { signal: controller.signal })
      .then((items) => {
        if (!controller.signal.aborted)
          setBindings(items.filter((item) => item.projectId === projectId));
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted)
          setError(`Could not load review bindings. ${errorMessage(cause)}`);
      });
    return () => {
      mounted.current = false;
      controller.abort();
      onBusyChange(false);
    };
  }, [endpoint, onBusyChange]);

  const canEnable = Boolean(
    connection?.canPush && connection.issuesEnabled && !connection.archived,
  );
  const controlsDisabled = disabled || busy !== null;

  async function perform(action: Action) {
    if (controlsDisabled) return;
    if (action !== "load" && !report) return;
    if (action === "enable" && !canEnable) return;
    setBusy(action);
    onBusyChange(true);
    setError("");
    try {
      const next =
        action === "load"
          ? await request<IntakeReport>(endpoint)
          : action === "poll"
            ? await post<IntakeReport>(`${endpoint}/poll`)
            : action === "review"
              ? await request<IntakeReport>(`${endpoint}/review`, {
                  method: "PUT",
                  body: JSON.stringify({ bindingId: reviewBinding }),
                })
              : await request<IntakeReport>(endpoint, {
                  method: "PUT",
                  body: JSON.stringify({ enabled: action === "enable" }),
                });
      if (mounted.current) {
        setReport(next);
        setReviewBinding(next.reviewBindingId ?? "");
      }
    } catch (cause) {
      if (!mounted.current) return;
      const message =
        action === "load"
          ? "Could not refresh intake status."
          : action === "poll"
            ? "Could not receive the poll result; work may still be in progress."
            : "Could not confirm the intake setting; it may have changed.";
      setError(
        `${message} Refresh intake status before retrying. Any report below is historical. ${errorMessage(cause)}`,
      );
    } finally {
      if (mounted.current) {
        setBusy(null);
        onBusyChange(false);
      }
    }
  }

  return (
    <section className="stack github-intake" aria-busy={busy !== null}>
      <div className="github-report-heading" aria-live="polite">
        <h4>Issue intake</h4>
        <Status status={report ? (report.enabled ? "enabled" : "disabled") : "not loaded"} />
        <span className="muted">
          {report?.checkedAt ? (
            <>
              Last polled{" "}
              <time dateTime={report.checkedAt} title={report.checkedAt}>
                {timestamp(report.checkedAt)}
              </time>
            </>
          ) : (
            "No poll recorded"
          )}
        </span>
      </div>
      <p className="muted">
        Opt in to let <code>agent:run</code> select the default issue-to-draft-PR
        workflow. No extra workflow or mode labels are required. OMP writes in
        isolated worktrees, not your checkout. Factory never auto-merges PRs.
        Unsupported or ambiguous workflow/mode labels block a job. Enabling intake
        also creates an editable PR feedback revision workflow for this repository.
      </p>
      <ErrorNotice message={error} />
      <ErrorNotice message={report?.error ? `Intake poll: ${report.error}` : ""} />
      <div className="actions">
        <button
          type="button"
          className={report?.enabled ? "" : "primary"}
          disabled={controlsDisabled || !report || (!report.enabled && !canEnable)}
          onClick={() => void perform(report?.enabled ? "disable" : "enable")}
        >
          {busy === "enable"
            ? "Enabling intake…"
            : busy === "disable"
              ? "Disabling intake…"
              : report?.enabled
                ? "Disable issue intake"
                : "Enable issue intake"}
        </button>
        <button
          type="button"
          disabled={controlsDisabled || !report}
          onClick={() => void perform("poll")}
        >
          {busy === "poll" ? "Polling GitHub…" : "Poll now"}
        </button>
        <button
          type="button"
          className="subtle"
          disabled={controlsDisabled}
          onClick={() => void perform("load")}
        >
          {busy === "load" ? "Loading intake status…" : "Refresh intake status"}
        </button>
      </div>
      <label>
        PR feedback revision binding
        <select
          value={reviewBinding}
          disabled={controlsDisabled}
          onChange={(event) => setReviewBinding(event.target.value)}
        >
          <option value="">Off — do not revise PRs</option>
          {bindings.map((binding) => (
            <option key={binding.id} value={binding.id}>
              {binding.name}
            </option>
          ))}
        </select>
      </label>
      <button
        type="button"
        disabled={controlsDisabled || !report || reviewBinding === (report.reviewBindingId ?? "")}
        onClick={() => void perform("review")}
      >
        {busy === "review" ? "Saving review binding…" : "Save review binding"}
      </button>
      <p className="muted">
        A PR feedback revision workflow is selected automatically when issue intake
        is enabled. Edit that workflow or choose another binding with one agent node
        receiving <code>pullRequest</code> (object) and <code>feedback</code> (array).
        Its <code>feedbackSources</code> selects review surfaces. Choose Off to disable
        revisions. On an open owned PR, <code>agent:run</code> authorizes action;
        trusted review feedback automatically selects <code>agent/workflow:review</code>.
      </p>
      {!report?.enabled && !canEnable && (
        <p className="muted">
          Check the GitHub connection before enabling intake. A matching repository
          with push permission, issues enabled, and no archive restriction is required.
        </p>
      )}
      <p className="muted">
        Polling rechecks the saved GitHub identity before admitting issues.
        Disabling stops new issue work and publication; existing PR lifecycle and
        cleanup polling continue. Poll now does not wait for agents to finish.
        Refresh intake status to see the latest durable job records.
      </p>
      {report && (
        report.jobs.length ? (
          <ul className="github-intake-jobs">
            {report.jobs.map((job) => <IntakeJob key={job.id} job={job} />)}
          </ul>
        ) : (
          <p className="muted">No issue jobs recorded for this repository.</p>
        )
      )}
      {report && report.revisions?.length > 0 && (
        <div className="stack">
          <h4>PR revisions</h4>
          <ul className="github-intake-jobs">
            {report.revisions.map((revision) => (
              <li className="stack github-intake-job" key={revision.id}>
                <div className="github-report-heading">
                  PR #{revision.pullRequestNumber} <Status status={revision.status} />
                </div>
                <ErrorNotice message={revision.error ?? ""} />
                {revision.runId && (
                  <a href={href("runs", revision.runId)}>View revision run</a>
                )}
                <span className="muted">
                  {revision.feedbackKeys.length} feedback item
                  {revision.feedbackKeys.length === 1 ? "" : "s"} captured
                </span>
                {revision.worktree && (
                  <span>Retained worktree: <code>{revision.worktree}</code></span>
                )}
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function IntakeJob({ job }: { job: IssueJob }) {
  const retained = ["failed", "interrupted", "paused"].includes(job.status);
  const cleanupPending = ["published", "closed"].includes(job.status);
  return (
    <li className="stack github-intake-job">
      <div className="github-report-heading">
        <a href={job.url} target="_blank" rel="noreferrer">
          #{job.number} {job.title}
        </a>
        <Status status={job.status} />
        <span className="muted">
          Updated{" "}
          <time dateTime={job.updatedAt} title={job.updatedAt}>
            {timestamp(job.updatedAt)}
          </time>
        </span>
      </div>
      <ErrorNotice message={job.error ?? ""} />
      {(job.runId || job.pullRequestUrl) && (
        <div className="actions">
          {job.runId && <a href={href("runs", job.runId)}>View run</a>}
          {job.pullRequestUrl && (
            <a href={job.pullRequestUrl} target="_blank" rel="noreferrer">
              View PR{job.pullRequestNumber ? ` #${job.pullRequestNumber}` : ""}
            </a>
          )}
        </div>
      )}
      <dl>
        <dt>GitHub repository ID</dt>
        <dd><code>{job.githubRepositoryId}</code></dd>
        {job.branch && (
          <>
            <dt>Branch</dt>
            <dd><code>{job.branch}</code></dd>
          </>
        )}
        <dt>Worktree</dt>
        <dd>
          {job.worktree ? (
            <>
              <code>{job.worktree}</code>
              <span className="github-worktree-state muted">
                {cleanupPending
                  ? "Cleanup pending; worktree retained."
                  : retained
                    ? "Retained for inspection."
                    : "Isolated workspace allocated."}
              </span>
            </>
          ) : (
            "No retained worktree reported."
          )}
        </dd>
      </dl>
      {job.worktree && (retained || cleanupPending) && (
        <p className="muted">
          Uncertain, dirty, or untracked work is retained rather than force-deleted.
          Check the job error before attempting manual cleanup.
        </p>
      )}
    </li>
  );
}
