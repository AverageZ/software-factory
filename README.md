# Factory

Factory runs declarative workflows on Git repositories. It uses a local Go/SQLite daemon and a React dashboard. You define the work. Factory schedules it, records the results, and checks authorization before publication.

Factory has no built-in workflows. Without a configured workflow, it does no work. Workflows can use any project language or build system.

## Run

Requires macOS or Linux, Go 1.26+, Node 22.12+, npm, Python 3.10+, Git, and `lsof`.

Install and authenticate `omp` for agent steps. For GitHub access, install `gh` and run `gh auth login`.

```sh
make bootstrap
make rebuild
```

Open **http://127.0.0.1:8080**.

`make rebuild` builds the binaries and dashboard, stops the daemon, and starts the replacement. It keeps the daemon's data directory and listen address. If work is active, the command exits with an error.

Older daemons use idle checks during their first upgrade. Do not start work until the command finishes.

The daemon runs in the background. Its log is `~/.factory/factoryd.log`. To select another address, set `FACTORY_ADDR`. To select a data directory for a new daemon, set `FACTORY_DATA`.

### UI-only workflow playground

To explore the Workflows page without starting or connecting to the daemon:

```sh
npm --prefix web ci # first time only
make dev-ui
```

This opens **http://127.0.0.1:5173/#/workflows** with the declarative Given/If/Then editor. Existing step-based and custom graph workflows retain their editors. This mode edits browser-local data only. It does not run projects, bindings, agents, commands, or GitHub operations. Use `make dev` for real execution.

| Command | Use |
| --- | --- |
| `make build` | Build without starting or stopping the daemon. |
| `make dev` | Start the daemon and Vite. Open port 5173 for UI development. |
| `make test` | Run Go race tests and Python tests, then build the dashboard. |
| `make lint` | Check Go formatting, Go vet, and TypeScript. |

## Data

`~/.factory` is the daemon's data directory. It contains the SQLite database, project settings, run history, logs, artifacts, and managed Git worktrees. Keep it outside your repositories. One daemon can use it at a time.

`.factory-data` in this checkout contains build tools, caches, and binaries. It is not the default daemon data directory.

## Workflows

A workflow is a reusable sequence of steps. For example: inspect an issue, change code, run checks, and request approval. Your workflow defines the instructions and checks. Factory does not choose them for your project.

1. Create a project and attach its local Git repositories.
2. Set the project's `omp` executable, model, and maximum number of concurrent runs.
3. In **Workflows**, define start data, an optional **Given** guard, named actions, and ordered **If / Then** steps.
4. Create a **binding** for the project. A binding selects the workflow's repositories and supplies its settings.
5. Start the binding. Open the run to see results, logs, files, and approval requests.

Steps can run an agent, execute a command, call HTTP, request approval, choose a branch, run parallel work, or call another workflow. Decision-provider steps are not available.

Agent steps use the selected repository's OMP rules and environment. Command steps use its scripts. No Factory configuration file is required in the repository.

New workflows use the declarative Given / If / Then editor. The declaration is the source of truth and Factory compiles it to the durable execution graph. Existing simple workflows retain the step editor. Custom graphs retain **Advanced: edit connections** so the UI does not make a lossy conversion.

Given guards the whole workflow. Each matching If runs its Then action in order; a false If skips that step and continues. Named results can supply later conditions and inputs. The editor's GitHub examples only create conditions and input shapes. They do not subscribe to GitHub events, grant authorization, approve, or merge.

## GitHub triggers

Think of a workflow as a function. A binding selects the repository and settings. The `agent:run` label authorizes Factory to call that binding with the issue or PR data.

For each repository, open **GitHub setup**:

1. Select **Check GitHub connection**.
2. Select **Create missing labels** if needed.
3. Select and save an issue workflow binding.
4. Enable issue intake.
5. Add `agent:run` to an open issue.

Factory checks GitHub every 30 seconds. **Poll now** starts a check immediately. No issue binding means no issue execution. Enabling intake does not create workflows.

### Labels

Your repository needs `agent:run` to authorize work. Workflow and mode labels are optional selectors. Status and phase labels are for display; they do not start or stop work.

