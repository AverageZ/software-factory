import { useEffect, useState } from "react";
import {
  artifactUrl,
  errorMessage,
  getLog,
  href,
  json,
  logUrl,
  post,
  request,
  timestamp,
} from "./api";
import { ErrorNotice, PageHeader, RunTable, Status } from "./components";
import type { Binding, Project, Run } from "./types";

export function RunsPage({
  runs,
  projects,
}: {
  runs: Run[];
  projects: Project[];
}) {
  const [projectId, setProjectId] = useState("");
  return (
    <>
      <PageHeader
        title="Runs"
        description="Recorded executions from the local daemon. Refreshes automatically; no simulated results."
      />
      <label className="field filter-field">
        <span>Project filter</span>
        <select
          value={projectId}
          onChange={(event) => setProjectId(event.target.value)}
        >
          <option value="">All projects</option>
          {projects.map((project) => (
            <option value={project.id} key={project.id}>
              {project.name}
            </option>
          ))}
        </select>
      </label>
      <section className="panel">
        <RunTable
          runs={runs.filter((run) => !projectId || run.projectId === projectId)}
        />
      </section>
    </>
  );
}

export function RunPage({
  id,
  projects,
  bindings,
}: {
  id: string;
  projects: Project[];
  bindings: Binding[];
}) {
  const [run, setRun] = useState<Run | null>(null);
  const [pollError, setPollError] = useState("");
  const [actionError, setActionError] = useState("");
  const [selectedNodeId, setSelectedNodeId] = useState("");
  const [busy, setBusy] = useState(false);
  const [reload, setReload] = useState(0);
  const [observedAt, setObservedAt] = useState("");
  useEffect(() => {
    const controller = new AbortController();
    let timer = 0;
    async function poll() {
      try {
        const recorded = await request<Run>(`/runs/${encodeURIComponent(id)}`, {
          signal: controller.signal,
        });
        if (controller.signal.aborted) return;
        setRun(recorded);
        setPollError("");
        setObservedAt(new Date().toLocaleTimeString());
        setSelectedNodeId(
          (current) =>
            current ||
            Object.values(recorded.nodes).find(
              (node) =>
                node.status === "waiting" ||
                node.status === "failed" ||
                node.status === "running",
            )?.nodeId ||
            Object.keys(recorded.nodes)[0] ||
            "",
        );
      } catch (cause) {
        if (!controller.signal.aborted) setPollError(errorMessage(cause));
      }
      if (!controller.signal.aborted)
        timer = window.setTimeout(() => void poll(), 1500);
    }
    void poll();
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [id, reload]);

  async function control(
    action: "approve" | "retry" | "cancel",
    body?: unknown,
  ) {
    setBusy(true);
    setActionError("");
    try {
      const recorded = await post<Run>(
        `/runs/${encodeURIComponent(id)}/${action}`,
        body,
      );
      setRun(recorded);
      setReload((value) => value + 1);
    } catch (cause) {
      setActionError(errorMessage(cause));
    } finally {
      setBusy(false);
    }
  }

  const node = run?.nodes[selectedNodeId];
  const [log, setLog] = useState("");
  const [logError, setLogError] = useState("");
  const [logLoading, setLogLoading] = useState(false);
  const [logReload, setLogReload] = useState(0);
  const logPath = node?.logPath;
  const nodeStatus = node?.status;
  useEffect(() => {
    setLog("");
    setLogError("");
    setLogLoading(false);
    if (!selectedNodeId || !logPath) return;
    const controller = new AbortController();
    let timer = 0;
    async function pollLog() {
      setLogLoading(true);
      try {
        const text = await getLog(id, selectedNodeId, controller.signal);
        if (!controller.signal.aborted) {
          setLog(text);
          setLogError("");
        }
      } catch (cause) {
        if (!controller.signal.aborted) setLogError(errorMessage(cause));
      } finally {
        if (!controller.signal.aborted) setLogLoading(false);
      }
      if (
        !controller.signal.aborted &&
        (nodeStatus === "running" || nodeStatus === "waiting")
      )
        timer = window.setTimeout(() => void pollLog(), 1500);
    }
    void pollLog();
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [id, selectedNodeId, logPath, nodeStatus, logReload]);

  if (!run)
    return (
      <>
        <PageHeader title="Run" />
        <ErrorNotice message={pollError} />
        {pollError ? (
          <button onClick={() => setReload((value) => value + 1)}>
            Retry connection
          </button>
        ) : (
          <p role="status">Loading recorded run…</p>
        )}
      </>
    );
  const project = projects.find((entry) => entry.id === run.projectId);
  const binding = bindings.find((entry) => entry.id === run.bindingId);
  const cancellable = ["queued", "running", "waiting"].includes(run.status);

  return (
    <>
      <PageHeader
        title={`Run ${run.id.slice(0, 12)}`}
        description={`${project?.name || run.projectId} · ${binding?.name || run.bindingId}`}
      >
        <a className="button" href={href("projects", run.projectId)}>
          Project
        </a>
        <a className="button" href={href("runs")}>
          All runs
        </a>
        {cancellable && (
          <button
            className="danger"
            disabled={busy}
            onClick={() => {
              if (
                window.confirm(
                  "Cancel this run and stop its active processes? Completed side effects are not rolled back.",
                )
              )
                void control("cancel");
            }}
          >
            Cancel run
          </button>
        )}
      </PageHeader>
      <ErrorNotice
        message={
          pollError
            ? `Live refresh failed; the visible record may be stale. ${pollError}`
            : ""
        }
      />
      <ErrorNotice message={actionError} />
      <div className="run-summary panel">
        <Status status={run.status} />
        <span>Created {timestamp(run.createdAt)}</span>
        <span>Updated {timestamp(run.updatedAt)}</span>
        <span className="muted">Observed {observedAt} · polls every 1.5s</span>
      </div>
      {run.error && <div className="notice error">{run.error}</div>}
      {run.parentRunId && (
        <p className="notice">
          Child execution of{" "}
          <a href={href("runs", run.parentRunId)}>
            parent run {run.parentRunId.slice(0, 12)}
          </a>
          .
        </p>
      )}
      <div className="run-columns">
        <section className="panel node-list">
          <h2>Node executions</h2>
          {Object.values(run.nodes).map((entry) => (
            <button
              className={`node-row ${selectedNodeId === entry.nodeId ? "active" : ""}`}
              key={entry.nodeId}
              onClick={() => setSelectedNodeId(entry.nodeId)}
            >
              <code>{entry.nodeId}</code>
              <Status status={entry.status} />
              <small>
                Attempt {entry.attempt}
                {entry.childRunId ? " · child workflow" : ""}
              </small>
            </button>
          ))}
        </section>
        <section className="panel node-detail">
          {node ? (
            <>
              <div className="section-heading">
                <h2>
                  <code>{node.nodeId}</code>
                </h2>
                <Status status={node.status} />
              </div>
              <p className="muted">
                Attempt {node.attempt} · started {timestamp(node.startedAt)} ·
                finished {timestamp(node.finishedAt)}
              </p>
              {node.error && <div className="notice error">{node.error}</div>}
              {node.status === "unavailable" && (
                <div className="notice warning">
                  This node is unavailable. Factory has not performed a decision
                  or produced a successful result.
                </div>
              )}
              {node.childRunId && (
                <p className="notice">
                  <a className="button" href={href("runs", node.childRunId)}>
                    Open child run {node.childRunId.slice(0, 12)}
                  </a>{" "}
                  Inspect its nodes and handle any approvals there.
                </p>
              )}
              {node.status === "waiting" && !node.childRunId && (
                <div className="approval-box">
                  <h3>Approval required</h3>
                  <p>
                    Review the recorded outputs, logs, and events before
                    deciding. Rejection fails this node and run.
                  </p>
                  <div className="actions">
                    <button
                      className="primary"
                      disabled={busy}
                      onClick={() =>
                        void control("approve", {
                          nodeId: node.nodeId,
                          approved: true,
                        })
                      }
                    >
                      Approve node
                    </button>
                    <button
                      className="danger"
                      disabled={busy}
                      onClick={() =>
                        void control("approve", {
                          nodeId: node.nodeId,
                          approved: false,
                        })
                      }
                    >
                      Reject node
                    </button>
                  </div>
                </div>
              )}
              {["failed", "interrupted", "unavailable", "cancelled"].includes(
                node.status,
              ) && (
                <div className="notice warning">
                  <p>
                    Retry is explicit and may repeat external side effects.
                    Completed nodes are not replayed. Runs use their saved
                    configuration snapshot.
                  </p>
                  <button
                    disabled={busy || node.status === "unavailable"}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Retry ${node.nodeId}? Any partial side effects from its previous attempt may happen again.`,
                        )
                      )
                        void control("retry", { nodeId: node.nodeId });
                    }}
                  >
                    Retry node
                  </button>
                  {node.status === "unavailable" && (
                    <p>
                      Replace the unavailable node in the workflow and start a
                      new run.
                    </p>
                  )}
                </div>
              )}
              <h3>Recorded outputs</h3>
              <pre className="json-output">{json(node.outputs)}</pre>
              <div className="section-heading">
                <h3>Process log</h3>
                {node.logPath && (
                  <div className="actions">
                    <button
                      className="small"
                      onClick={() => setLogReload((value) => value + 1)}
                    >
                      Refresh log
                    </button>
                    <a
                      href={logUrl(run.id, node.nodeId)}
                      target="_blank"
                      rel="noreferrer"
                    >
                      Open full log
                    </a>
                  </div>
                )}
              </div>
              <ErrorNotice message={logError} />
              {node.logPath ? (
                <>
                  <span className="muted" role="status">
                    {logLoading
                      ? "Reading recorded log…"
                      : "Recorded stdout / stderr"}
                  </span>
                  <pre className="process-log">
                    {log ||
                      (logLoading ? "Loading…" : "The recorded log is empty.")}
                  </pre>
                </>
              ) : (
                <p className="muted">
                  No process log has been recorded for this node.
                </p>
              )}
              <h3>Saved artifacts</h3>
              {node.artifacts.length ? (
                <ul className="artifact-list">
                  {node.artifacts.map((name) => (
                    <li key={name}>
                      <a
                        href={artifactUrl(run.id, node.nodeId, name)}
                        download={name}
                      >
                        {name}
                      </a>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="muted">No artifacts recorded.</p>
              )}
            </>
          ) : (
            <p className="empty">
              Select a node to inspect its recorded execution.
            </p>
          )}
        </section>
      </div>
      <section className="panel events-panel">
        <h2>Run events</h2>
        {run.events.length ? (
          <ol className="event-list">
            {run.events.map((event, index) => (
              <li key={`${event.time}-${index}`}>
                <time>{timestamp(event.time)}</time>
                <strong>{event.type}</strong>
                {event.nodeId && (
                  <button
                    className="text-button"
                    onClick={() => setSelectedNodeId(event.nodeId || "")}
                  >
                    {event.nodeId}
                  </button>
                )}
                <p>{event.message}</p>
              </li>
            ))}
          </ol>
        ) : (
          <p className="muted">No events recorded.</p>
        )}
      </section>
      <details className="panel">
        <summary>Run metadata and typed inputs</summary>
        <dl>
          <dt>Run ID</dt>
          <dd>
            <code>{run.id}</code>
          </dd>
          <dt>Workflow ID</dt>
          <dd>
            <code>{run.workflowId}</code>
          </dd>
          <dt>Binding ID</dt>
          <dd>
            <code>{run.bindingId}</code>
          </dd>
        </dl>
        <pre className="json-output">{json(run.inputs)}</pre>
        <p className="muted">
          Execution uses the daemon's snapshot of the workflow, binding, and
          project settings, not later edits.
        </p>
      </details>
    </>
  );
}
