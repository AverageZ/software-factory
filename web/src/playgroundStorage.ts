import type { Layout, Workflow } from "./types";

// Separate from daemon state; the playground never sends an API request.
const storageKey = "factory.workflow-playground.v1";
type Store = { workflows: Workflow[]; layouts: Record<string, Layout> };

function load(): Store {
  const stored = localStorage.getItem(storageKey);
  return stored ? JSON.parse(stored) as Store : { workflows: [], layouts: {} };
}

export function playgroundRequest(path: string, options?: RequestInit): unknown {
  options?.signal?.throwIfAborted();
  const method = options?.method || "GET";
  const layoutPath = /^\/workflows\/([^/]+)\/layout$/.exec(path);
  if (path === "/workflows" && method === "GET") return load().workflows;
  if (path === "/workflows" && method === "POST") {
    const workflow = JSON.parse(String(options?.body)) as Workflow;
    const saved = { ...workflow, id: workflow.id || crypto.randomUUID() };
    const store = load();
    store.workflows = [...store.workflows.filter((entry) => entry.id !== saved.id), saved];
    // Storage failures propagate rather than claiming a save worked.
    localStorage.setItem(storageKey, JSON.stringify(store));
    return saved;
  }
  if (layoutPath && (method === "GET" || method === "PUT")) {
    const id = decodeURIComponent(layoutPath[1]);
    const store = load();
    if (!store.workflows.some((workflow) => workflow.id === id))
      throw new Error("Playground workflow not found.");
    if (method === "GET")
      return Object.hasOwn(store.layouts, id) ? store.layouts[id] : { nodes: {} };
    const layout = JSON.parse(String(options?.body)) as Layout;
    store.layouts[id] = layout;
    localStorage.setItem(storageKey, JSON.stringify(store));
    return layout;
  }
  throw new Error("The UI-only playground supports workflow editing only. Execution and daemon APIs are unavailable.");
}
