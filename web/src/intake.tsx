import { useEffect, useRef, useState } from "react";
import { errorMessage, href, post, request, timestamp } from "./api";
import { ErrorNotice, Status } from "./components";
import type {
  Binding,
  GitHubConnection,
  IntakeReport,
  IssueJob,
  ReviewJob,
  SyncJob,
} from "./types";

type Action =
  | "load"
  | "enable"
  | "disable"
  | "poll"
  | "issue"
  | "review"
  | "sync"
  | "release"
  | "recover";
type RecoveryAction = "retry" | "dismiss";
type ManagedJob = ReviewJob | SyncJob;

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
  const [issueBinding, setIssueBinding] = useState("");
  const [syncBinding, setSyncBinding] = useState("");
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
          setIssueBinding(next.issueBindingId ?? "");
          setSyncBinding(next.syncBindingId ?? "");
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
          setError(`Could not load workflow bindings. ${errorMessage(cause)}`);
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
            : action === "review" || action === "issue" || action === "sync"
              ? await request<IntakeReport>(`${endpoint}/${action}`, {
                  method: "PUT",
                  body: JSON.stringify({
                    bindingId:
                      action === "issue"
                        ? issueBinding
                        : action === "review"
                          ? reviewBinding
                          : syncBinding,
                  }),
                })
              : await request<IntakeReport>(endpoint, {
                  method: "PUT",
                  body: JSON.stringify({ enabled: action === "enable" }),
                });
      if (mounted.current) {
        setReport(next);
        setReviewBinding(next.reviewBindingId ?? "");
        setIssueBinding(next.issueBindingId ?? "");
        setSyncBinding(next.syncBindingId ?? "");
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

  async function release(job: IssueJob) {
    if (controlsDisabled || !report || (!job.headSHA && !job.baseSHA)) return;
    const checkpointed = Boolean(job.headSHA);
    const message = checkpointed
      ? `Release the retained candidate for issue #${job.number}? Factory will remove its clean managed worktree, keep checkpoint branch ${job.branch ?? "(unknown)"}, and allow queued work to start. The candidate will not be published.`
      : `Release the no-change workspace for issue #${job.number}? Factory will remove it only if it is clean and still exactly matches its recorded base, then allow queued work to start.`;
    if (!window.confirm(message)) return;
    setBusy("release");
    onBusyChange(true);
    setError("");
    try {
      const next = await post<IntakeReport>(
        `${endpoint}/jobs/${encodeURIComponent(job.id)}/release`,
        { headSHA: job.headSHA ?? "", baseSHA: job.baseSHA ?? "" },
      );
      if (mounted.current) setReport(next);
    } catch (cause) {
      if (mounted.current)
        setError(
          `Could not release the retained workspace. It was preserved. Refresh intake status before retrying. ${errorMessage(cause)}`,
        );
    } finally {
      if (mounted.current) {
        setBusy(null);
        onBusyChange(false);
      }
    }
  }

  async function recover(
    kind: "revisions" | "synchronizations",
    job: ManagedJob,
    action: RecoveryAction,
  ) {
    if (controlsDisabled || !report) return;
    const work = kind === "revisions" ? "revision" : "synchronization";
    const retained = job.worktree
      ? " This permanently deletes the retained worktree and its uncommitted changes."
      : "";
    const outcome =
      action === "retry"
        ? " Factory will admit a fresh attempt if the PR is still eligible."
        : " Factory will treat this feedback or synchronization observation as handled.";
    if (
      !window.confirm(
        `${action === "retry" ? "Retry" : "Dismiss"} the failed PR ${work} for #${job.pullRequestNumber}?${retained}${outcome}`,
      )
    )
      return;
    setBusy("recover");
    onBusyChange(true);
    setError("");
    try {
      const next = await post<IntakeReport>(
        `${endpoint}/${kind}/${encodeURIComponent(job.id)}/recover`,
        {
          action,
          baseSHA: job.baseSHA ?? "",
          headSHA: job.headSHA ?? "",
          ...("pullRequestHeadSHA" in job
            ? { pullRequestHeadSHA: job.pullRequestHeadSHA }
            : {}),
        },
      );
      if (mounted.current) setReport(next);
    } catch (cause) {
      if (mounted.current)
        setError(
          `Could not recover the failed PR ${work}. Its retained state was preserved. Refresh intake status before retrying. ${errorMessage(cause)}`,
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
        Workflows are functions. Bindings set their repository and inputs.
        On an open issue, <code>agent:run</code> calls the selected issue binding
        with the issue as an input. An optional <code>agent/workflow:BINDING_ID</code>
        label selects another configured binding. Unknown or conflicting selectors
        block work. Factory runs the agent in an isolated worktree and publishes
        a draft PR. It does not create workflows or merge PRs.
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
        Issue workflow binding
        <select
          value={issueBinding}
          disabled={controlsDisabled}
          onChange={(event) => setIssueBinding(event.target.value)}
        >
          <option value="">None — do not run issue work</option>
          {bindings.map((binding) => (
            <option key={binding.id} value={binding.id}>
              {binding.name}
            </option>
          ))}
        </select>
      </label>
      <button
        type="button"
        disabled={controlsDisabled || !report || issueBinding === (report.issueBindingId ?? "")}
        onClick={() => void perform("issue")}
      >
        {busy === "issue" ? "Saving issue binding…" : "Save issue binding"}
      </button>
      <p className="muted">
        Select a configured workflow with one agent node and no edges. Its agent
        must target this repository and receive <code>issue</code> (object) from
        <code>inputs.issue</code>. Set other workflow inputs in the binding.
        Other graph shapes are not supported for managed intake. A binding
        save reports any configuration error. Without a valid issue binding,
        intake does no work.
      </p>
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
        Revisions are off until you select a configured binding. The workflow must
        have one agent node and no edges, receiving <code>pullRequest</code> (object)
        and <code>feedback</code> (array) from workflow inputs. Its
        <code>feedbackSources</code> selects <code>conversation</code>,
        <code>inline</code>, or <code>review</code>. Choose Off to stop revisions.
        On an open owned PR, <code>agent:run</code> authorizes action; trusted review
        feedback selects the configured revision binding and Factory adds
        <code>agent/workflow:review</code> only if no conflicting selector is present.
      </p>
      <label>
        PR synchronization binding
        <select
          value={syncBinding}
          disabled={controlsDisabled}
          onChange={(event) => setSyncBinding(event.target.value)}
        >
          <option value="">Off — do not synchronize PR branches</option>
          {bindings.map((binding) => (
            <option key={binding.id} value={binding.id}>
              {binding.name}
            </option>
          ))}
        </select>
      </label>
      <button
        type="button"
        disabled={controlsDisabled || !report || syncBinding === (report.syncBindingId ?? "")}
        onClick={() => void perform("sync")}
      >
        {busy === "sync" ? "Saving synchronization binding…" : "Save synchronization binding"}
      </button>
      <p className="muted">
        Synchronization is off until you select a configured binding. The workflow
        must have one agent node and no edges, receiving <code>pullRequest</code>
        and <code>synchronization</code> objects from workflow inputs. On an open
        Factory-owned PR, <code>agent:run</code> authorizes branch updates. Factory
        adds <code>agent/workflow:sync</code> to show that the PR is monitored,
        merges the current base in an isolated worktree, runs the agent to resolve
        conflicts and validate, and uses an exact-head compare-and-swap push.
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
            {report.jobs.map((job) => (
              <IntakeJob
                key={job.id}
                job={job}
                disabled={controlsDisabled}
                releasing={busy === "release"}
                onRelease={release}
              />
            ))}
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
                  <a href={revision.url} target="_blank" rel="noreferrer">
                    PR #{revision.pullRequestNumber} {revision.title}
                  </a>
                  <Status status={revision.status} />
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
                <ManagedRecoveryActions
                  job={revision}
                  disabled={controlsDisabled}
                  recovering={busy === "recover"}
                  onRecover={(action) =>
                    void recover("revisions", revision, action)
                  }
                />
              </li>
            ))}
          </ul>
        </div>
      )}
      {report && report.synchronizations?.length > 0 && (
        <div className="stack">
          <h4>PR synchronizations</h4>
          <ul className="github-intake-jobs">
            {report.synchronizations.map((synchronization) => (
              <li className="stack github-intake-job" key={synchronization.id}>
                <div className="github-report-heading">
                  <a href={synchronization.url} target="_blank" rel="noreferrer">
                    PR #{synchronization.pullRequestNumber} {synchronization.title}
                  </a>
                  <Status status={synchronization.status} />
                </div>
                <ErrorNotice message={synchronization.error ?? ""} />
                {synchronization.runId && (
                  <a href={href("runs", synchronization.runId)}>
                    View synchronization run
                  </a>
                )}
                <span className="muted">
                  Base <code>{synchronization.baseSHA.slice(0, 12)}</code>
                  {" · "}
                  {synchronization.conflicted
                    ? "Agent resolved merge conflicts"
                    : "Merge did not report conflicts"}
                </span>
                {synchronization.worktree && (
                  <span>
                    Retained worktree: <code>{synchronization.worktree}</code>
                  </span>
                )}
                <ManagedRecoveryActions
                  job={synchronization}
                  disabled={controlsDisabled}
                  recovering={busy === "recover"}
                  onRecover={(action) =>
                    void recover("synchronizations", synchronization, action)
                  }
                />
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  );
}

function ManagedRecoveryActions({
  job,
  disabled,
  recovering,
  onRecover,
}: {
  job: ManagedJob;
  disabled: boolean;
  recovering: boolean;
  onRecover: (action: RecoveryAction) => void;
}) {
  if (!["failed", "timed_out", "interrupted"].includes(job.status)) return null;
  return (
    <div className="stack">
      <div className="actions">
        <button
          type="button"
          disabled={disabled}
          onClick={() => onRecover("retry")}
        >
          {recovering
            ? "Recovering…"
            : job.worktree
              ? "Discard worktree and retry"
              : "Retry"}
        </button>
        <button
          type="button"
          className="danger"
          disabled={disabled}
          onClick={() => onRecover("dismiss")}
        >
          {job.worktree ? "Discard worktree and dismiss" : "Dismiss"}
        </button>
      </div>
      <p className="muted">
        Retry starts from the current authorized PR state. Dismiss prevents this
        feedback or synchronization observation from running again.
      </p>
    </div>
  );
}

function IntakeJob({
  job,
  disabled,
  releasing,
  onRelease,
}: {
  job: IssueJob;
  disabled: boolean;
  releasing: boolean;
  onRelease: (job: IssueJob) => void;
}) {
  const retained = ["failed", "timed_out", "interrupted", "paused"].includes(job.status);
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
      {job.worktree && retained && (job.headSHA || job.baseSHA) && (
        <div className="stack">
          <button
            type="button"
            className="danger"
            disabled={disabled}
            onClick={() => onRelease(job)}
          >
            {releasing
              ? "Releasing workspace…"
              : job.headSHA
                ? "Release candidate and continue queue"
                : "Release no-change workspace and continue queue"}
          </button>
          <p className="muted">
            {job.headSHA
              ? "Release succeeds only for the recorded clean checkpoint. Factory keeps the local checkpoint branch and refuses to remove dirty, changed, or uncheckpointed work."
              : "Release succeeds only if the workspace is clean and its branch and HEAD still exactly match the recorded base."}
          </p>
        </div>
      )}
      {job.status === "released" && (
        <p className="muted">
          Candidate released. The local checkpoint branch remains available,
          and this issue will not be admitted again.
        </p>
      )}
    </li>
  );
}
