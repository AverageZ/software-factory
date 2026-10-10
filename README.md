# Factory

A local Go/SQLite daemon and React UI for running declarative workflows against Git repositories, with opt-in GitHub issue-to-draft-PR intake and workflow-defined PR feedback revisions. No Temporal or demo adapters.

## Run

Requires Go 1.26+, Node 22.12+, npm, Python 3.10+, Git, and an authenticated `omp` on `PATH`.

```sh
make bootstrap
make build
.factory-data/bin/factoryd
```

Open **http://127.0.0.1:8080**. The daemon serves `web/dist` and `/api`, storing configuration, snapshots, events, logs, and artifacts in `~/.factory`. Override with `-data DIR`, `-addr HOST:PORT`, or `-web DIR`.

`make dev` starts the real daemon and Vite at port 5173. `FACTORY_DATA` overrides its data directory; `.env.example` is not automatically loaded. `make test` runs Go race tests and the frontend build; `make lint` checks formatting, Go vet, and TypeScript.

The attached Strategy Game project and real verification runs are stored in the local, ignored `.factory-data/v2` directory. Use `.factory-data/bin/factoryd -data .factory-data/v2` to reopen them. Legacy `.factory-data` databases are untouched and are not imported into the new model.

### UI-only workflow playground

To explore the Workflows page without starting or connecting to the daemon:

```sh
npm --prefix web ci # first time only
make dev-ui
```

This opens **http://127.0.0.1:5173/#/workflows** with the declarative Given/If/Then editor. Existing step-based and custom graph workflows retain their editors. Only Node 22.12+ and npm are needed for the playground; it does not build Go binaries or require OMP. The equivalent frontend command is `npm --prefix web run dev:ui`. Stop Vite with **Ctrl-C**.

Workflows and separate graph layouts save to browser local storage and survive reloads. Saves are scoped to the browser and origin (host/port), never imported from or written to daemon data. Clearing site storage removes playground saves. Storage failures are shown rather than reported as successful saves.

This mode is **editing only**: no projects, bindings, runs, commands, agents, or GitHub operations. API proxying is disabled even if a real daemon is running. Editor checks still apply, but daemon workflow validation and execution are not exercised; a playground save does not prove a workflow will execute successfully. Use `make dev` for real execution. Both modes use port 5173, so stop the current UI before switching; do not restart an active daemon.

### Rebuild and restart the running daemon

From the Factory repository root, first check that no managed run is executing in the dashboard. Graceful shutdown cancels active processes; interrupted issue/revision work may need manual inspection and is **not** automatically replayed.

```sh
make build
```

This replaces `.factory-data/bin/factoryd` and `.factory-data/bin/factoryctl` and rebuilds `web/dist`; it does **not** restart an already-running daemon. Stop the old process with **Ctrl-C** in its terminal. If it was started in the background, identify the process listening on port 8080 with `lsof -nP -iTCP:8080 -sTCP:LISTEN`, confirm its command is `factoryd`, then send that PID `kill -TERM PID` (replace `PID` with the number printed by `lsof`). Wait for it to exit before starting another instance; never use `kill -9` for a routine restart.

Start the replacement **with the same data directory** as the previous daemon. For the checked-in Strategy Game setup:

```sh
.factory-data/bin/factoryd -data .factory-data/v2 -web web/dist -addr 127.0.0.1:8080
```

Wait for the `Factory listening` message. Leave that terminal open. In another terminal, run `curl -fsS http://127.0.0.1:8080/api/health`; `{"status":"ok"}` confirms it is serving. Reload the dashboard. To keep a daemon running after closing a terminal, use your process manager with the same executable, working directory, and arguments—not a second instance sharing `.factory-data/v2`. Omitting `-data .factory-data/v2` instead opens the unrelated default `~/.factory`.

## Dogfood Factory on itself

The local daemon at `.factory-data/v2` also has a **Software Factory** project attached to this repository (`factory-dogfood` / `factory-source`), with OMP model `openai-codex/gpt-6-sol` and project parallelism 1. Its `AverageZ/software-factory` GitHub connection is verified, Factory labels have been created, and issue intake is enabled. These settings live in ignored local daemon state; a fresh clone or data directory needs its own project, repository attachment, GitHub check, label setup, and intake enablement through the dashboard. Use the authenticated `gh` CLI and the repository's `origin` URL; do not copy `.factory-data/v2` between machines.

