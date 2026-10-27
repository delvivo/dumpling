# Data Model: Database Hot Backup & Restore Web UI

Stored in SQLite (`<data-dir>/dumpling.db`). IDs are ULID-like sortable strings
(timestamp + random, generated with stdlib). All timestamps UTC RFC 3339.
`enc:` = AES-256-GCM sealed with the master key (research R4), never returned by the API.

## user

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | |
| username | text unique | 1–64 chars |
| password_hash | text | bcrypt |
| created_at | time | |

v1: exactly one row; `POST /api/setup` allowed only while table is empty.

## session

| Field | Type | Rules |
|-------|------|-------|
| token_hash | text PK | SHA-256 of cookie token |
| user_id | FK user | |
| expires_at | time | sliding 7 days |

## connection

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | |
| name | text unique | 1–64 chars |
| engine | enum | `postgres` \| `mysql` \| `mssql` |
| host, port | text, int | port 1–65535 |
| username | text | |
| password | enc | write-only via API |
| tls_mode | text | engine-specific: postgres `""`(prefer)/`disable`/`require`/`verify-full`; mysql `""`/`DISABLED`/`REQUIRED`/`VERIFY_IDENTITY` (ssl-mode); mssql `""`(encrypt)/`trust` (encrypt + trust server cert)/`disable` |
| share_path_server | text | mssql only, required: share as seen by SQL Server |
| share_path_local | text | mssql only, required: same share as seen by the app; must exist |
| server_version | text | filled at last successful test |
| tested_at | time | |

Delete refused while any `plan.connection_id` references it (spec edge case); backups and
operations keep `connection_name` and stay restorable to other targets.

## destination (Storage Destination)

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | |
| name | text unique | |
| path | text | absolute, must exist and be writable at save time |

Delete refused while referenced by a plan or by non-deleted backups.

## plan (Backup Plan)

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | |
| name | text unique | |
| connection_id | FK connection | |
| databases | json array of text | ≥ 1 |
| destination_id | FK destination | |
| schedule | text | 5-field cron, optional `CRON_TZ=` prefix; validated by parser |
| retention | json | `{last, daily, weekly, monthly}` ints ≥ 0, at least one > 0 |
| passphrase | enc, nullable | null ⇒ not encrypted |
| paused | bool | |
| created_at, updated_at | time | |

## backup

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | |
| operation_id | FK operation | the backup operation that produced it |
| plan_id | FK plan, nullable, ON DELETE SET NULL | null ⇒ on-demand or plan deleted |
| plan_name | text, nullable | copied at backup time (history survives plan deletion) |
| connection_id | FK connection, nullable, ON DELETE SET NULL | source |
| connection_name | text | copied at backup time |
| engine | enum | copied from connection at backup time |
| server_version | text | source engine version |
| database | text | |
| destination_id | FK destination | |
| path | text | relative to destination path |
| size_bytes | int | |
| sha256 | text | hex of stored bytes |
| encrypted | bool | |
| status | enum | `ok` \| `corrupted` \| `deleted` |
| started_at, finished_at | time | |

Row exists only for completed artifacts (failed runs leave no backup row, FR-008).
Sidecar `<path>.json` holds the same metadata (minus ids of local entities) next to the artifact.

**Artifact path**: `<plan-name|adhoc>/<connection-name>/<database>/<YYYYMMDDTHHMMSSZ>-<op-id last 6>.<ext>`;
ext = `pgdump` | `sql.gz` | `bak.gz`, plus `.age` when encrypted.

## operation

| Field | Type | Rules |
|-------|------|-------|
| id | text PK | sortable ⇒ newest first by id |
| type | enum | `backup` \| `restore` \| `prune` \| `verify` |
| status | enum | see state machine |
| plan_id, connection_id, backup_id | nullable FKs, ON DELETE SET NULL | for filtering (FR-023) |
| target_connection_id, target_database | nullable (FK ON DELETE SET NULL) | restore only |
| database | text | source database of backup/restore/verify (for display) |
| message | text | short summary / error / warning text (no secrets) |
| created_at, started_at, finished_at | time | |

Log file: `<data-dir>/logs/<id>.log` (tool stderr + app messages, secrets redacted).
Indexes: `(status)`, `(type)`, `(plan_id)`, `(connection_id)`.

### Operation state machine

```text
pending ──► running ──► success
   │           ├──────► warning      (finished, consistency caveat e.g. non-InnoDB tables)
   │           ├──────► failed
   │           └──────► interrupted  (set at startup if process died, FR-026)
   └──► skipped                      (overlapping scheduled run, FR-015)
```

Terminal states never change, except `backup.status` may later become `corrupted`
(failed verify / checksum mismatch at restore) or `deleted` (prune).

## Restore Request (not persisted separately)

Captured in the `restore` operation row: `backup_id`, `target_connection_id`, `target_database`,
`overwrite_confirmed` (stored in `message`/log as "confirmed by <user> at <time>").

Validation (FR-016–FR-021), all server-side:
1. backup `status = ok`;
2. target connection engine == backup engine;
3. target exists ⇒ request carries `confirm_database == target_database`;
4. no other running restore for `(target_connection_id, target_database)`;
5. encrypted backup ⇒ passphrase available (plan's stored one or supplied in request);
6. sha256 of artifact recomputed and matching before the target is touched (see note).

**Note on checksum vs. streaming**: verifying before dropping the target needs a full read
pass first. Decision: restore reads the artifact twice — pass 1 checksum (no disk writes),
pass 2 drop/recreate + restore. Doubles read I/O, keeps the target untouched on corruption.
