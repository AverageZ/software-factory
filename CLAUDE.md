# Software Factory repository

This directory is the Factory application, not the parent `projects/` launcher. Factory is a local Go/SQLite daemon with a React/TypeScript dashboard; `poc.md` is the original specification, while `README.md` describes the implemented behavior and operational limits.

## Change and verification conventions

- Keep the daemon's authorization, GitHub identity checks, durable job state, worktree ownership, and exact-SHA publication boundaries intact. Never turn a failed or uncertain check into an implicit authorization or a successful run.
- Trace a change across `internal/factory/` (engine, intake, review, workspace, GitHub), `cmd/`, and `web/src/` as applicable. Extend the existing behavior tests for consumer-visible transitions and failure paths.
- Use `make bootstrap` when dependencies are missing, `make test` for Go race tests and frontend build, and `make lint` for formatting, vet, and TypeScript checks. Exercise the affected API/UI or managed workflow path, not just its unit tests.
- Do not commit generated `web/dist`, dependencies, `.factory-data`, secrets, or local databases. `.factory-data/v2` is local daemon state, not a reproducible repository fixture.
- When operating inside a Factory-managed issue or PR revision worktree, follow the managed prompt: leave commits, branches, pushes, labels, PRs, and worktree lifecycle to Factory. Do not restart the daemon during an active job.

## Self-hosted dogfood loop

The `Software Factory` project in the local daemon attaches this Git root. GitHub issue intake requires an open issue on `AverageZ/software-factory` with `agent:run`; the default repair workflow creates a **draft** PR, not a merge. On that Factory-owned PR, `agent:run` separately authorizes trusted review feedback revisions. Humans review and merge. No unlabelled issue, generic checkout-bound workflow, or automatic merge is part of the loop. See the dogfood section of `README.md` for setup and recovery.
