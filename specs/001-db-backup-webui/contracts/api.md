# HTTP API Contract (v1)

Base: `/api`. JSON in/out (`Content-Type: application/json`). Auth: session cookie `dumpling_session`.
All mutating requests (POST/PUT/DELETE) require header `X-Requested-With: dumpling`.
Errors: `{"error": "<human message>", "code": "<machine_code>"}` with 4xx/5xx; messages never
contain secrets. Secret fields (`password`, `passphrase`) are write-only: responses expose
`"password_set": true` / `"encrypted": true` instead. Omitting a secret on PUT keeps the stored value.

## Auth & setup

| Method | Path | Body | Response |
|--------|------|------|----------|
| GET | `/api/setup` | – | `{"needed": bool}` (no auth) |
| POST | `/api/setup` | `{username, password}` | 201; 409 `already_setup` if a user exists (no auth) |
| POST | `/api/login` | `{username, password}` | 204 + cookie; 401; 429 rate limited |
| POST | `/api/logout` | – | 204 |
| GET | `/api/me` | – | `{"username"}` (session check) |

## System

| GET | `/api/system` | – | `{"version", "tools": {"pg_dump": "17.2"\|null, "pg_restore", "psql", "mysqldump", "mysql"}, "tls": bool, "bind"}` |

## Connections

| Method | Path | Body / Notes |
|--------|------|--------------|
| GET | `/api/connections` | list |
| POST | `/api/connections` | `{name, engine, host, port, username, password, tls_mode, share_path_server?, share_path_local?}` |
| PUT | `/api/connections/{id}` | same as POST, `password` optional |
| DELETE | `/api/connections/{id}` | 409 `in_use` if plans reference it |
| POST | `/api/connections/test` | unsaved body as POST (or `{id}` to test saved) → `{"ok", "server_version", "databases": [..], "error"?, "warnings": [..]}`; mssql also probes share |
| GET | `/api/connections/{id}/databases` | `["db1", ...]` |

## Destinations

> Extended by feature 002 (S3/SFTP types, test, rescan, location lock): see
> `specs/002-s3-sftp-destinations/contracts/api.md`, which supersedes the rows below.

| GET | `/api/destinations` | list with `free_bytes` |
| POST | `/api/destinations` | `{name, path}`; 400 `not_writable` |
| PUT | `/api/destinations/{id}` | `{name, path}` |
| DELETE | `/api/destinations/{id}` | 409 `in_use` |

## Plans

| Method | Path | Body / Notes |
|--------|------|--------------|
| GET | `/api/plans` | list incl. `next_run`, `last_operation` |
| POST | `/api/plans` | `{name, connection_id, databases[], destination_id, schedule, retention:{last,daily,weekly,monthly}, passphrase?, paused}`; 400 `invalid_schedule`/`invalid_retention` |
| PUT | `/api/plans/{id}` | same; `passphrase: ""` removes encryption for future backups |
| DELETE | `/api/plans/{id}` | existing backups kept (`plan_id` set null, `plan_name` retained for history) |
| POST | `/api/plans/{id}/run` | → `{"operation_ids": [..]}` one per database |
| POST | `/api/plans/{id}/pause` · `/resume` | 204 |

## Backups

> Feature 002 adds `imported`, `locked`, `destination_type`, `location` and `POST /api/backups/{id}/unlock`.

| Method | Path | Body / Notes |
|--------|------|--------------|
| POST | `/api/backups` | on-demand: `{connection_id, databases[], destination_id, passphrase?}` → `{"operation_ids"}` |
| GET | `/api/backups?plan_id=&connection_id=&database=&status=` | list, newest first |
| GET | `/api/backups/{id}` | detail incl. metadata |
| POST | `/api/backups/{id}/verify` | → `{"operation_id"}` |
| DELETE | `/api/backups/{id}` | requires body `{"confirm": "<database>"}` |

## Restore

`POST /api/restores`

```json
{
  "backup_id": "…",
  "target_connection_id": "…",
  "target_database": "orders_copy",
  "confirm_database": "orders_copy",   // required iff target exists
  "passphrase": "…",                    // only if backup encrypted and plan passphrase unavailable
  "skip_ownership": false               // postgres: --no-owner --no-acl; mysql: strip DEFINER
}
```

Responses: 202 `{"operation_id"}`;
409 `confirmation_required` `{"target_exists": true, "warnings": [...]}` when target exists and
`confirm_database` missing/mismatched;
400 `engine_mismatch`, `backup_not_ok`, `passphrase_required`;
409 `restore_in_progress`.

`POST /api/restores/preflight` — same body without confirm → `{"target_exists", "version_warning"?, "passphrase_required"}`
(drives the UI wizard, FR-018/FR-020).

## Operations

| GET | `/api/operations?type=&status=&plan_id=&connection_id=&before=<id>&limit=50` | newest first, cursor by id |
| GET | `/api/operations/{id}` | detail |
| GET | `/api/operations/{id}/log` | `text/plain` full log |
| GET | `/api/events` | SSE stream. Events: `operation` (row JSON on every status change), `log` (`{"id","line"}`) for running operations |

## CLI

```text
dumpling [--data-dir ./data] [--bind 127.0.0.1:9898] [--tls-cert FILE --tls-key FILE]
env: DUMPLING_MASTER_KEY (base64 32 bytes, optional)
```

Non-loopback `--bind` without TLS ⇒ prominent startup warning (FR-027a).
