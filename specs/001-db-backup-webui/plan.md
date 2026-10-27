# Implementation Plan: Database Hot Backup & Restore Web UI

**Branch**: `feature/001-db-backup-webui` | **Date**: 2026-10-27 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/001-db-backup-webui/spec.md`

## Summary

Self-hosted single-binary app: Go backend (stdlib HTTP, SQLite state) serving an embedded React
UI. Hot backups delegate to official engine tools (`pg_dump -Fc`, `mysqldump
--single-transaction`, native SQL Server `BACKUP DATABASE ... COPY_ONLY` into a shared directory)
and stream through gzip → optional age encryption → SHA-256 into a filesystem destination with a
metadata sidecar. Restores go to any same-engine target (optionally renamed), verify checksum
first, and require typed confirmation before drop-and-recreate. Cron plans with restic-style
retention; operation history with live SSE logs. Details: [research.md](research.md).

## Technical Context

**Language/Version**: Go 1.26+ (dev: 1.27); TypeScript 5 + React 19 (Node 22+ build only)

**Primary Dependencies**: Go — `modernc.org/sqlite`, `golang.org/x/crypto` (bcrypt),
`filippo.io/age`, `github.com/microsoft/go-mssqldb`, `github.com/robfig/cron/v3`.
Web — `react`, `react-dom`, `react-router`, `vite`, `vitest`.

**Storage**: SQLite file for app state; filesystem directories for artifacts; log files per operation

**Testing**: `go test` (unit), `go test -tags integration` + Docker Compose (engine round-trips), Vitest

**Target Platform**: Linux amd64/arm64 (primary), Windows/macOS (best effort; same binary build)

**Project Type**: web application (backend + embedded SPA, single binary)

**Performance Goals**: databases up to ~100 GB; scheduled runs start within 1 min (SC-006);
throughput bounded by dump tool and disk, not by the app

**Constraints**: constant memory regardless of DB size (streaming); no extra temp disk beyond the
artifact (SQL Server: plus native `.bak` in share); secrets never on argv/logs

**Scale/Scope**: single admin, tens of connections/plans, thousands of operations; ~8 UI pages

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Gate | Pre | Post-design |
|-----------|------|-----|-------------|
| I. Data Integrity | Success only after clean exit + checksum; partials removed; metadata sidecar; verify on demand | ✅ | ✅ R5, R11, data-model `backup` |
| II. Safe Restore | Explicit target; typed confirmation for overwrite; engine mismatch rejected | ✅ | ✅ API `/restores` 409 flow; checksum pass before drop |
| III. Hot Backup | Engine consistent snapshot, no downtime; caveats surfaced | ✅ | ✅ R6–R8; non-InnoDB ⇒ `warning` |
| IV. Credential Security | Encrypted at rest; never in logs/argv/API; auth required | ✅ | ✅ R3, R4; `PGPASSWORD` env, MySQL defaults-file, write-only API fields, log redaction |
| V. Simplicity | Stdlib first; each dependency justified | ✅ | ✅ 5 Go deps, each justified in R1, R3, R5, R8, R9 |
| VI. Tests | Non-trivial logic tested; round-trip per engine | ✅ | ✅ R13, quickstart |
| Single binary, GPL-3.0 | `go:embed` frontend | ✅ | ✅ R12, R14 |

No violations ⇒ Complexity Tracking empty.

## Project Structure

### Documentation (this feature)

```text
specs/001-db-backup-webui/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/api.md
└── tasks.md             # /speckit-tasks
```

### Source Code (repository root)

```text
cmd/dumpling/main.go        # flags, wiring, startup recovery, TLS/bind warning
internal/
├── store/                  # SQLite schema (embedded .sql), queries, ids
├── secret/                 # AES-GCM box + master key load/generate
├── engine/                 # Engine interface + postgres.go, mysql.go, mssql.go, tools detection
├── artifact/               # streaming pipeline: gzip, age, sha256, .partial→rename, sidecar
├── runner/                 # backup / restore / verify / prune operations, locks, logs
├── schedule/               # cron loop + retention.go (pure)
└── server/                 # HTTP handlers, auth/session, CSRF header, SSE hub, static UI
web/
├── embed.go                # //go:embed dist
├── src/                    # pages: Setup, Login, Connections, Destinations, Plans,
│                           #        Backups, RestoreWizard, Operations, OperationDetail
└── package.json
testdata/compose.yaml       # pg/mysql/mssql ×2 + shared mssql dir
Dockerfile                  # binary + client tools
```

**Structure Decision**: one Go module at repo root; `engine.Engine` interface is justified by
3 real implementations. Frontend under `web/` because `go:embed` needs `dist` inside a package dir.

## Complexity Tracking

None.