| Label | Effect |
| --- | --- |
| `agent:run` on an issue | Authorize the configured issue binding. |
| `agent:run` on a Factory PR | Authorize configured PR feedback revisions and synchronization. |
| `agent/workflow:BINDING_ID` on an issue | Select another issue binding in the same project for this repository. |
| `agent/workflow:review` on a Factory PR | Show that Factory selected the configured PR revision path. It does not authorize work. |
| `agent/workflow:sync` on a Factory PR | Show that Factory monitors and synchronizes the PR branch with its base. It does not authorize work. |
| `agent/mode:repair` | Permit proposed code changes. This is the default mode for managed publication. |
| `agent/mode:observe`, `agent/mode:manage` | Not supported for managed publication. |
| `agent/status:*`, `agent/phase:*` | Display work status and phase. Factory does not update these labels. |

Unknown or conflicting selectors stop the job. Removing `agent:run` or disabling intake prevents new work and publication. Work already in progress can finish locally.

For example, `agent:run` calls the selected issue binding with the issue data. To use a binding whose ID is `bug`, create the GitHub label `agent/workflow:bug` and add it to the issue. The label does not create the binding.

### Issues and PRs

Managed issue and PR workflows each use one agent step. Issue workflows receive the issue. Feedback workflows receive the PR and selected review feedback. Synchronization workflows receive the PR and the exact observed head and base commits. Factory adds the publication step. Other workflow graphs can be started manually.

The issue workflow runs in a separate Git worktree and creates a **draft PR**. Factory checks the issue label, repository identity, issue contents, and base commit before publication. It does not merge the PR into its base or mark the PR ready.

To process PR feedback, select and save a PR revision binding. Add `agent:run` to the Factory-created PR. Factory passes new feedback from repository collaborators with write, maintain, or admin access to the selected workflow. The workflow chooses which feedback sources to read. Issue authorization does not authorize PR revisions.

To keep a Factory PR current, select and save a PR synchronization binding. When an open Factory-owned PR has `agent:run`, Factory adds `agent/workflow:sync` to show that monitoring is active. If the current base is not an ancestor of the PR head, Factory checks out the exact PR head in an isolated worktree and starts a two-parent merge of the exact base commit. The synchronization agent resolves conflicts and runs validation. Factory checkpoints the merge and pushes it only when the remote PR head and base still match the admitted commits. A changed observation is superseded and retried from a fresh poll. Factory never merges the PR into its base branch.

A successful feedback revision or synchronization updates the same PR branch. Factory uses an exact expected-head push and refuses to overwrite another writer.

## Failures and safety

Failed or interrupted managed jobs keep their worktrees for inspection. They do not restart automatically. Read the run error and logs before recovery. Completed nodes and earlier attempt logs remain in run history. For issue work, after you inspect a clean checkpointed candidate, select **Release candidate and continue queue**. If the issue agent made no commit, select **Release no-change workspace and continue queue**. For a checkpointed candidate, Factory requires the recorded checkpoint. For a no-change workspace, Factory requires a clean worktree with its branch and `HEAD` at the recorded base. A successful release removes only the managed worktree, keeps the local branch, marks the job released, and starts eligible queued work. Factory preserves the worktree if any identity or content check fails. For failed PR revisions and synchronizations, select **Discard worktree and retry** to delete the retained attempt and admit fresh eligible work, or **Discard worktree and dismiss** to delete it without replaying the same feedback or synchronization observation. Both PR recovery actions require the recorded job identity and permanently discard uncheckpointed work. A handled retained PR worktree does not stop unrelated PRs. An ownership record left uncertain by a daemon or host failure still blocks new writers until an operator recovers it.

Manually started workflows use their attached checkout. Managed worktrees isolate Git changes, but they are not a sandbox. Agents and commands have your local permissions. Use trusted repositories and workflows.

The API has no authentication. Keep it on loopback. Do not store credentials in workflow definitions; Factory saves definitions and run snapshots.

Git LFS publication is not supported. Factory retains the worktree if a run changes LFS pointers.

## CLI

The binaries are in `.factory-data/bin`. `factoryctl` uses the dashboard API.

```sh
.factory-data/bin/factoryctl projects
.factory-data/bin/factoryctl workflows
.factory-data/bin/factoryctl start BINDING_ID
.factory-data/bin/factoryctl run RUN_ID
```

Use `factoryctl -h` for commands. Use `factoryctl api METHOD /api/PATH [JSON_FILE|-]` for API requests. `-` reads JSON from standard input. Put `-url` before the command to select another daemon.
