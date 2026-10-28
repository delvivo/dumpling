# Quickstart: validate incremental backups

Prereqs: compose stack from `CLAUDE.md` (pg-a runs with `summarize_wal=on`; MySQL 8.4 has binlog on by
default). Dumpling built and running against `testdata/compose.yaml` (`dumpling` service, :9898).

## 1. SQL Server differential (US1, US2)

1. Plan on `mssql-a`, database `orders`, local destination, full `0 3 * * 0`, incremental `0 * * * *`.
   Saving without ticking the acknowledgment → error `ack_required`; tick it → saved.
2. Run now (incremental) with no full yet → history shows a **full**, log says "no base".
3. Change ≤ 5% of rows; run incremental three times (changing rows in between) → three `incr` rows,
   `parent` = the full, each ≤ 20% of the full's size.
4. Take `BACKUP DATABASE orders TO DISK='/var/opt/mssql/share/x.bak'` manually, run incremental →
   log "base changed outside Dumpling", a new full is taken.
5. Restore the last diff to `mssql-b` as `orders_copy` → row counts match source at that time.

## 2. Chain integrity (US2)

Delete the full's artifact file on disk, open restore for a dependent diff → blocked, link named.
Verify the diff → per-link report, full marked corrupted.

## 3. Retention (US3)

Plan `keep last 3`, run full + 5 incrementals (MySQL or SQL Server) → after prune, the 3 newest links
and every ancestor they need remain; restore of each kept link works.

## 4. MySQL binlog (US4)

1. Plan on `mysql-a`, database `shop`, incremental mode on: saving without the read-lock
   acknowledgment → `ack_required`; tick it → saved.
2. Full, then three rounds of insert/update/delete + incremental; each incremental ≤ 20% of the full.
3. Restore the 3rd incremental to `mysql-b` as `shop_copy` and to `mysql-a` as `shop_copy` (same
   server: proves `--skip-gtids`) → contents match.
4. `PURGE BINARY LOGS BEFORE NOW()` on mysql-a, run incremental → new full, reason logged.
5. Revoke `REPLICATION SLAVE` from the account, run incremental → full + warning "prerequisite missing".
6. In another session keep a write running > 10 s (e.g. `INSERT … SELECT SLEEP(30)`), start a full →
   no backup, warning "long-running statement; full postponed". Also start the long write right after
   the full begins → dump stopped within ~5 s, warning, writes resume.
7. Point the plan's connection at `mysql-b` (host change), run incremental → new full, log
   "server identity changed".

## 5. PostgreSQL physical (US5)

1. Plan on `pg-a`, incremental mode on → database field hidden, "Entire server".
   Same plan on `pg-b` (PG 16) → `prerequisite_missing` (server < 17).
2. Full, then three rounds of changes in two databases + incremental.
3. Restore the 3rd incremental to `pgr` (inside the restore root `<data-dir>/restores`);
   `../x` → `target_path_outside_root`; non-empty dir → `target_path_not_empty`.
4. Mount `<data-dir>/restores/pgr` into `postgres:17` (chown 999) → server starts; both databases show
   the changes.

## Automated

`go test ./...` (chain resolution, retention closure, kind decision, marker parsing, scheduler two
schedules); `docker compose -f testdata/compose.yaml run --rm it` adds chain round-trips per engine.
