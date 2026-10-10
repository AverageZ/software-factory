import { useEffect, useState } from "react";
import { errorMessage, href, request, useRoute } from "./api";
import { ErrorNotice } from "./components";
import type { Workflow } from "./types";
import { WorkflowList, WorkflowPage } from "./workflows";

export default function Playground() {
  const route = useRoute();
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    document.title = "Workflow playground · Factory";
    if (!window.location.hash) window.location.replace(href("workflows"));
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    request<Workflow[]>("/workflows", { signal: controller.signal })
      .then((saved) => {
        if (controller.signal.aborted) return;
        setWorkflows(saved);
        setLoaded(true);
        setError("");
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [refresh]);

  const workflow = workflows.find((entry) => entry.id === route.id);
  const onSaved = (saved: Workflow) => setWorkflows((current) => [
    ...current.filter((entry) => entry.id !== saved.id), saved,
  ]);

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content" onClick={(event) => {
        event.preventDefault();
        document.getElementById("main-content")?.focus();
      }}>Skip to content</a>
      <header className="app-header">
        <a className="brand" href={href("workflows")}>
          Factory<span>UI-only workflow playground</span>
        </a>
        <nav aria-label="Main navigation">
          <a href={href("workflows")} aria-current="page">Workflows</a>
        </nav>
        <button className="subtle" onClick={() => setRefresh((value) => value + 1)}>Refresh local workflows</button>
      </header>
      <main id="main-content" tabIndex={-1}>
        <section className="notice" aria-label="Playground limits">
          <strong>Standalone playground — no daemon connection.</strong>
          <p>Saves stay in this browser on this origin. No commands, agents, GitHub actions, or workflows execute. Editor checks apply; daemon validation is not performed.</p>
        </section>
        <ErrorNotice message={error ? `Could not load browser-local workflows. ${error}` : ""} />
        {!loaded ? (
          <section className="panel empty">
            <h1>{error ? "Browser storage unavailable" : "Loading local workflows…"}</h1>
            {error && <button onClick={() => setRefresh((value) => value + 1)}>Retry loading</button>}
          </section>
        ) : route.section === "workflows" && !route.id ? (
          <WorkflowList workflows={workflows} />
        ) : route.section === "workflows" && (route.id === "new" || workflow) ? (
          <WorkflowPage key={route.id} workflow={workflow} workflows={workflows} onSaved={onSaved} />
        ) : (
          <section className="panel empty">
            <h1>Playground workflow not found</h1>
            <a href={href("workflows")}>Return to local workflows</a>
          </section>
        )}
      </main>
      <footer>Experimental definitions and graph layouts are separate from Factory daemon data. Clearing site storage removes these local saves.</footer>
    </div>
  );
}
