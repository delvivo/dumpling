# Research: Database Hot Backup & Restore Web UI

All Technical Context unknowns resolved below. Format: Decision / Rationale / Alternatives.

## R1. App state storage (config, plans, backups index, operations)

- **Decision**: SQLite via `modernc.org/sqlite` (pure Go, no cgo), single file `<data-dir>/dumpling.db`.
  Operation logs as plain files `<data-dir>/logs/<operation-id>.log`.
- **Rationale**: history must be filterable (FR-023) and survive crashes consistently (FR-026);
  SQLite gives transactions + queries for one dependency. Pure Go keeps a static single binary
  and trivial cross-compilation. Log files stream easily to the UI and stay out of the DB.
- **Alternatives**: JSON config + JSON-lines op log (Backrest-style config) — no atomic multi-row
  updates, filtering by hand; `mattn/go-sqlite3` — needs cgo; bbolt — no ad-hoc queries.

## R2. HTTP API and live updates

- **Decision**: stdlib `net/http` with Go 1.22+ method/pattern routing, JSON REST under `/api`;
  live status/logs via Server-Sent Events (`text/event-stream`).
- **Rationale**: zero dependencies; SSE is one-way server→browser, exactly what FR-024 needs,
  native `EventSource` on the client.
- **Alternatives**: gRPC/Connect (Backrest) — codegen toolchain for both sides, overkill;
  WebSocket — needs a dependency, bidirectional not needed.

## R3. Authentication (FR-027, FR-027a)

- **Decision**: single admin user created on first launch; password hashed with bcrypt
  (`golang.org/x/crypto/bcrypt`); session = random 32-byte token in `HttpOnly`, `SameSite=Strict`
  cookie (`Secure` when TLS), stored hashed in SQLite, 7-day sliding expiry. Mutating requests
  must carry `X-Requested-With: dumpling` (CSRF defence in addition to SameSite). Login attempts
  rate-limited per IP (in-memory, 5/min).
- **Rationale**: standard, small, matches the single-admin assumption.
- **Alternatives**: HTTP basic auth — no logout, password on every request; OIDC — out of scope.

## R4. Secrets at rest (FR-003, FR-011a)

- **Decision**: AES-256-GCM (stdlib `crypto/aes`, `crypto/cipher`) with a master key from
  `DUMPLING_MASTER_KEY` env (base64) or, if unset, `<data-dir>/master.key` generated on first run
  with mode 0600. Encrypted fields: connection passwords, plan passphrases.
- **Rationale**: stdlib only; env option lets operators keep the key off the data disk.
- **Known ceiling**: key file on the same disk as the DB protects against DB-file leaks only,
  not full-disk compromise. Documented in README.

## R5. Artifact pipeline (FR-007, FR-008, FR-011, SC-008)

- **Decision**: one streaming pipeline per backup:
  `engine dump stdout → [gzip] → [age encrypt] → tee(sha256) → <final>.partial` then
  `fsync` + atomic `rename` to final name; a sidecar `<final>.json` with full metadata is written
  the same way. On any error: delete `.partial`, mark operation failed.
  - Compression: PostgreSQL uses `pg_dump -Fc` (built-in compression, no extra gzip);
    MySQL and SQL Server use stdlib `compress/gzip`.
  - Encryption: `filippo.io/age` with scrypt passphrase recipient (streaming, audited format).
  - Checksum: SHA-256 of the final stored bytes.
- **Rationale**: constant memory, single temp file that becomes the artifact (SC-008); sidecar
  makes artifacts self-describing (Constitution I) and lets a lost DB be re-indexed later.
- **Alternatives**: zstd (`klauspost/compress`) — faster but another dependency, gzip enough for
  v1; hand-rolled AES-GCM streaming — never roll crypto formats.

## R6. PostgreSQL

- **Decision**: CLI tools `psql`, `pg_dump`, `pg_restore`. Password via `PGPASSWORD` env of the
  child process only; TLS via `PGSSLMODE`. Backup: `pg_dump -Fc -d <db>` (keeps owners/ACLs;
  restore adds `--no-owner --no-acl` when the user ticks "skip ownership", since target roles
  may differ). Hot consistency: pg_dump runs in a
  single repeatable-read snapshot by default.
  Restore overwrite: `DROP DATABASE "<db>" WITH (FORCE)` (PG ≥ 13; for older, terminate backends
  via `pg_terminate_backend` first) → `CREATE DATABASE` → `pg_restore -d <db> --exit-on-error`.
  Listing/version: `psql -Atc`.
- **Rationale**: official tools, no driver dependency; tools are a runtime requirement anyway.
- **Note**: `pg_dump` major version must be ≥ server version; checked at connection test (FR-004).

## R7. MySQL

