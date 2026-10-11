# Software Factory repository

Use ASD-STE100 Simplified Technical English (STE).

Factory is a local Go/SQLite daemon with a React/TypeScript dashboard.

## Change and verification conventions

- Keep the daemon's authorization, GitHub identity checks, durable job state, worktree ownership, and exact-SHA publication boundaries intact. Never turn a failed or uncertain check into an implicit authorization or a successful run.
- Trace a change across `internal/factory/` (engine, intake, review, workspace, GitHub), `cmd/`, and `web/src/` as applicable. Extend the existing behavior tests for consumer-visible transitions and failure paths.
- Use `make bootstrap` when dependencies are missing, `make test` for Go race tests and frontend build, and `make lint` for formatting, vet, and TypeScript checks. Exercise the affected API/UI or managed workflow path, not just its unit tests.
- Do not commit generated `web/dist`, dependencies, `.factory-data`, secrets, or local databases. The active daemon data is `~/.factory`, outside this checkout; `.factory-data/v2` is an obsolete local snapshot, not a reproducible repository fixture. Keep self-hosted managed worktrees outside this Git root.
- When operating inside a Factory-managed issue or PR revision worktree, follow the managed prompt: leave commits, branches, pushes, labels, PRs, and worktree lifecycle to Factory. Do not restart the daemon during an active job.

## Managed changes to Factory

Select an issue workflow binding before enabling GitHub intake. `agent:run` on an open issue authorizes that binding. The managed publication step creates a draft PR. A separate PR revision binding and `agent:run` on the PR authorize review changes. Humans review and merge. See `README.md` for setup and recovery.
