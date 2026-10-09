import { useCallback, useEffect, useRef, useState } from "react";
import {
  Background,
  Controls,
  Handle,
  Position,
  ReactFlow,
  useEdgesState,
  useNodesState,
} from "@xyflow/react";
import type {
  Connection,
  Edge as FlowEdge,
  Node as FlowNode,
  NodeProps,
  Viewport,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { errorMessage, href, json, parseObject, post, request } from "./api";
import { ErrorNotice, JsonField, PageHeader } from "./components";
import { nodeKinds, valueTypes } from "./types";
import type { Input, Layout, NodeKind, Workflow, WorkflowNode } from "./types";

type EditorNode = FlowNode<
  { node: WorkflowNode; configText: string; inputText: string },
  "factory"
>;
type EditorEdge = FlowEdge<{ when?: "true" | "false" }>;

const kindDescriptions: Record<NodeKind, string> = {
  agent:
    "Runs the project harness in a mapped repository. Supply a workflow pre-prompt, not repository rules or system settings.",
  tool: "Runs a real repository tool using an argv array. Typed inputs are exposed in FACTORY_INPUTS.",
  command:
    "Runs an argv array in a mapped repository. No shell expansion unless you explicitly use a shell.",
  decision:
    "Unavailable. No decision provider is configured; execution fails explicitly. Use branch for deterministic routing.",
  approval:
    "Waits for an explicit approve or reject action. Rejection fails the run.",
  validation:
    "Runs a validation argv array in a mapped repository. A nonzero exit fails the node.",
  branch:
    "Compares one named resolved input to a JSON value. Label outgoing edges true or false.",
  parallel:
    "Explicit fan-out/barrier. All predecessors must complete; project maxParallel limits concurrency.",
  workflow:
    "Executes a real child workflow with inherited repository mappings. Node inputs become child workflow inputs.",
  integration: "Makes a real HTTP request. Non-2xx responses fail the node.",
};
const configHelp: Record<NodeKind, string> = {
  agent:
    "prompt (string), timeoutSeconds (optional number), artifacts (optional array of repository-relative paths).",
  tool: "command (argv string array), timeoutSeconds (optional number), artifacts (optional repository-relative paths).",
  command:
    "command (argv string array), timeoutSeconds (optional number), artifacts (optional repository-relative paths).",
  decision:
    "Not configurable for execution. This node remains visibly unavailable.",
  approval: "message (string) shown to the person reviewing this approval.",
  validation:
    "command (argv string array), timeoutSeconds (optional number), artifacts (optional repository-relative paths).",
  branch: "input (the name of a node input), equals (any JSON value).",
  parallel: "No configuration required: {}.",
  workflow:
    "workflowId (the saved child workflow ID). Recursive definitions are rejected.",
  integration:
    "url, optional method, headers object, body JSON value, timeoutSeconds.",
};

function defaultConfig(kind: NodeKind): Record<string, unknown> {
  switch (kind) {
    case "agent":
      return { prompt: "" };
    case "tool":
    case "command":
    case "validation":
      return { command: [] };
    case "approval":
      return { message: "" };
    case "branch":
      return { input: "", equals: true };
    case "workflow":
      return { workflowId: "" };
    case "integration":
      return { url: "", method: "GET" };
    default:
      return {};
  }
}

function WorkflowCardNode({ data, selected }: NodeProps<EditorNode>) {
  return (
    <div
      className={`workflow-node ${selected ? "selected" : ""} ${data.node.kind === "decision" ? "unavailable" : ""}`}
    >
      <Handle type="target" position={Position.Left} />
      <span className="node-kind">
        {data.node.kind}
        {data.node.kind === "decision" ? " · unavailable" : ""}
      </span>
      <strong>{data.node.name || data.node.id}</strong>
      <code>{data.node.id}</code>
      {data.node.repository && (
        <small>Repository: {data.node.repository}</small>
      )}
      <span className="node-output">
        result: {data.node.outputType || "object"}
      </span>
      <Handle type="source" position={Position.Right} />
    </div>
  );
}
const flowNodeTypes = { factory: WorkflowCardNode };

export function WorkflowList({ workflows }: { workflows: Workflow[] }) {
  return (
    <>
      <PageHeader
        title="Reusable workflows"
        description="Typed execution graphs, independent of project repository paths. Positions are stored separately from workflow logic."
      >
        <a className="button primary" href={href("workflows", "new")}>
          New workflow
        </a>
      </PageHeader>
      {workflows.length ? (
        <div className="card-grid">
          {workflows.map((workflow) => (
            <a
              className="project-card"
              key={workflow.id}
              href={href("workflows", workflow.id)}
            >
              <h2>{workflow.name}</h2>
              <p>
                {workflow.nodes.length} nodes · {workflow.edges.length} edges ·{" "}
                {Object.keys(workflow.inputs || {}).length} workflow inputs
              </p>
              {workflow.nodes.some((node) => node.kind === "decision") && (
                <span className="status status-unavailable">
                  Contains unavailable decision node
                </span>
              )}
            </a>
          ))}
        </div>
      ) : (
        <section className="panel empty">
          <h2>No workflows yet</h2>
          <p>
            Create nodes on a graph, connect dependencies, then bind repository
            slots to a project.
          </p>
          <a className="button" href={href("workflows", "new")}>
            Create workflow
          </a>
        </section>
      )}
    </>
  );
}

export function WorkflowPage({
  workflow,
  workflows,
  onSaved,
}: {
  workflow?: Workflow;
  workflows: Workflow[];
  onSaved: (workflow: Workflow) => void;
}) {
  const [layout, setLayout] = useState<Layout | null>(
    workflow ? null : { nodes: {} },
  );
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const id = workflow?.id;
  useEffect(() => {
    if (!id) return;
    const controller = new AbortController();
    setError("");
    request<Layout>(`/workflows/${encodeURIComponent(id)}/layout`, {
      signal: controller.signal,
    })
      .then(setLayout)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(cause));
      });
    return () => controller.abort();
  }, [id, reload]);
  if (!layout)
    return (
      <>
        <PageHeader title={workflow?.name || "Workflow"} />
        <ErrorNotice message={error} />
        {error ? (
          <button onClick={() => setReload(reload + 1)}>
            Retry loading layout
          </button>
        ) : (
          <p role="status">Loading saved graph layout…</p>
        )}
      </>
    );
  return (
    <WorkflowEditor
      workflow={workflow}
      workflows={workflows}
      layout={layout}
      onSaved={onSaved}
    />
  );
}