- **Decision**: CLI tools `mysql`, `mysqldump`. Credentials via a temp `--defaults-extra-file`
  (mode 0600 in `<data-dir>/tmp`, removed after use) — never on the command line.
  Backup: `mysqldump --single-transaction --routines --triggers --events --hex-blob --set-gtid-purged=OFF <db>`
  (`--set-gtid-purged=OFF` so dumps restore onto other GTID-enabled instances; requires the Oracle
  MySQL client — MariaDB's client lacks the flag, so tool detection reports the client variant)
  (database name as plain argument, no `--databases`, so the dump has no `CREATE DATABASE`/`USE`
  and can be restored under another name). Before dumping, query `information_schema.TABLES` for non-InnoDB tables
  → operation status `warning` with explanation (spec edge case).
  Restore overwrite: `DROP DATABASE` → `CREATE DATABASE` → `mysql <db> < dump`.
  Restore with `skip_ownership`: line-by-line streaming filter strips `DEFINER=\`...\`@\`...\``
  clauses (also inside `/*!50013 ... */` comments) so views/routines are created by the
  restoring user when the original definer does not exist on the target.
- **Rationale**: `--single-transaction` gives a consistent InnoDB snapshot without locking writes.
- **Alternatives**: `MYSQL_PWD` env — deprecated by MySQL; mydumper — extra tool.

## R8. SQL Server (clarification Q1)

- **Decision**: driver `github.com/microsoft/go-mssqldb` (no `sqlcmd` needed).
  Backup: `BACKUP DATABASE [db] TO DISK = '<share_server>/<op-id>.bak' WITH COPY_ONLY, CHECKSUM`
  → app reads `<share_local>/<op-id>.bak` through the pipeline (gzip + age) → storage → delete
  the `.bak`. `COPY_ONLY` so existing DBA log chains are not broken.
  Restore: pipeline in reverse writes `<share_local>/<op-id>.bak`; `RESTORE FILELISTONLY` gives
  logical file names; target paths from `SERVERPROPERTY('InstanceDefaultDataPath'/'LogPath')`;
  if target exists: `ALTER DATABASE SET OFFLINE WITH ROLLBACK IMMEDIATE` (no other session can grab
  the database, unlike SINGLE_USER), then `RESTORE DATABASE [target] ... WITH REPLACE, MOVE ..., CHECKSUM`
  (brings it online; on failure the app sets it ONLINE again), delete `.bak`.
  Connection test probes the share: `BACKUP DATABASE model TO DISK=... WITH COPY_ONLY` then checks
  the file is readable locally, deletes it.
- **Rationale**: native backup is the only transactionally consistent hot method; driver avoids
  shipping `sqlcmd`. Express edition lacks `WITH COMPRESSION`, hence gzip in the app.

## R9. Scheduling (FR-013, FR-015, SC-006)

- **Decision**: parse schedules with `github.com/robfig/cron/v3` standard 5-field parser
  (`CRON_TZ=` prefix supported; default server local time). Own loop: for each active plan
  compute `Next()`, one timer to the earliest, re-plan on config changes. Presets in the UI map
  to cron strings. Per-plan in-memory lock; overlapping fire → `skipped` operation (FR-015).
- **Rationale**: cron parsing is fiddly and well covered by the library; the runtime loop is
  10s of lines. Single process ⇒ in-memory locks are enough.
- **Alternatives**: hand-written cron parser — more code + tests than the dependency is worth.

## R10. Retention (FR-014)

- **Decision**: pure function `Keep(backups, policy) → set` over successful backups of a plan:
  union of last N, newest per day for N days, per ISO week for N weeks, per month for N months;
  always includes newest successful. Prune runs after each successful backup as its own
  `prune` operation; deletes artifact + sidecar, marks backup `deleted`.
- **Rationale**: restic/Backrest semantics users already know; pure ⇒ table-driven unit tests.

## R11. Startup recovery (FR-026)

- **Decision**: at boot, operations with status `running`/`pending` → `interrupted`; delete any
  `*.partial` in destinations and stray `<op-id>.bak` in SQL Server shares referenced by those ops.

## R12. Frontend

- **Decision**: Vite + React + TypeScript, `react-router` for pages, plain CSS (no component
  library), `fetch` + `EventSource`. Build output `web/dist` embedded with `//go:embed`.
  Tests: Vitest for non-trivial UI logic (restore confirmation gating, cron preset mapping).
- **Rationale**: few pages (connections, destinations, plans, history, operation detail, restore
  wizard, login/setup); a component library would dominate bundle and deps.

## R13. Testing & integration (Constitution VI)

- **Decision**: `go test ./...` for unit tests (retention, scheduler next-run, pipeline, secret
  box, command construction, restore guards). Integration tests behind build tag `integration`
  using `testdata/compose.yaml` (postgres:17, mysql:8.4, mcr.microsoft.com/mssql/server:2022)
  with a bind-mounted share dir; each engine: write load → backup → restore to new name on a
  second DB → compare schema + row counts (SC-003, SC-004).
- **Rationale**: Docker Compose avoids the testcontainers dependency tree.

## R14. Distribution

- **Decision**: single binary (`go build ./cmd/dumpling`) + a Dockerfile (debian-slim with
  `postgresql-client-17` from PGDG and `mysql-client` 8.4 from Oracle repo) for users who want
  the client tools bundled. Flags: `--data-dir`, `--bind` (default `127.0.0.1:9898`),
  `--tls-cert`, `--tls-key`.

## R15. Deferred low-impact item: operation history retention

- **Decision**: v1 keeps operation records and logs indefinitely.
  `ponytail:` add age-based pruning when history size becomes a problem.