For a concrete improvement, open an issue on `AverageZ/software-factory` with a bounded outcome, acceptance criteria, and verification steps. Add `agent:run` only when ready for the agent to work on it (optional `agent/workflow:bug` and `agent/mode:repair`). The daemon polls every 30 seconds; **Poll now** on the project page forces a scan. The built-in issue workflow executes OMP in an owned worktree, runs the requested checks, and publishes a **draft PR**. Read the run transcript and checks, inspect the diff, and review it yourself; the agent's report is not independent validation. Failed or interrupted jobs retain their worktree and require inspection, not automatic replay.

To iterate on review feedback, add `agent:run` to the **Factory-owned PR** separately. Trusted collaborator feedback in conversation, inline comments, or reviews is then passed to the project's editable **PR feedback revision** workflow; the issue's label alone does not authorize PR changes. Remove the PR's `agent:run` to stop new revisions. Only a human should mark a draft ready and merge. Generic workflows started against the attached repository operate in this checkout, not an isolated worktree; use managed issue intake for code changes.

Check the setup and run state with:

```sh
.factory-data/bin/factoryctl api GET /api/projects/factory-dogfood/repositories/factory-source/github
.factory-data/bin/factoryctl api GET /api/projects/factory-dogfood/repositories/factory-source/intake
.factory-data/bin/factoryctl api POST /api/projects/factory-dogfood/repositories/factory-source/intake/poll
.factory-data/bin/factoryctl api GET /api/runs?projectId=factory-dogfood
```

Do not rebuild or restart the daemon while a managed job is active; shutdown interrupts its work. The daemon must keep running for automatic polls. The repository's `CLAUDE.md` holds Factory-specific agent conventions.

## Configure and run

1. Create a project; attach existing local Git roots. Configure the harness executable/model and project-wide parallelism.
2. Open **Workflows → New workflow**. Name the workflow and write the agent objective under **What must the AI do?**. The starter runs one agent in repository slot `primary`, with no required start data. Add named start data, an optional **Given** guard, and ordered **If / Then steps** as needed. Save does not run anything. Agent tasks can change files; adding a later approval does not undo those changes.
3. Open **Projects** and choose a project. Under **Workflow bindings**, select **New binding**. Choose your saved workflow. Map `primary` to a repository attached to that project. A binding connects the workflow to its project folder. Supply start data only if the workflow requires it.
4. Select **Save binding & start run** when ready. Inspect the task results and logs. At an approval task, approve to continue or reject to stop the run.

### Declarative Given / If / Then

The workflow's `declaration` is the source of truth. The daemon compiles it into the existing durable execution graph on save and load; supplied `nodes` and `edges` cannot override a declaration. Runs retain immutable compiled snapshots, so later edits do not change admitted work. Graph-only definitions and existing step-editor workflows remain supported without lossy conversion.

- **Given** guards the whole workflow. A false guard skips its actions.
- **Named actions** configure real agents, commands, tools, checks, approvals, HTTP requests, or saved child workflows. Reuse an action in multiple steps.
- **Then** steps execute in order. Every matching **If** executes independently; this is not first-match routing. A false If skips only its step and continues. Failure blocks downstream execution.
- **Save result as** names a result for later conditions and inputs. For structured process output, `Diagnosis.repositoryCount` means the JSON field inside `result.data`, without graph node IDs or the envelope prefix. Non-structured outputs expose envelope fields such as `Result.exitCode`. Input mappings pass the full typed result envelope, including `data`.
- Conditions support nested **all/any**, equality, numeric comparisons, existence, and `contains` / `notContains`. Array membership uses exact JSON equality (labels are case-sensitive); text containment uses substrings. Missing fields do not satisfy comparisons except `notExists`. Missing or skipped *result sources* fail the condition, even if another rule could short-circuit; use a child workflow to group dependent conditional work.
- Omitted step inputs inherit the action's inputs, or forward workflow start data if those are also omitted. An explicit `{}` passes no inputs. Child workflows must receive their declared inputs with matching types and share the binding's repository mappings.

**Example guard** offers the bug, review, dependency-author, and delivery conditions. It only supplies `Given` and a `WorkItem` input—not action implementations or event subscriptions. Configure the work item fields yourself at the binding/run boundary. Names such as `CreatePR`, `ApprovalWorkflow`, or `MergePR` do not provide GitHub operations, authorization, safety classification, or managed worktrees. Existing managed intake remains separate and unchanged.

#### Defaults and completion loops