function WorkflowEditor({
  workflow,
  workflows,
  layout,
  onSaved,
}: {
  workflow?: Workflow;
  workflows: Workflow[];
  layout: Layout;
  onSaved: (workflow: Workflow) => void;
}) {
  const [savedId, setSavedId] = useState(workflow?.id || "");
  const [name, setName] = useState(workflow?.name || "");
  const [inputDeclarations, setInputDeclarations] = useState(
    json(workflow?.inputs || {}),
  );
  const [nodes, setNodes, onNodesChange] = useNodesState<EditorNode>(
    (workflow?.nodes || []).map((node, index) => ({
      id: node.id,
      type: "factory",
      position: layout.nodes[node.id] || {
        x: 70 + (index % 3) * 280,
        y: 70 + Math.floor(index / 3) * 180,
      },
      data: {
        node,
        configText: json(node.config),
        inputText: json(node.inputs || {}),
      },
    })),
  );
  const [edges, setEdges, onEdgesChange] = useEdgesState<EditorEdge>(
    (workflow?.edges || []).map((edge) => ({
      id: edge.id,
      source: edge.source,
      target: edge.target,
      label: edge.when,
      data: { when: edge.when },
    })),
  );
  const [selectedNodeId, setSelectedNodeId] = useState("");
  const [selectedEdgeId, setSelectedEdgeId] = useState("");
  const [addKind, setAddKind] = useState<NodeKind>("agent");
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const [saving, setSaving] = useState(false);
  const viewport = useRef<Viewport>(layout.viewport || { x: 0, y: 0, zoom: 1 });
  const selectedNode = nodes.find((node) => node.id === selectedNodeId);
  const selectedEdge = edges.find((edge) => edge.id === selectedEdgeId);
  const selectedSource =
    selectedEdge && nodes.find((node) => node.id === selectedEdge.source);

  function addNode() {
    const id = `${addKind}_${crypto.randomUUID().slice(0, 8)}`;
    const node: WorkflowNode = {
      id,
      name: addKind === "decision" ? "Unavailable decision" : addKind,
      kind: addKind,
      outputType:
        addKind === "approval"
          ? "Approval"
          : addKind === "branch"
            ? "boolean"
            : "object",
      config: defaultConfig(addKind),
    };
    const { x, y, zoom } = viewport.current;
    setNodes((current) => [
      ...current.map((entry) => ({ ...entry, selected: false })),
      {
        id,
        type: "factory",
        selected: true,
        position: {
          x: (70 - x) / zoom + (current.length % 3) * 275,
          y: (70 - y) / zoom + Math.floor(current.length / 3) * 190,
        },
        data: { node, configText: json(node.config), inputText: "{}" },
      },
    ]);
    setSelectedNodeId(id);
    setSelectedEdgeId("");
    setMessage("");
  }

  function updateNode(data: Partial<EditorNode["data"]>) {
    setNodes((current) =>
      current.map((node) =>
        node.id === selectedNodeId
          ? { ...node, data: { ...node.data, ...data } }
          : node,
      ),
    );
    setMessage("");
  }

  function changeKind(kind: NodeKind) {
    if (!selectedNode) return;
    updateNode({
      node: {
        ...selectedNode.data.node,
        kind,
        outputType:
          kind === "approval"
            ? "Approval"
            : kind === "branch"
              ? "boolean"
              : "object",
      },
      configText: json(defaultConfig(kind)),
    });
    setEdges((current) =>
      current.map((edge) =>
        edge.source === selectedNodeId
          ? {
              ...edge,
              data: { when: kind === "branch" ? "true" : undefined },
              label: kind === "branch" ? "true" : undefined,
            }
          : edge,
      ),
    );
  }

  const connect = useCallback(
    (connection: Connection) => {
      if (!connection.source || !connection.target) return;
      if (
        connection.source === connection.target ||
        edges.some(
          (edge) =>
            edge.source === connection.source &&
            edge.target === connection.target,
        )
      ) {
        setError("Connect distinct nodes without duplicating an edge.");
        return;
      }
      const pending = [connection.target];
      const seen = new Set<string>();
      while (pending.length) {
        const id = pending.pop()!;
        if (id === connection.source) {
          setError(
            "This connection would create a cycle. Workflows must be DAGs.",
          );
          return;
        }
        if (seen.has(id)) continue;
        seen.add(id);
        pending.push(
          ...edges
            .filter((edge) => edge.source === id)
            .map((edge) => edge.target),
        );
      }
      const when =
        nodes.find((node) => node.id === connection.source)?.data.node.kind ===
        "branch"
          ? "true"
          : undefined;
      const id = crypto.randomUUID();
      setEdges((current) => [
        ...current,
        {
          id,
          source: connection.source,
          target: connection.target,
          data: { when },
          label: when,
        },
      ]);
      setSelectedEdgeId(id);
      setSelectedNodeId("");
      setError("");
      setMessage("");
    },
    [edges, nodes, setEdges],
  );

  function removeSelection() {
    if (selectedNodeId) {
      setNodes((current) =>
        current.filter((node) => node.id !== selectedNodeId),
      );
      setEdges((current) =>
        current.filter(
          (edge) =>
            edge.source !== selectedNodeId && edge.target !== selectedNodeId,
        ),
      );
      setSelectedNodeId("");
    } else if (selectedEdgeId) {
      setEdges((current) =>
        current.filter((edge) => edge.id !== selectedEdgeId),
      );
      setSelectedEdgeId("");
    }
    setMessage("");
  }

  async function save(layoutOnly: boolean) {
    setSaving(true);
    setError("");
    setMessage("");
    let id = savedId;
    let graphSaved = false;
    try {
      if (!layoutOnly) {
        const declarations = parseObject<unknown>(
          inputDeclarations,
          "Workflow input declarations",
        );
        for (const [inputName, type] of Object.entries(declarations))
          if (
            !inputName.trim() ||
            typeof type !== "string" ||
            !valueTypes.some((entry) => entry === type)
          )
            throw new Error(
              `Workflow input “${inputName}” requires a supported type name.`,
            );
        const workflowInputs = Object.fromEntries(
          Object.entries(declarations).map(([key, type]) => [
            key,
            String(type),
          ]),
        );
        const workflowNodes = nodes.map(({ data }) => {
          const inputs = parseObject<Input>(
            data.inputText,
            `Inputs for ${data.node.name}`,
          );
          for (const [inputName, input] of Object.entries(inputs)) {
            if (
              !input ||
              typeof input !== "object" ||
              !valueTypes.some((type) => type === input.type)
            )
              throw new Error(
                `Node ${data.node.name}, input ${inputName}: supply a supported type.`,
              );
            if (Object.hasOwn(input, "value") === Object.hasOwn(input, "from"))
              throw new Error(
                `Node ${data.node.name}, input ${inputName}: provide exactly one of value or from.`,
              );
            if (
              Object.hasOwn(input, "from") &&
              (typeof input.from !== "string" || !input.from.includes("."))
            )
              throw new Error(
                `Node ${data.node.name}, input ${inputName}: from must be inputs.NAME or NODEID.OUTPUT.`,
              );
          }
          return {
            ...data.node,
            inputs,
            config: parseObject<unknown>(
              data.configText,
              `Configuration for ${data.node.name}`,
            ),
          };
        });
        const saved = await post<Workflow>("/workflows", {
          id,
          name,
          inputs: workflowInputs,
          nodes: workflowNodes,
          edges: edges.map((edge) => ({
            id: edge.id,
            source: edge.source,
            target: edge.target,
            ...(edge.data?.when ? { when: edge.data.when } : {}),
          })),
        });
        id = saved.id;
        setSavedId(id);
        onSaved(saved);
        graphSaved = true;
      }
      if (!id) throw new Error("Save the workflow before saving its layout.");
      await request<Layout>(`/workflows/${encodeURIComponent(id)}/layout`, {
        method: "PUT",
        body: JSON.stringify({
          nodes: Object.fromEntries(
            nodes.map((node) => [node.id, node.position]),
          ),
          viewport: viewport.current,
        }),
      });
      setMessage(
        layoutOnly
          ? "Layout saved separately. Workflow logic was not changed."
          : "Workflow and separate graph layout saved.",
      );
      if (!workflow) window.location.hash = href("workflows", id);
    } catch (cause) {
      setError(
        `${graphSaved ? "Workflow saved, but layout was not saved. Retry “Save layout only”. " : ""}${errorMessage(cause)}`,
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title={workflow ? workflow.name : "New workflow"}
        description="Drag nodes to arrange. Connect right handles to left handles. Select a node or edge to configure it."
      >
        <a className="button" href={href("workflows")}>
          All workflows
        </a>
      </PageHeader>
      <ErrorNotice message={error} />
      {message && (
        <div className="notice" role="status">
          {message}
        </div>
      )}
      <div className="workflow-toolbar">
        <label className="field">
          <span>Workflow name</span>
          <input
            aria-label="Workflow name"
            value={name}
            onChange={(event) => {
              setName(event.target.value);
              setMessage("");
            }}
          />
        </label>
        <label className="field">
          <span>Node kind</span>
          <select
            value={addKind}
            onChange={(event) => setAddKind(event.target.value as NodeKind)}
          >
            {nodeKinds.map((kind) => (
              <option key={kind} value={kind}>
                {kind === "decision" ? "decision — unavailable" : kind}
              </option>
            ))}
          </select>
        </label>
        <button onClick={addNode}>Add node</button>
        <button
          className="danger"
          disabled={!selectedNode && !selectedEdge}
          onClick={removeSelection}
        >
          Delete selected
        </button>
        <div className="toolbar-spacer" />
        <button disabled={saving || !savedId} onClick={() => void save(true)}>
          Save layout only
        </button>
        <button
          className="primary"
          disabled={saving}
          onClick={() => void save(false)}
        >
          {saving ? "Saving…" : "Save workflow"}
        </button>
      </div>
      <div className="workflow-workspace">
        <div className="graph-canvas" aria-label="Workflow graph editor">
          <ReactFlow<EditorNode, EditorEdge>
            nodes={nodes}
            edges={edges}
            nodeTypes={flowNodeTypes}
            onNodesChange={onNodesChange}
            onEdgesChange={onEdgesChange}
            onConnect={connect}
            onNodeClick={(_, node) => {
              setSelectedNodeId(node.id);
              setSelectedEdgeId("");
            }}
            onEdgeClick={(_, edge) => {
              setSelectedEdgeId(edge.id);
              setSelectedNodeId("");
            }}
            onPaneClick={() => {
              setSelectedNodeId("");
              setSelectedEdgeId("");
            }}
            onMoveEnd={(_, nextViewport) => {
              viewport.current = nextViewport;
            }}
            defaultViewport={layout.viewport}
            fitView={!layout.viewport && nodes.length > 0}
            minZoom={0.2}
            maxZoom={2}
            deleteKeyCode={["Backspace", "Delete"]}
          >
            <Background gap={20} />
            <Controls />
          </ReactFlow>
          {!nodes.length && (
            <div className="canvas-empty">
              Choose a node kind above and add your first node.
            </div>
          )}
        </div>
        <aside className="node-inspector" aria-label="Graph selection settings">
          {selectedNode ? (
            <div className="stack">
              <h2>Node settings</h2>
              <label className="field">
                <span>Node ID</span>
                <input readOnly value={selectedNode.id} />
                <small>Use this ID in input references.</small>
              </label>
              <label className="field">
                <span>Node name</span>
                <input
                  value={selectedNode.data.node.name}
                  onChange={(event) =>
                    updateNode({
                      node: {
                        ...selectedNode.data.node,
                        name: event.target.value,
                      },
                    })
                  }
                />
              </label>
              <label className="field">
                <span>Kind</span>
                <select
                  value={selectedNode.data.node.kind}
                  onChange={(event) =>
                    changeKind(event.target.value as NodeKind)
                  }
                >
                  {nodeKinds.map((kind) => (
                    <option key={kind} value={kind}>
                      {kind === "decision" ? "decision — unavailable" : kind}
                    </option>
                  ))}
                </select>
                <small>
                  Changing kind resets its configuration and output type.
                </small>
              </label>
              <p
                className={
                  selectedNode.data.node.kind === "decision"
                    ? "notice warning"
                    : "muted"
                }
              >
                {kindDescriptions[selectedNode.data.node.kind]}
              </p>
              <label className="field">
                <span>Repository slot</span>
                <input
                  value={selectedNode.data.node.repository || ""}
                  onChange={(event) =>
                    updateNode({
                      node: {
                        ...selectedNode.data.node,
                        repository: event.target.value || undefined,
                      },
                    })
                  }
                  placeholder="Logical slot name"
                />
                <small>
                  Required for agent, command, tool, and validation. Bind this
                  slot to a repository in each project.
                </small>
              </label>
              <label className="field">
                <span>Result output type</span>
                <select
                  value={selectedNode.data.node.outputType || "object"}
                  onChange={(event) =>
                    updateNode({
                      node: {
                        ...selectedNode.data.node,
                        outputType: event.target.value,
                      },
                    })
                  }
                >
                  {valueTypes.map((type) => (
                    <option key={type}>{type}</option>
                  ))}
                </select>
                <small>
                  Output name is result. Approvals produce Approval; branches
                  produce boolean.
                </small>
              </label>
              <JsonField
                label="Node inputs (JSON)"
                value={selectedNode.data.inputText}
                onChange={(inputText) => updateNode({ inputText })}
                help={
                  'Object keyed by input name: {"type":"string","from":"inputs.NAME"} or {"type":"object","from":"NODEID.result"} or {"type":"boolean","value":true}.'
                }
                rows={7}
              />
              <JsonField
                label="Configuration (JSON)"
                value={selectedNode.data.configText}
                onChange={(configText) => updateNode({ configText })}
                help={configHelp[selectedNode.data.node.kind]}
                rows={9}
              />
              {selectedNode.data.node.kind === "workflow" && (
                <details>
                  <summary>Available child workflow IDs</summary>
                  <ul className="compact-list">
                    {workflows
                      .filter((entry) => entry.id !== savedId)
                      .map((entry) => (
                        <li key={entry.id}>
                          {entry.name}
                          <code>{entry.id}</code>
                        </li>
                      ))}
                  </ul>
                </details>
              )}
              <button className="danger" onClick={removeSelection}>
                Delete node and its edges
              </button>
            </div>
          ) : selectedEdge ? (
            <div className="stack">
              <h2>Edge settings</h2>
              <dl>
                <dt>From</dt>
                <dd>
                  <code>{selectedEdge.source}</code>
                </dd>
                <dt>To</dt>
                <dd>
                  <code>{selectedEdge.target}</code>
                </dd>
              </dl>
              {selectedSource?.data.node.kind === "branch" ? (
                <label className="field">
                  <span>Branch condition</span>
                  <select
                    value={selectedEdge.data?.when || ""}
                    onChange={(event) => {
                      const when =
                        event.target.value === "true" ? "true" : "false";
                      setEdges((current) =>
                        current.map((edge) =>
                          edge.id === selectedEdgeId
                            ? { ...edge, data: { when }, label: when }
                            : edge,
                        ),
                      );
                      setMessage("");
                    }}
                  >
                    <option value="" disabled>
                      Select condition
                    </option>
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                  <small>
                    This path is selected only when the branch result matches.
                  </small>
                </label>
              ) : (
                <p className="muted">
                  Unconditional dependency. Conditions are only available on
                  edges leaving a branch node.
                </p>
              )}
              <button className="danger" onClick={removeSelection}>
                Delete edge
              </button>
            </div>
          ) : (
            <>
              <h2>Graph configuration</h2>
              <p className="muted">
                Select a node to configure its type, repository slot, inputs,
                and output. Select an edge to set branch conditions. Delete
                removes the selected graph element.
              </p>
              <p className="muted">
                Every node waits for its predecessors. Independent nodes can
                execute concurrently up to the project's limit.
              </p>
            </>
          )}
        </aside>
      </div>
      <section className="panel workflow-inputs">
        <h2>Workflow input declaration</h2>
        <JsonField
          label="Input names and types (JSON)"
          value={inputDeclarations}
          onChange={(value) => {
            setInputDeclarations(value);
            setMessage("");
          }}
          rows={5}
          help={
            'Map input names to type names, for example {"request":"string"}. Nodes reference them with inputs.request. Supported types: ' +
            valueTypes.join(", ") +
            "."
          }
        />
      </section>
    </>
  );
}
