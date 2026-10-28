# API changes: Incremental Backups

Delta on `specs/001-db-backup-webui/contracts/api.md` (and 002). Unchanged endpoints omitted.

## Plans

`POST /api/plans`, `PUT /api/plans/{id}` — new body fields:

```json
{
  "incr_schedule": "0 * * * *",        // "" = incremental mode off
  "max_chain": 30,
  "ack_incremental_risk": true         // SQL Server and MySQL: required when turning incremental mode on
}
```

PostgreSQL with `incr_schedule != ""`: `databases` must be `["*"]`.

New errors: 400 `ack_required`; 400 `prerequisite_missing` `{"missing": ["summarize_wal is off", …]}`.

`GET /api/plans` rows add `incr_schedule`, `max_chain`, `incr_ack_at`, `next_incr_run`.

`POST /api/plans/{id}/run?kind=full|incremental` — default `full`.

## Backups

Rows (`GET /api/backups`, `GET /api/backups/{id}`) add:

```json
{ "kind": "incr", "chain_id": "…", "parent_id": "…", "seq": 3, "chain_ok": true }
```

`chain_ok` = every ancestor exists with status `ok` (computed per list response).

`DELETE /api/backups/{id}` — when dependents exist, 409 `has_dependents` `{"dependents": N}` unless body has `"cascade": true` (still with `"confirm": "<database>"`). Cascade deletes dependents first.

`POST /api/backups/{id}/verify` — on a chain link verifies all links; operation message summarises per-link results.

## Restore

`POST /api/restores/preflight` and `POST /api/restores` — response/body additions:

```json
// request (PostgreSQL chain backups only; replaces target_connection_id/target_database)
{ "backup_id": "…", "target_path": "orders-2026-10-28" }   // relative to the restore root

// preflight response adds
{
  "chain": [{"id": "…", "kind": "full", "seq": 0, "status": "ok", "started_at": "…"}, …],
  "chain_error": "link 2 (…) is corrupted",   // omitted when chain is complete
  "target_path_ok": true                        // PostgreSQL chain only
}
```

New errors: 400 `chain_broken` `{"link": "<backup id>"}`; 400 `target_path_not_empty`; 400 `target_path_required`; 400 `target_path_outside_root`.

For SQL Server and MySQL chain backups the request is unchanged (target connection + database, confirm rules as today).

For chain backups, `backup_locked` and `passphrase_required` carry `{"link": "<backup id>"}` naming the first link whose
passphrase is unavailable; both are returned by preflight/restore before anything is downloaded.
