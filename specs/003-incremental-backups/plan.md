# Implementation Plan: Incremental Backups

**Branch**: `feature/003-incremental-backups` | **Date**: 2026-10-28 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/003-incremental-backups/spec.md`

## Summary

Plans gain an optional incremental schedule. Backups become links of a chain per (plan, database):
SQL Server native cumulative differentials, MySQL raw binary-log capture replayed with database
rewrite, PostgreSQL 17 physical incremental base backups combined into a data directory at restore.
Chains are modeled with a `parent_id` pointer on each backup, so restore, verify and retention share
one parent walk. Every case where an incremental cannot be produced validly falls back to a full.
Design details: [research.md](research.md), [data-model.md](data-model.md), [contracts/api.md](contracts/api.md).

## Technical Context

**Language/Version**: Go (module as today), TypeScript/React (Vite)

**Primary Dependencies**: existing only (go-mssqldb, minio-go, pkg/sftp, age, robfig/cron, modernc SQLite); stdlib `archive/tar`. New runtime client tools: `mysqlbinlog`, `pg_basebackup`, `pg_combinebackup` (only for incremental plans of that engine)

**Storage**: SQLite (additive migration #2); artifacts on local/S3/SFTP destinations

**Testing**: `go test ./...` (unit), compose `it` runner (round-trips on real engines), Vitest

**Target Platform**: single binary, Linux/macOS/Windows; engines in containers for tests

**Project Type**: web service + embedded SPA

**Performance Goals**: SC-001 — incremental ≤ 20% size and ≤ 50% time of a full at ≤ 5% change

**Constraints**: streaming (no full artifact in memory); secrets never on argv/logs; checksum all links before touching a restore target; PostgreSQL restore output confined to the restore root

**Scale/Scope**: chains ≤ `max_chain` (default 30) links; PostgreSQL restore needs local disk ≈ sum of chain + reconstructed cluster

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
|---|---|---|
| I. Data Integrity | PASS | Link reported ok only after commit + checksum (FR-004); chain validity checked before restore; verify checks every link; fallback-to-full prevents broken chains (R2) |
| II. Safe Restore | PASS (with note) | All links checksummed before target is touched; confirmation rules unchanged for DB targets. PostgreSQL chains restore to a user-chosen empty directory confined to the restore root instead of a database — a target the user chooses, never overwritten (clarification Q1). Cross-engine rejection unchanged |
| III. Hot Backup | PASS — exception per amended Principle III (v1.1.0) | MySQL `--source-data` takes a brief global read lock at dump start (no lock-free way to record the binlog position): opt-in with stored acknowledgment (R9) and abort-instead-of-stall via pre-check + 5 s watchdog (R4 b, c). SQL Server non-copy-only fulls affect other tools' differential base — explicit acknowledgment (FR-007). `pg_basebackup` is online |
| IV. Credential Security | PASS | `mysqlbinlog` uses the same `--defaults-extra-file`; `pg_basebackup` uses `PGPASSWORD` env; manifests encrypted like artifacts |
| V. Simplicity | PASS | No new Go dependency; one `parent_id` pointer instead of a chain table; sentinel `"*"` instead of a table rebuild; engine work behind one optional interface |
| VI. Tests | PASS (planned) | Unit: kind decision, retention closure, chain resolution, marker parsing, scheduler; round-trip per engine in compose `it` |

Post-design re-check: unchanged — PASS.

## Project Structure

### Documentation (this feature)

```text
specs/003-incremental-backups/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/api.md
├── checklists/requirements.md
└── tasks.md             # /speckit-tasks
```

### Source Code (repository root)

```text
internal/store/
├── migrate.go           # migration #2 (plans + backups columns)
└── entities.go          # Plan/Backup fields, chain queries (Ancestors, Dependents)
internal/engine/
├── engine.go            # Incremental/ChainRestorer interfaces (R12), ErrChainBroken, ErrLockUnsafe, tools, Gzip per artifact
├── tarx.go              # shared safe tar extraction (R11)
├── mssql.go             # non-copy-only full, DIFFERENTIAL, base GUID, NORECOVERY chain restore
├── mysql.go             # --source-data marker, raw binlog capture (tar), replay with rewrite
└── postgres.go          # pg_basebackup full/incr, manifest capture, pg_combinebackup restore
internal/runner/
├── backup.go            # kind decision + fallback, chain fields, manifest object (pg)
├── restore.go           # chain resolution, download+checksum all links, target_path (pg)
├── maintain.go          # verify chain, retention closure, cascade delete
└── rescan.go            # import chain fields, remap parent ids
internal/schedule/
├── retention.go         # Closure()
└── scheduler.go         # two schedules per plan, Run(plan, kind)
internal/server/handlers.go   # plan fields/validation, run?kind, restore target_path, delete cascade
cmd/dumpling/main.go          # scheduler callback signature, --restore-dir flag
web/src/
├── pages/Plans.tsx           # incremental toggle, schedule, max chain, ack checkbox, PG "Entire server"
├── pages/Backups.tsx         # kind/chain columns, chain health
├── pages/RestoreWizard.tsx   # chain preview, blocked state, PG target path
├── restoreGate.ts            # gate on chain_error / target_path
└── types.ts
testdata/compose.yaml         # pg-a: summarize_wal=on; it image gets mysqlbinlog/pg_combinebackup
```

**Structure Decision**: existing single Go module + `web/` SPA; no new packages.

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|---|---|---|
| PostgreSQL restore outputs a directory, not a database | Physical backups cannot load through a client connection | Temporary server + logical re-dump: far more code and needs server binaries on the host (clarification Q1) |
| `"*"` sentinel in `plans.databases` / `backups.database` | Cluster-scope plans; CHECK `json_array_length >= 1` | Table rebuild migration: riskier than a documented sentinel |