| Setting | Default | Override |
|---|---|---|
| Agent model | Project harness model | Workflow default, then action `config.model` |
| Action timeout | 3600 seconds | Workflow default, then action `config.timeoutSeconds`; applies to agents, processes, and HTTP |
| Agent completion limit | 3 passes | Workflow default, then action `config.maxIterations`; integer 1–20 |
| Repetition | One pass | Enable **Repeat until semantic completion** and configure **Until conditions** |

An Until loop requires JSON output. Factory instructs the agent to return a complete JSON object; the default condition is `result.data.status == "Complete"`. After a successful but incomplete pass, the next receives `previousResult` with the prior typed envelope. A process error or invalid JSON fails immediately—this is not an automatic error retry. Exhausting the bound fails and blocks downstream work. Timeout covers the entire action, all passes share one attempt transcript with iteration markers, and artifacts are collected only after completion. An interrupted action retains the existing explicit-retry policy; successful intermediate passes are not independently replayable checkpoints.

Example API definition for read-only issue readiness assessment followed by human clarification review:

```json
{
  "id": "issue-readiness", "name": "Issue readiness",
  "inputs": {"WorkItem": "object"},
  "declaration": {
    "version": 1,
    "given": {"all": [
      {"path": "WorkItem.kind", "operator": "equals", "value": "Issue"},
      {"path": "WorkItem.labels", "operator": "contains", "value": "agent/workflow:review"}
    ]},
    "actions": {
      "IssueReview": {
        "kind": "agent", "repository": "primary",
        "config": {
          "prompt": "Read WorkItem and inspect repository context without changing files or GitHub. Assess whether the issue has enough detail to implement and verify. Return status Complete or Incomplete, with reasons and clarification questions.",
          "outputJson": true
        }
      },
      "ReviewClarification": {
        "kind": "approval",
        "config": {"message": "Read Assessment in the previous task output. Resolve its clarification questions before approving. This does not post a GitHub comment."}
      }
    },
    "steps": [
      {"id": "assess", "run": "IssueReview", "as": "Assessment"},
      {"id": "clarify", "run": "ReviewClarification",
       "if": {"path": "Assessment.status", "operator": "equals", "value": "Incomplete"},
       "inputs": {"Assessment": {"type": "object", "from": "Assessment"}}}
    ]
  }
}
```

Save through `POST /api/workflows`, create a project binding, and supply a typed `WorkItem` value when starting it. No graph fields are needed. The daemon validates named references, types, child workflow contracts, conditions, and settings; declarations are capped at 256 actions/steps and 1 MiB.

### Execution primitives and existing editors

Factory controls execution, not repository skills, rules, or system prompts. Agent nodes pass a workflow pre-prompt and typed input data to `omp -p` in the selected repository. Authentication, configuration, and repository discovery are inherited from your normal environment. Tool nodes call repository scripts; no Factory manifest is required inside a repository.

| Node | Configuration |
|---|---|
| Agent | `prompt` (task objective); optional `outputJson`, `model`, `timeoutSeconds`, `artifacts`, `maxIterations`, `until` (branch condition group over input `result`) |
| Command / tool / validation | `command` argv array; optional `outputJson`, `timeoutSeconds`, `artifacts` |
| Approval | `message`; explicit approval continues, rejection fails |
| Branch | Legacy: `input` name and `equals` JSON value. Conditions: group (`combinator`: `all`/`any`, nonempty `rules`) containing nested groups or rules with `input`, optional dotted object `property`, `operator` (`equals`, `notEquals`, `contains`, `notContains`, `exists`, `notExists`, `greaterThan`, `greaterOrEqual`, `lessThan`, `lessOrEqual`), and `value` for comparisons. Numeric comparisons require finite numbers without string coercion. Outgoing edges labeled `true`/`false` |
| Parallel | Fan-out/barrier using graph edges; waits for every predecessor |
| Nested workflow | `workflowId`; inherits repository mappings; node inputs become child inputs |
| Integration | Real HTTP `url`; optional `method`, `headers`, JSON `body`, `timeoutSeconds`; non-2xx fails |
| Decision | **Unavailable** until a decision provider is implemented; never fabricates a result |

