# Data Model: Incremental Backups

Additive SQLite migration #2 (`internal/store/migrate.go`, `ADD COLUMN` only).

## plans (extended)

| Column | Type | Default | Meaning |
|---|---|---|---|
| `incr_schedule` | TEXT | `''` | cron for incremental runs; `''` = incremental mode off |
| `max_chain` | INTEGER | `30` | max incrementals per chain before a forced full (1–1000) |
| `chain_reset_at` | TEXT | `''` | set to now when connection, destination, databases or passphrase change; chains whose full started earlier are closed |
| `incr_ack_at` | TEXT | `''` | SQL Server and MySQL: when the user acknowledged the engine-specific incremental warning |

Rules:
- `incr_schedule` must parse like `schedule` (400 `invalid_schedule`).
- SQL Server or MySQL + incremental on: `incr_ack_at` must be set (request `ack_incremental_risk: true`), else 400 `ack_required`.
- PostgreSQL + incremental on: `databases` = `["*"]` (cluster scope); any other value rejected (400 `invalid`).
  Turning incremental off on a PostgreSQL plan requires choosing real databases again.
- Prerequisites checked on save when turning incremental on; failure → 400 `prerequisite_missing` `{missing: [...]}`.

## backups (extended)

| Column | Type | Default | Meaning |
|---|---|---|---|
| `kind` | TEXT | `'full'` | `full` or `incr` |
| `chain_id` | TEXT | `''` | id of the chain's full backup (the full's own id for the full); `''` = standalone backup outside incremental mode |
| `parent_id` | TEXT | `''` | backup this link applies on top of (`''` for fulls) |
| `seq` | INTEGER | `0` | position in chain (0 = full) |
| `marker` | TEXT | `''` | engine resume state, JSON (below) |

`database = "*"` marks a PostgreSQL cluster backup. A PostgreSQL backup with `chain_id != ''` is physical (tar); otherwise a `pg_dump` archive.

Markers:
- mssql: `{"base_guid": "…"}` (full and diffs carry the base's GUID)
- mysql full: `{"server_uuid": "…", "file": "binlog.000042", "pos": 157}`; incr: `{"server_uuid", "start_file", "start_pos", "file", "pos"}`
- postgres: `{"system_id": "…", "manifest": "<rel>.manifest"}`

Server identity (`server_uuid`, `system_id`; mssql: `base_guid`) is compared at every incremental with the server reached now;
a mismatch is `ErrChainBroken` → full. A password-only connection change keeps the chain.

Parent rule (set at backup time): mssql incr → chain full; mysql/postgres incr → previous link.

Chain validity for restore of backup X: walk `parent_id` from X to the full; every link exists, `status = 'ok'`.
For mssql the ancestors of a diff are the full only (cumulative); for mysql/postgres every earlier link.
Field naming: `kind` = the spec's backup "type".

## Sidecar `<rel>.json` (extended)

Adds `kind`, `chain_id`, `parent_id`, `seq`, `marker`. Rescan imports them; since imported backups get new ids, rescan maps sidecar ids → new ids after importing all sidecars of a destination (old `operation_id`/backup ids stored in sidecar as `backup_id`, added now).

## Extra object (PostgreSQL only)

`<rel>.manifest` — `backup_manifest` of that link, through the artifact pipeline (encrypted if plan is). Local cache: `<data-dir>/manifests/<backup_id>`. Deleted together with its backup.

## State transitions

```
incremental run requested
  ├─ no open chain / chain full / chain_reset_at newer / prerequisite missing ──► full (reason logged; prereq ⇒ warning)
  ├─ chain head's ancestors not all present + ok ──► full ("chain broken: link <id>")
  ├─ mysql full: long-running write (> 10 s) or lock wait > 5 s (watchdog kill) ──► ErrLockUnsafe ──► no backup, op warning
  └─ capture ──► ErrChainBroken ──► abort incr upload ──► full (reason logged)
               └─ ok ──► incr link (seq = parent.seq+1 for mysql/pg; count of diffs for mssql)
```

Backup `status` values unchanged (`ok`/`corrupted`/`deleted`). Verify of a link sets `corrupted` on each failing link of its chain.

## Retention

Incremental plans: `keep = closure(Keep(backups of plan+database))` where `closure` adds every ancestor via `parent_id`.
Non-incremental plans: unchanged (`Keep` over all backups of the plan).

## Restore target path (PostgreSQL chains)

`target_path` is relative to the restore root (`--restore-dir` / `DUMPLING_RESTORE_DIR`, default `<data-dir>/restores`).
After `filepath.Clean` and resolving symlinks of its parent it must stay inside the root, and must not exist or be empty.
