# Factory

A local Go/SQLite daemon and React UI for running reusable workflows against Git repositories, with opt-in GitHub issue-to-draft-PR intake and workflow-defined PR feedback revisions. No Temporal or demo adapters.

## Run

Requires Go 1.26+, Node 22.12+, npm, Python 3.10+, Git, and an authenticated `omp` on `PATH`.

```sh
make bootstrap
make build
.factory-data/bin/factoryd
```

Open **http://127.0.0.1:8080**. The daemon serves `web/dist` and `/api`, storing configuration, snapshots, events, logs, and artifacts in `~/.factory`. Override with `-data DIR`, `-addr HOST:PORT`, or `-web DIR`.

`make dev` starts the real daemon and Vite at port 5173. `FACTORY_DATA` overrides its data directory; `.env.example` is not automatically loaded. `make test` runs Go race tests and the frontend build; `make lint` checks formatting, Go vet, and TypeScript.

The attached Strategy Game project and real verification runs are stored in `.factory-data/v2`. Use `.factory-data/bin/factoryd -data .factory-data/v2` to reopen them. Legacy `.factory-data` databases are untouched and are not imported into the new model.

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

## Configure and run

1. Create a project; attach existing local Git roots. Configure the harness executable/model and project-wide parallelism.
2. Create a reusable workflow in **Workflows**. Add/configure nodes, connect their handles, and save. Graph positions and viewport are saved separately from execution semantics.
3. Add a project binding: map workflow repository slots to attached repositories and supply typed inputs.
4. Start the binding. Observe real node outputs, events, logs, artifacts, child runs, and approval controls.

Factory controls execution, not repository skills, rules, or system prompts. Agent nodes pass a workflow pre-prompt and typed input data to `omp -p` in the selected repository. Authentication, configuration, and repository discovery are inherited from your normal environment. Tool nodes call repository scripts; no Factory manifest is required inside a repository.

| Node | Configuration |
|---|---|
| Agent | `prompt`; optional `timeoutSeconds`, `artifacts` |
| Command / tool / validation | `command` argv array; optional `timeoutSeconds`, `artifacts` |
| Approval | `message`; explicit approval continues, rejection fails |
| Branch | `input` name and `equals` JSON value; outgoing edges labeled `true`/`false` |
| Parallel | Fan-out/barrier using graph edges; waits for every predecessor |
| Nested workflow | `workflowId`; inherits repository mappings; node inputs become child inputs |
| Integration | Real HTTP `url`; optional `method`, `headers`, JSON `body`, `timeoutSeconds`; non-2xx fails |
| Decision | **Unavailable** until a decision provider is implemented; never fabricates a result |

Workflow inputs declare names and types. Node inputs contain `type` and either `value` or `from` (`inputs.NAME` or `ANCESTOR.result`). Command inputs are available as `FACTORY_INPUTS` JSON. Outputs use typed `result` envelopes: commands/agents expose `exitCode`, `stdout`, `stderr`; integrations expose `status`, `body`. Domain types such as `TestResult` and `ReviewFinding` label object payloads, not custom schema validation. Process output envelopes are capped at 64 KiB per stream; complete attempt logs remain available. Artifacts are repository-relative files copied into Factory storage.

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