Existing step-based workflows use short instructions and consistent terms based on [ASD-STE100 writing principles](https://asd-ste100.org/STE_faq.html). This is STE-style interface copy, not a claim of full specification compliance. That editor calls stages **steps**, rules **task groups**, and actions **tasks**. Its graph execution behavior has not changed.

Add, remove, or move steps and tasks as needed. **Add another task** offers AI, commands, project tools, checks, approval, web requests, and saved workflows. For a command, put the program on the first line and each argument on a new line. Spaces on a line stay within one argument. Shell features require an explicit shell program. Added steps and task groups contain a real approval task; change it if you need a different task.

**Optional: conditions and task order** controls a task group. Define **Values to check** from start data or task results in earlier steps. Select an optional dotted field path and value type. The condition builder can match all or any conditions, including nested groups. A missing or null field matches `notExists`. A missing or skipped source stops the run; Factory does not invent a value. Turn conditions off to run the group every time; saved conditions are retained.

Steps and task groups run in their displayed order. Factory tries every group, not just the first match. If conditions do not match, it skips that group and continues. Task order is **One at a time** by default. **Together · wait for all** lets tasks start within the project's concurrency limit. Factory waits for all tasks, including approval tasks. A failed task or rejected approval stops later tasks. Tasks that already started may finish. Approval cannot override a failed check.

**Advanced task settings** contains task names and types, project folder names, JSON output, result types, inputs, and configuration. **Read the run order** shows a plain-language summary. **Advanced: workflow JSON** shows the compiled definition. Save stays disabled until the editor's checks pass; daemon validation still applies. Existing workflows keep their settings. Stage metadata is accepted only when it compiles to the saved graph. Convertible linear workflows open as one step. Custom graphs stay in **Advanced: edit connections**; their run order is not flattened or replaced with stale metadata. Layout positions remain separate from execution.

A manual run still requires a project binding. Editing does not subscribe to GitHub events, grant permission, create managed worktrees, or publish pull requests. Managed issue and review jobs keep their separate permission checks and exact-SHA publication boundaries. Names alone add no behavior; model overrides and completion loops require the explicit settings described above. Explicit run Retry keeps the existing managed-workflow restrictions. Automatic GitHub approval and merging are not provided by this declarative authoring change.

Workflow inputs declare names and types. Node inputs contain `type` and either `value` or `from` (`inputs.NAME` or `ANCESTOR.result`). Command inputs are available as `FACTORY_INPUTS` JSON. Outputs use typed `result` envelopes: commands/agents expose `exitCode`, `stdout`, `stderr`; integrations expose `status`, `body`. Domain types such as `TestResult` and `ReviewFinding` label object payloads, not custom schema validation. Process output envelopes are capped at 64 KiB per stream; complete attempt logs remain available. Artifacts are repository-relative files copied into Factory storage.

For agent, command, tool, and validation tasks, enable **Read the task output as JSON** (`outputJson: true`) in **Advanced task settings** when later conditions need values from task output. A successful task must print one complete JSON object to stdout, exposed as `NODE.result` → `data` (for example, field `data.complexity` or `data.repository_count`). Invalid JSON, non-object output, or output exceeding the 64 KiB capture limit fails the task and blocks dependent work. Normal stdout/stderr/exitCode remain available; agents cannot add tasks or bypass configured approvals through this data.

## GitHub connection and labels

GitHub setup uses the server's installed, authenticated `gh` CLI (`gh auth login` outside Factory). Save a repository, then use **GitHub setup → Check GitHub connection** on its project page. Factory discovers its `origin`, verifies the account and repository, and records the default branch, reported permissions, availability, and missing labels. Standard github.com HTTPS/SSH origins are supported; other hosts and ambiguous origins are blocked. Reports persist across restarts and show when they were last checked, not live monitoring.

**Create missing labels** previews the actual GitHub destination and requires confirmation. Factory rechecks its immutable repository ID and creates only missing definitions from `poc.md`: `agent:run`, three workflow labels, three mode labels, seven status labels, and seven phase labels. Existing labels, colors, and descriptions are preserved. Partial failures are visible; retry creates only names still missing. Checks themselves do not modify GitHub. Reported repository permissions do not prove every token scope or branch-protection requirement.

## Label intake and issue-to-draft-PR

On a saved project's **GitHub setup** panel, check its connection, then **Enable issue intake**. Intake is off by default. The daemon polls every 30 seconds; **Poll now** scans immediately without waiting for agent execution. **Refresh intake status** loads the latest durable jobs, run links, PR links, and cleanup errors.

- An open issue with `agent:run` selects the built-in repair workflow. No extra label or manually configured binding is required. For example, [Strategy Game #2463](https://github.com/AverageZ/strategy-game/issues/2463) is eligible with just `chore` and `agent:run`.
- Optional `agent/workflow:bug` and `agent/mode:repair` are supported. Observe/manage, review/rollback, unknown selectors, and conflicting selectors block publication rather than silently selecting repair. Pull requests are not issue-intake subjects.
- The immutable GitHub repository ID plus issue number deduplicates intake across polls and daemon restarts. Closing a PR does not cause the still-labeled issue to create another PR.
- Factory snapshots the issue and project harness settings, fetches the current default branch, and runs OMP to implement and validate the issue in an owned worktree. The run's `implement` node retains the real transcript; its `publish` node succeeds only after a PR is recorded.
- Before execution, checkpointing, push, and draft creation, Factory rechecks the repository identity, issue state, and `agent:run`. Changed issue content or an advanced base blocks publication pending intervention. Restored authorization can resume a successfully completed local candidate without replaying the agent.
- Publication checkpoints tracked and relevant untracked changes, including binary files, and pushes the exact candidate to a unique branch using create-only compare-and-swap. Existing unrelated branches are never overwritten. An ownership marker and branch lookup reconcile an ambiguous PR-create response before another attempt.
- PRs are always created as drafts with an issue-closing reference. Factory does not mark them ready, merge them, or rewrite GitHub status/phase labels. Agent-reported validation remains evidence for human review, not an independent correctness guarantee.

Disabling intake stops new issue work and publication; already-running local work may finish but cannot be published while disabled. Removing `agent:run` likewise quarantines local results at the next authorization check. Status/phase labels never authorize work.

CLI/API equivalents:

```sh
factoryctl api GET /api/projects/PROJECT_ID/repositories/REPOSITORY_ID/intake
printf '{"enabled":true}' | factoryctl api PUT /api/projects/PROJECT_ID/repositories/REPOSITORY_ID/intake -
factoryctl api POST /api/projects/PROJECT_ID/repositories/REPOSITORY_ID/intake/poll
```

## Workflow-defined PR feedback revisions

Enabling issue intake creates an **editable PR feedback revision workflow and binding** for that repository. Existing enabled intakes receive one on daemon restart; the selected binding appears under **GitHub setup → PR feedback revision binding**. Edit the workflow prompt or select another compatible binding to change revision behavior. Choose **Off** (or `PUT /api/projects/PROJECT_ID/repositories/REPOSITORY_ID/intake/review` with `{"bindingId":""}`) to opt out; selecting a binding re-enables it. The existing Strategy Game **Repository review** binding is a read-only readiness report, not a revision workflow, and is not silently repurposed.

The minimal managed revision contract is one **agent** node (no edges), bound to the intake repository, with workflow inputs `pullRequest` (`object`) and `feedback` (`array`) mapped to agent inputs via `inputs.pullRequest` and `inputs.feedback`. The agent's `feedbackSources` selects one or more of `conversation`, `inline`, and `review`; Factory reads only the selected GitHub review surfaces. The workflow prompt defines how to interpret and implement the feedback, which checks to run, and whether to use `gh` to inspect additional PR context. Factory supplies the transport, trust check, and branch safety, **not** a hardcoded “fetch comments and fix them” instruction. Binding inputs can provide other workflow parameters. Factory appends mandatory worktree/publication safety instructions and adds a managed publication step to the run snapshot. Other graph shapes are rejected for revision intake rather than silently executing in the attached checkout.

For each **open, Factory-owned PR** created by issue intake, `agent:run` on the **PR** explicitly authorizes revision. A new trusted review item automatically adds `agent/workflow:review` to that PR if no conflicting workflow/mode selector exists; the workflow label selects the revision path but **never grants authorization**. Factory does not add `agent:run`, relabel unrelated PRs, or remove competing selectors. Issue labels alone do not authorize a revision. The supported feedback sources are conversation comments, inline review comments (including suggestions), and submitted commented/changes-requested reviews. Only feedback from current repository collaborators with write, maintain, or admin permission enters the workflow; bots are excluded. A human reviewer using the same GitHub account as Factory is still trusted when that account has repository write permission. Every selected item has a durable source/ID/edit identity. New trusted items are batched into a revision job; processed identities survive restart, and unchanged comments do not rerun the workflow.

Factory snapshots the binding, feedback, and **current PR head**, fetches that branch into a new owned worktree, runs the configured workflow there, and rechecks labels, reviewer trust, unchanged feedback, and head before publishing. A checked-in revision advances the **existing** PR branch with an exact-SHA Git force-with-lease; a concurrent branch change is rejected, never overwritten. A failed/interrupted revision retains its workspace and blocks subsequent revisions of that PR for inspection. PR closure, authorization removal, or disabled intake prevents new revision work. A run's managed publication node reports the push separately from agent execution. Publication creates no new PR and never merges or marks it ready.

Example workflow shape (use your repository's own instructions and verification commands in the prompt):

```json
{
  "id": "pr-revision", "name": "PR revision",
  "inputs": {"pullRequest": "object", "feedback": "array"},
  "nodes": [{
    "id": "revise", "name": "Revise and validate", "kind": "agent",
    "repository": "source",
    "inputs": {
      "pullRequest": {"type": "object", "from": "inputs.pullRequest"},
      "feedback": {"type": "array", "from": "inputs.feedback"}
    },
    "config": {
      "feedbackSources": ["conversation", "inline", "review"],
      "prompt": "Inspect the provided PR feedback and repository context, implement the requested changes, then run the affected checks."
    }
  }],
  "edges": []
}
```

## Worktree isolation and cleanup

Issue jobs use `DATA/worktrees/JOB_ID` and `factory/issue-JOB_ID`, not the attached checkout's branch, index, or working files. A repository-wide OS lease in the Git common directory excludes other issue writers, including daemons using different data directories. Durable ownership records also block fresh writers when a crashed job has left uncheckpointed work. Git subprocesses discard inherited `GIT_*` overrides; publication uses the verified repository's explicit HTTPS destination, not `origin` push URLs or URL-rewrite rules.

Lifecycle fetch/push commands use `gh auth git-credential`, so the authenticated GitHub connection also supplies Git HTTPS credentials. No global Git configuration or separate `gh auth setup-git` step is required.

After draft publication, Factory releases the issue worktree as soon as execution no longer needs it. It also observes recorded PRs and retries issue-worktree cleanup when they are **closed or merged**, even when intake is disabled or issue authorization has been removed. Revision worktrees are released after a successful branch update; failed revision cleanup remains visible for manual inspection. Local checkpoint branches, base refs, ownership records, and run evidence are retained; cleanup does not delete PR branches.

Cleanup validates the exact owned path, branch, checkpoint, Git common directory, and worktree registration. Clean ignored build/dependency outputs may be removed; dirty, untracked/uncheckpointed, changed-HEAD, symlinked, or otherwise ambiguous work is retained with a visible error. Resolve uncertainty before retrying issue cleanup with **Poll now**; revision cleanup requires manual inspection. Unrelated worktrees are never globally pruned. Missing worktrees are reconciled idempotently.

Regular Git binary changes are checkpointed. Changes to Git LFS pointer files stop with an explicit error and retain the workspace: external LFS object upload is not implemented, and disabled Git hooks must not silently publish unavailable binary content.

Failed or interrupted agent work is retained for inspection and is not automatically replayed. The generic node **Retry** endpoint deliberately cannot bypass managed issue/revision ownership and authorization checks. Inspect the recorded error, transcript, retained path, and any surviving processes before manual recovery. Hard-killed processes are not assumed dead merely because an OS lease was released.

This isolation applies to managed issue and selected PR revision workflows. Manually started generic workflows, including the original Strategy Game review binding, still execute in their bound checkout. Worktrees are not a sandbox: OMP and commands have the user's permissions, and only trusted repositories and harnesses should be attached.

## State and safety

Runs snapshot workflow definitions, bindings, and project settings. Completed nodes are not replayed; pending approvals and safe queued work survive restart. Interrupted in-flight work requires an explicit retry because side effects may already have happened. Retry preserves completed work and prior attempt logs/artifacts. Graceful shutdown cancels process groups; a hard-killed daemon may leave descendants running. This is **not exactly-once execution** or a sandbox.

Commands, agents, and HTTP integrations have your local permissions and inherited environment. Only attach trusted repositories and run trusted workflows. The API has no authentication; it binds to loopback and checks browser origins by default. Never expose it publicly. Do not put credentials into workflow definitions: definitions and run snapshots are persisted. One daemon owns each data directory.

`factoryctl` uses the same API: `projects`, `workflows`, `bindings`, `runs`, `run ID`, `start BINDING_ID`, `approve/reject/retry RUN_ID NODE_ID`, `cancel RUN_ID`, and `log RUN_ID NODE_ID`. For configuration and integrations, use `factoryctl api METHOD /api/PATH [JSON_FILE|-]`; `-` reads JSON from stdin. Global `-url` precedes the command. Run `factoryctl -h` for usage.
