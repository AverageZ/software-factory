export interface Repository {
  id: string;
  name: string;
  path: string;
}
export interface Project {
  id: string;
  name: string;
  repositories: Repository[];
  harness: { binary: string; model: string };
  policy: { maxParallel: number };
}

export interface GitHubConnection {
  host: string;
  id: number;
  owner: string;
  name: string;
  url: string;
  defaultBranch: string;
  account: string;
  canPush: boolean;
  canManageLabels: boolean;
  archived: boolean;
  issuesEnabled: boolean;
}
export interface GitHubCheck {
  id: string;
  status: "ok" | "warning" | "error";
  message: string;
}
export interface GitHubLabel {
  name: string;
  group: "authorization" | "workflow" | "mode" | "status" | "phase";
  color: string;
  description: string;
  present: boolean;
}
export interface GitHubHealth {
  repositoryPath: string;
  checkedAt: string;
  status: "unchecked" | "ready" | "attention" | "blocked";
  connection?: GitHubConnection;
  checks: GitHubCheck[];
  labels: GitHubLabel[];
  createdLabels: string[];
  setupError?: string;
}
export interface GitHubTarget {
  host: string;
  repositoryId: number;
}

export interface IssueJob {
  id: string;
  projectId: string;
  repositoryId: string;
  githubRepositoryId: number;
  number: number;
  title: string;
  url: string;
  status:
    | "queued"
    | "working"
    | "paused"
    | "publishing"
    | "published"
    | "closed"
    | "failed"
    | "interrupted";
  error?: string;
  runId?: string;
  branch?: string;
  worktree?: string;
  baseSHA?: string;
  headSHA?: string;
  pullRequestNumber?: number;
  pullRequestUrl?: string;
  updatedAt: string;
}
export interface ReviewJob {
  id: string;
  issueJobId: string;
  pullRequestNumber: number;
  status: string;
  error?: string;
  runId?: string;
  worktree?: string;
  baseSHA?: string;
  headSHA?: string;
  feedbackKeys: string[];
  updatedAt: string;
}
export interface IntakeReport {
  enabled: boolean;
  reviewBindingId?: string;
  checkedAt: string;
  error?: string;
  jobs: IssueJob[];
  revisions: ReviewJob[];
}

export const valueTypes = [
  "string",
  "number",
  "boolean",
  "object",
  "array",
  "Issue",
  "Diagnosis",
  "Plan",
  "Patch",
  "TestResult",
  "ReviewFinding",
  "Approval",
  "PullRequest",
] as const;
export interface Value {
  type: string;
  value: unknown;
}
export interface Input {
  type: string;
  value?: unknown;
  from?: string;
}
export interface Binding {
  id: string;
  name: string;
  projectId: string;
  workflowId: string;
  repositories: Record<string, string>;
  inputs: Record<string, Value>;
}
export const nodeKinds = [
  "agent",
  "tool",
  "command",
  "decision",
  "approval",
  "validation",
  "branch",
  "parallel",
  "workflow",
  "integration",
] as const;
export type NodeKind = (typeof nodeKinds)[number];
export interface WorkflowNode {
  id: string;
  name: string;
  kind: NodeKind;
  repository?: string;
  inputs?: Record<string, Input>;
  outputType?: string;
  config: Record<string, unknown>;
}
export interface WorkflowEdge {
  id: string;
  source: string;
  target: string;
  when?: "true" | "false";
}
export interface Workflow {
  id: string;
  name: string;
  nodes: WorkflowNode[];
  edges: WorkflowEdge[];
  inputs?: Record<string, string>;
}
export interface Layout {
  nodes: Record<string, { x: number; y: number }>;
  viewport?: { x: number; y: number; zoom: number };
}
export type RunStatus =
  | "queued"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "interrupted"
  | "cancelled";
export type NodeStatus =
  | "pending"
  | "running"
  | "waiting"
  | "succeeded"
  | "failed"
  | "skipped"
  | "interrupted"
  | "cancelled"
  | "unavailable";
export interface NodeExecution {
  nodeId: string;
  status: NodeStatus;
  attempt: number;
  startedAt?: string;
  finishedAt?: string;
  outputs: Record<string, Value>;
  error?: string;
  logPath?: string;
  artifacts: string[];
  childRunId?: string;
}
export interface RunEvent {
  time: string;
  nodeId?: string;
  type: string;
  message: string;
}
export interface Run {
  id: string;
  projectId: string;
  bindingId: string;
  workflowId: string;
  status: RunStatus;
  createdAt: string;
  updatedAt: string;
  inputs: Record<string, Value>;
  nodes: Record<string, NodeExecution>;
  events: RunEvent[];
  error?: string;
  parentRunId?: string;
}
