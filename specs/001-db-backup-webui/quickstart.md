# Quickstart & Validation Guide

## Prerequisites

- Go 1.26+, Node 22+, Docker with Compose.
- Client tools on PATH: `psql`, `pg_dump`, `pg_restore` (≥ server major), `mysql`, `mysqldump`.
  SQL Server needs no client tool.

## Build & run

```sh
cd web && npm ci && npm run build && cd ..
go build -o dumpling ./cmd/dumpling
./dumpling --data-dir ./data          # http://127.0.0.1:9898
```

## Run everything with Docker Compose

```sh
docker compose -f testdata/compose.yaml up -d --build --wait dumpling   # app + test DBs, http://127.0.0.1:9898
```

Env vars and the values to enter in the UI are documented in the `dumpling` service of
`testdata/compose.yaml`. Inside the compose network use host names `pg-a`, `mysql-a`, `mssql-a`…,
destination `/backups`, SQL Server share `/var/opt/mssql/share` (server) / `/share` (local).

## Test databases

```sh
docker compose -f testdata/compose.yaml up -d --wait pg-a pg-b mysql-a mysql-b mssql-a mssql-b
```

SQL Server share: compose bind-mounts `./testdata/mssql-share` to `/var/opt/mssql/share`
in both mssql containers → `share_path_server=/var/opt/mssql/share`,
`share_path_local=./testdata/mssql-share` (absolute).

## Automated checks

```sh
go test ./...                                  # unit
docker compose -f testdata/compose.yaml run --rm it   # round-trip per engine, in a container with client tools
# or on a host with the tools: go test -tags integration ./internal/runner/
cd web && npm test                             # UI logic
```

## Manual scenarios (map to spec)

| # | Steps | Expected |
|---|-------|----------|
| 1 | First launch → setup admin → login | Setup only offered once (FR-027) |
| 2 | Add `pg-a` connection with wrong password → Test | Clear error, password not shown (US1-2, SC-005) |
| 3 | Fix password → Test | Version + database list shown (US1-1) |
| 4 | Add destination `./backups`; on-demand backup of `shop` on pg-a while `testdata/load.sh` writes | Success, size/checksum shown; load script reports 0 failed writes (US1-3, SC-004) |
| 5 | Restore that backup to pg-b as `shop_copy` | Created, no confirmation needed; row counts match (US2-1) |
| 6 | Restore again to `shop_copy` | Confirmation dialog requires typing `shop_copy`; then drop+recreate (US2-2, FR-018) |
| 7 | Pick a MySQL connection as target for a pg backup | Not offered / `engine_mismatch` (US2-3) |
| 8 | Flip one byte of the artifact file → Verify, then Restore | Verify marks `corrupted`; restore refused, target untouched (US2-4) |
| 9 | Plan on mysql-a every minute, retention `last=3`; wait 5 runs | Exactly 3 backups remain, prune operations logged (US3) |
| 10 | Plan with passphrase; restore one of its backups | No passphrase prompt; artifact on disk ends in `.age` (FR-011a) |
| 11 | Repeat 4–6 with mssql-a → mssql-b | Same outcomes; `.bak` removed from share afterwards |
| 12 | Kill process during a backup, restart | Operation `interrupted`, no `.partial` left (FR-026) |
| 13 | `grep -r <test-password> ./data ./backups` | No match (SC-005) |
| 14 | Start with `--bind 0.0.0.0:9898` without TLS | Startup warning (FR-027a) |
