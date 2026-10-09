## Software Factory

A local daemon and UI for configuring, running, and observing software-development workflows across multiple projects and repositories.

### Core Architecture

- **Factory Daemon**
  - Long-running local service
  - Owns configuration, workflow execution, state, events, approvals, logs, and artifacts
  - Exposes an API used by the UI, CLI, and other integrations

- **Web UI**
  - Connects to the local daemon
  - Primary interface for configuring projects, repositories, workflows, agents, and policies
  - Uses React Flow for visual workflow editing
  - UI state/layout is stored separately from workflow semantics

### Domain Model

**Project**

- Logical workspace managed by the Factory
- May contain one or many repositories
- Defines workflow bindings, policies, harness configuration, and runtime state

**Repository**

- Local Git repository associated with a project
- Provides repository-specific skills, conventions, scripts, and metadata

**Workflow**

- Reusable, declarative graph describing how work progresses
- Stored independently from projects and repositories
- Consists of typed nodes, edges, inputs, outputs, conditions, and configuration

**Workflow Binding**

- Associates a reusable workflow with a project
- Supplies project-specific configuration, repositories, skills, policies, and execution providers

### Workflow Nodes

Initial node categories:

- Agent execution
- Repository skill/tool
- Deterministic command
- Decision/classification
- Human approval
- Validation/test
- Conditional branch
- Parallel execution
- Nested workflow
- External integration

Nodes communicate through typed inputs and outputs such as:

- `Issue`
- `Diagnosis`
- `Plan`
- `Patch`
- `TestResult`
- `ReviewFinding`
- `Approval`
- `PullRequest`

### Separation of Responsibilities

- **Workflow:** what work should happen
- **Skill:** how a capability is performed within a repository
- **Harness:** how an agent/model is executed
- **Daemon:** coordinates execution and persists state
- **UI:** configures and visualizes the system

### Storage

Factory-owned configuration is stored separately from source repositories, for example:

```text
~/.factory/
  projects/
  workflows/
  state/
```

Repositories can expose Factory-compatible skills and metadata without owning global workflow definitions.

### Execution Model

A workflow definition is instantiated into a runtime execution:

```text
Workflow Definition
        ↓
Project Binding
        ↓
Workflow Run
        ↓
Node Executions
        ↓
Agents / Skills / Humans / Tools
```

Runs persist their state so workflows can support:

- retries
- failures
- parallel work
- human approval
- long-running agents
- process restarts
- resumable execution

### User Experience

Typical setup:

```text
start factory daemon
        ↓
open local UI
        ↓
create project
        ↓
attach repository/repositories
        ↓
enable or customize workflows
        ↓
configure agents / policies / skills
        ↓
run and observe work
```

The primary UI is project-oriented. The workflow graph editor appears when creating or modifying workflows rather than being the main application surface.
