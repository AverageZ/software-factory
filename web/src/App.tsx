import { useEffect, useState } from "react";
import { errorMessage, href, request, useRoute } from "./api";
import { BindingPage } from "./bindings";
import { ErrorNotice } from "./components";
import { ProjectList, ProjectPage } from "./projects";
import { RunPage, RunsPage } from "./runs";
import type { Binding, Project, Run, Workflow } from "./types";
import { WorkflowList, WorkflowPage } from "./workflows";

export default function App() {
  const route = useRoute();
  const [projects, setProjects] = useState<Project[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [bindings, setBindings] = useState<Binding[]>([]);
  const [runs, setRuns] = useState<Run[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [runError, setRunError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [refreshing, setRefreshing] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    setRefreshing(true);
    Promise.all([
      request<Project[]>("/projects", { signal: controller.signal }),
      request<Workflow[]>("/workflows", { signal: controller.signal }),
      request<Binding[]>("/bindings", { signal: controller.signal }),
      request<Run[]>("/runs", { signal: controller.signal }),
    ])
      .then(([nextProjects, nextWorkflows, nextBindings, nextRuns]) => {
        if (controller.signal.aborted) return;
        setProjects(nextProjects);
        setWorkflows(nextWorkflows);
        setBindings(nextBindings);
        setRuns(nextRuns);
        setLoaded(true);
        setError("");
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setRefreshing(false);
      });
    return () => controller.abort();
  }, [refresh]);
  useEffect(() => {
    if (!loaded) return;
    const controller = new AbortController();
    let timer = 0;
    async function pollRuns() {
      try {
        const nextRuns = await request<Run[]>("/runs", {
          signal: controller.signal,
        });
        if (!controller.signal.aborted) {
          setRuns(nextRuns);
          setRunError("");
        }
      } catch (cause) {
        if (!controller.signal.aborted) setRunError(errorMessage(cause));
      }
      if (!controller.signal.aborted)
        timer = window.setTimeout(() => void pollRuns(), 3000);
    }
    timer = window.setTimeout(() => void pollRuns(), 3000);
    return () => {
      controller.abort();
      window.clearTimeout(timer);
    };
  }, [loaded]);
  useEffect(() => {
    document.title = `${route.section === "workflows" ? "Workflows" : route.section === "runs" ? "Runs" : route.section === "bindings" ? "Workflow binding" : "Projects"} · Factory`;
  }, [route.section]);

  const project = projects.find((entry) => entry.id === route.id);
  const workflow = workflows.find((entry) => entry.id === route.id);
  const binding = bindings.find((entry) => entry.id === route.id);
  const projectSaved = (saved: Project) =>
    setProjects((current) => [
      ...current.filter((entry) => entry.id !== saved.id),
      saved,
    ]);
  const workflowSaved = (saved: Workflow) =>
    setWorkflows((current) => [
      ...current.filter((entry) => entry.id !== saved.id),
      saved,
    ]);
  const bindingSaved = (saved: Binding) =>
    setBindings((current) => [
      ...current.filter((entry) => entry.id !== saved.id),
      saved,
    ]);
  const runStarted = (saved: Run) =>
    setRuns((current) => [
      ...current.filter((entry) => entry.id !== saved.id),
      saved,
    ]);
  const missing = (
    <section className="panel empty">
      <h1>Record not found</h1>
      <p>This URL does not identify a loaded project, workflow, or binding.</p>
      <button onClick={() => setRefresh((value) => value + 1)}>
        Refresh records
      </button>
      <a href={href("projects")}>Return to projects</a>
    </section>
  );

  return (
    <div className="app-shell">
      <a
        className="skip-link"
        href="#main-content"
        onClick={(event) => {
          event.preventDefault();
          document.getElementById("main-content")?.focus();
        }}
      >
        Skip to content
      </a>
      <header className="app-header">
        <a className="brand" href={href("projects")}>
          Factory<span>Local workflow execution</span>
        </a>
        <nav aria-label="Main navigation">
          <a
            href={href("projects")}
            aria-current={
              route.section === "projects" || route.section === "bindings"
                ? "page"
                : undefined
            }
          >
            Projects
          </a>
          <a
            href={href("workflows")}
            aria-current={route.section === "workflows" ? "page" : undefined}
          >
            Workflows
          </a>
          <a
            href={href("runs")}
            aria-current={route.section === "runs" ? "page" : undefined}
          >
            Runs
          </a>
        </nav>
        <button
          className="subtle"
          disabled={refreshing}
          onClick={() => setRefresh((value) => value + 1)}
        >
          {refreshing ? "Refreshing…" : "Refresh records"}
        </button>
      </header>
      <main id="main-content" tabIndex={-1}>
        <ErrorNotice
          message={error ? `Could not load Factory records. ${error}` : ""}
        />
        <ErrorNotice
          message={
            runError
              ? `Run list refresh failed; previously loaded records may be stale. ${runError}`
              : ""
          }
        />
        {!loaded ? (
          <section className="panel empty">
            <h1>
              {error
                ? "Cannot reach the local daemon"
                : "Connecting to Factory"}
            </h1>
            <p>
              {error
                ? "API requests are proxied to 127.0.0.1:8080. Start the daemon and retry."
                : "Reading saved projects, workflows, bindings, and runs."}
            </p>
            {error && (
              <button onClick={() => setRefresh((value) => value + 1)}>
                Retry connection
              </button>
            )}
          </section>
        ) : route.section === "projects" ? (
          !route.id ? (
            <ProjectList projects={projects} />
          ) : route.id === "new" || project ? (
            <ProjectPage
              key={route.id}
              project={project}
              bindings={bindings}
              workflows={workflows}
              runs={runs}
              onSaved={projectSaved}
            />
          ) : (
            missing
          )
        ) : route.section === "workflows" ? (
          !route.id ? (
            <WorkflowList workflows={workflows} />
          ) : route.id === "new" || workflow ? (
            <WorkflowPage
              key={route.id}
              workflow={workflow}
              workflows={workflows}
              onSaved={workflowSaved}
            />
          ) : (
            missing
          )
        ) : route.section === "bindings" ? (
          route.id === "new" || binding ? (
            <BindingPage
              key={`${route.id}-${route.query.get("project") || ""}`}
              binding={binding}
              initialProjectId={route.query.get("project") || ""}
              projects={projects}
              workflows={workflows}
              onSaved={bindingSaved}
              onRun={runStarted}
            />
          ) : (
            missing
          )
        ) : route.section === "runs" ? (
          route.id ? (
            <RunPage
              key={route.id}
              id={route.id}
              projects={projects}
              bindings={bindings}
            />
          ) : (
            <RunsPage runs={runs} projects={projects} />
          )
        ) : (
          missing
        )}
      </main>
      <footer>
        Factory runs real commands with your local permissions. Repository-owned
        rules, skills, and authentication stay in your repositories and harness.
      </footer>
    </div>
  );
}
