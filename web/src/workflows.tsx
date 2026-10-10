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
import { StageEditor } from "./workflowStageEditor";
import { stagesFromWorkflow } from "./workflowStages";
import { DeclarationEditor } from "./workflowDeclarationEditor";
import { workflowActions } from "./declarative";
import type { Input, Layout, NodeKind, Workflow, WorkflowNode } from "./types";

type EditorNode = FlowNode<
  { node: WorkflowNode; configText: string; inputText: string },
  "factory"
>;
type EditorEdge = FlowEdge<{ when?: "true" | "false" }>;

const kindDescriptions: Record<NodeKind, string> = {
  agent:
    "Runs the project's AI tool in the selected repository. Write task instructions. Do not put repository rules or system settings here.",
  tool: "Runs a repository tool. Give the command and each argument as a separate string. Run inputs are available in FACTORY_INPUTS.",
  command:
    "Runs a command in the selected repository. Give the command and each argument as a separate string. Shell expansion works only if you run a shell explicitly.",
  decision:
    "Unavailable. No decision provider is configured. This task fails if it runs. Use a branch to choose a path from values.",
  approval:
    "Waits for a person to approve or reject. Rejection fails the run.",
  validation:
    "Runs a check command in the selected repository. A nonzero exit code fails the task.",
  branch:
    "Checks a value or a group of conditions. Set each outgoing line to true or false.",
  parallel:
    "Starts separate paths. Waits for all tasks connected before it. The project's maxParallel setting limits how many tasks can run at once.",
  workflow:
    "Runs another saved workflow. Uses the same repository mappings. Task inputs become run inputs for that workflow.",
  integration: "Sends an HTTP request. A response outside 200–299 fails the task.",
};
const configHelp: Record<NodeKind, string> = {
  agent:
    "prompt: task instructions (string). Optional fields: outputJson (boolean; require a JSON object on standard output, available as result.data), timeoutSeconds (number), artifacts (array of paths relative to the repository).",
  tool: 'command: an array of strings, with the program first and each argument separate, for example ["go","test","./..."]. Optional fields: outputJson (boolean; require a JSON object on standard output, available as result.data), timeoutSeconds (number), artifacts (array of paths relative to the repository).',
  command:
    'command: an array of strings, with the program first and each argument separate, for example ["go","test","./..."]. Optional fields: outputJson (boolean; require a JSON object on standard output, available as result.data), timeoutSeconds (number), artifacts (array of paths relative to the repository).',
  decision:
    "This task is unavailable. Configuration cannot make it run.",
  approval: "message: instructions for the person who reviews the result (string).",
  validation:
    'command: an array of strings, with the program first and each argument separate, for example ["go","test","./..."]. Optional fields: outputJson (boolean; require a JSON object on standard output, available as result.data), timeoutSeconds (number), artifacts (array of paths relative to the repository).',
  branch: 'For a single equality check, use input (input name) and equals (value to compare). For a group, use combinator "all" (every condition) or "any" (at least one condition), and rules (an array of conditions or nested groups). Each condition uses input, optional property (a field in the value), operator (equals, notEquals, exists, notExists, greaterThan, greaterOrEqual, lessThan, lessOrEqual), and value for comparisons.',
  parallel: "Use an empty JSON object: {}.",
  workflow:
    "workflowId: the ID of another saved workflow. A workflow cannot call itself, directly or through another workflow.",
  integration:
    "url: request address. Optional fields: method (HTTP method), headers (JSON object), body (JSON value), timeoutSeconds (number).",
};
const kindLabels: Record<NodeKind, string> = {
  agent: "AI task",
  tool: "Tool task",
  command: "Command task",
  decision: "Decision — unavailable",
  approval: "Approval task",
  validation: "Check task",
  branch: "Branch",
  parallel: "Parallel paths",
  workflow: "Run another workflow",
  integration: "HTTP request",
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
        {kindLabels[data.node.kind]}
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
        title="Workflows"
        description="A workflow is saved instructions for Factory. Tasks can ask AI to do work, run a command, or wait for approval. To start a workflow manually, choose a project and repository."
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
                Tasks: {workflow.declaration ? workflow.declaration.steps.length : workflowActions(workflow).filter((node) => node.kind !== "branch" && node.kind !== "parallel").length}
              </p>
              {workflowActions(workflow).some((node) => node.kind === "decision") && (
                <span className="status status-unavailable">
                  Contains an unavailable decision task
                </span>
              )}
            </a>
          ))}
        </div>
      ) : (
        <section className="panel empty">
          <h2>No workflows yet</h2>
          <p>
            Name the workflow. Write the task instructions. Save the workflow.
            Then choose a project and repository before you start it.
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
  const [editor, setEditor] = useState<"declaration" | "stages" | "graph">(
    !workflow || workflow.declaration ? "declaration" : stagesFromWorkflow(workflow) ? "stages" : "graph",
  );
  const [layout, setLayout] = useState<Layout | null>(
    workflow ? null : { nodes: {} },
  );
  const [error, setError] = useState("");
  const [reload, setReload] = useState(0);
  const id = workflow?.id;
  useEffect(() => {
    if (!id || editor === "declaration") return;
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
  }, [id, reload, editor]);
  if (editor === "declaration")
    return <DeclarationEditor workflow={workflow} workflows={workflows} onSaved={onSaved} />;
  if (editor === "stages")
    return (
      <StageEditor
        workflow={workflow}
        workflows={workflows}
        onSaved={onSaved}
        onGraph={() => setEditor("graph")}
      />
    );
  if (!layout)
    return (
      <>
        <PageHeader title={workflow?.name || "Workflow"} />
        <ErrorNotice message={error} />
        {error ? (
          <button onClick={() => setReload(reload + 1)}>
            Retry loading box positions
          </button>
        ) : (
          <p role="status">Loading saved box positions…</p>
        )}
      </>
    );
  return (
    <>
      {(!workflow || stagesFromWorkflow(workflow)) && (
        <div className="actions">
          <button onClick={() => setEditor("stages")}>Edit steps</button>
        </div>
      )}
      <WorkflowEditor
        workflow={workflow}
        workflows={workflows}
        layout={layout}
        onSaved={onSaved}
      />
    </>
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
      name: kindLabels[addKind],
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
        setError("Connect two different tasks. Do not add the same connection twice.");
        return;
      }
      const pending = [connection.target];
      const seen = new Set<string>();
      while (pending.length) {
        const id = pending.pop()!;
        if (id === connection.source) {
          setError(
            "This connection would create a loop. A task cannot connect back to itself, directly or through other tasks.",
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
          "Run input definitions",
        );
        for (const [inputName, type] of Object.entries(declarations))
          if (
            !inputName.trim() ||
            typeof type !== "string" ||
            !valueTypes.some((entry) => entry === type)
          )
            throw new Error(
              `Run input “${inputName}” needs a supported type name.`,
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
                `Task ${data.node.name}, input ${inputName}: use a supported type.`,
              );
            if (Object.hasOwn(input, "value") === Object.hasOwn(input, "from"))
              throw new Error(
                `Task ${data.node.name}, input ${inputName}: provide exactly one of value or from.`,
              );
            if (
              Object.hasOwn(input, "from") &&
              (typeof input.from !== "string" || !input.from.includes("."))
            )
              throw new Error(
                `Task ${data.node.name}, input ${inputName}: from must be inputs.NAME or NODEID.OUTPUT.`,
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
      if (!id) throw new Error("Save the workflow before saving box positions.");
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
          ? "Box positions saved. Task instructions and run order did not change."
          : "Workflow and box positions saved.",
      );
      if (!workflow) window.location.hash = href("workflows", id);
    } catch (cause) {
      setError(
        `${graphSaved ? "Workflow saved, but box positions were not saved. Retry “Save positions only”. " : ""}${errorMessage(cause)}`,
      );
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <PageHeader
        title={workflow ? workflow.name : "New workflow"}
        description="Advanced: edit connections. Boxes are tasks. Lines show run order. Drag boxes to move them. Connect a right dot to a left dot. Select a box or line to edit it."
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
          <span>Task type</span>
          <select
            value={addKind}
            onChange={(event) => setAddKind(event.target.value as NodeKind)}
          >
            {nodeKinds.map((kind) => (
              <option key={kind} value={kind}>
                {kindLabels[kind]}
              </option>
            ))}
          </select>
        </label>
        <button onClick={addNode}>Add task</button>
        <button
          className="danger"
          disabled={!selectedNode && !selectedEdge}
          onClick={removeSelection}
        >
          Delete selected
        </button>
        <div className="toolbar-spacer" />
        <button disabled={saving || !savedId} onClick={() => void save(true)}>
          Save positions only
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
        <div className="graph-canvas" aria-label="Advanced: edit connections">
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
              Name the workflow. Choose a task type and add a task. Write its instructions and save. Then choose a project and repository before you start it.
            </div>
          )}
        </div>
        <aside className="node-inspector" aria-label="Task and connection settings">
          {selectedNode ? (
            <div className="stack">
              <h2>Task settings</h2>
              <label className="field">
                <span>Task ID</span>
                <input readOnly value={selectedNode.id} />
                <small>Use this ID to read this task's result in another task's inputs.</small>
              </label>
              <label className="field">
                <span>Task name</span>
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
                <span>Task type</span>
                <select
                  value={selectedNode.data.node.kind}
                  onChange={(event) =>
                    changeKind(event.target.value as NodeKind)
                  }
                >
                  {nodeKinds.map((kind) => (
                    <option key={kind} value={kind}>
                      {kindLabels[kind]}
                    </option>
                  ))}
                </select>
                <small>
                  Changing the type resets the configuration and result type.
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
                <span>Repository name</span>
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
                  placeholder="primary"
                />
                <small>
                  Required for AI, command, tool, and check tasks. Map this
                  name to a repository in each project.
                </small>
              </label>
              <label className="field">
                <span>Result type</span>
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
                  The output is named result. Approval tasks return Approval.
                  Branches return boolean (true or false).
                </small>
              </label>
              <JsonField
                label="Task inputs (JSON)"
                value={selectedNode.data.inputText}
                onChange={(inputText) => updateNode({ inputText })}
                help={
                  'Use a JSON object with one entry per input name. Each entry needs type (value type) and exactly one of value (fixed value) or from (reference). Example: {"request":{"type":"string","from":"inputs.NAME"}}. Use inputs.NAME for a run input or NODEID.result for a task result. A fixed value example is {"ready":{"type":"boolean","value":true}}.'
                }
                rows={7}
              />
              <JsonField
                label="Task configuration (JSON)"
                value={selectedNode.data.configText}
                onChange={(configText) => updateNode({ configText })}
                help={configHelp[selectedNode.data.node.kind]}
                rows={9}
              />
              {selectedNode.data.node.kind === "workflow" && (
                <details>
                  <summary>Saved workflow IDs</summary>
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
                Delete task and its connections
              </button>
            </div>
          ) : selectedEdge ? (
            <div className="stack">
              <h2>Connection settings</h2>
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
                  <span>Use this line when the branch returns</span>
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
                      Select result
                    </option>
                    <option value="true">true</option>
                    <option value="false">false</option>
                  </select>
                  <small>
                    Factory uses this line only when the branch result matches.
                  </small>
                </label>
              ) : (
                <p className="muted">
                  This line sets the run order. Only lines from a branch can
                  have a condition.
                </p>
              )}
              <button className="danger" onClick={removeSelection}>
                Delete connection
              </button>
            </div>
          ) : (
            <>
              <h2>Connection editor</h2>
              <p className="muted">
                Select a box to edit its task type, repository, inputs, and
                result. Select a line to set a branch condition. Delete removes
                the selected task or connection.
              </p>
              <p className="muted">
                Each task waits for the tasks connected before it. Tasks on
                separate paths can run at the same time, up to the project's limit.
              </p>
            </>
          )}
        </aside>
      </div>
      <section className="panel workflow-inputs">
        <h2>Run inputs</h2>
        <JsonField
          label="Input names and types (JSON)"
          value={inputDeclarations}
          onChange={(value) => {
            setInputDeclarations(value);
            setMessage("");
          }}
          rows={5}
          help={
            'Use a JSON object with input names as keys and type names as values, for example {"request":"string"}. Tasks read these values with inputs.request. Supported types: ' +
            valueTypes.join(", ") +
            "."
          }
        />
      </section>
    </>
  );
}
