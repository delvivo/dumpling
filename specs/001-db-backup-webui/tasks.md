---
description: "Task list for 001-db-backup-webui"
---

# Tasks: Database Hot Backup & Restore Web UI

**Input**: Design documents from `specs/001-db-backup-webui/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.md, quickstart.md

**Tests**: Included where Constitution VI requires them (non-trivial logic + one backup→restore
round-trip per engine). Not full TDD; trivial glue has no dedicated test.

**Organization**: Tasks are grouped by user story to enable independent implementation and testing.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: US1–US4 from spec.md
- Paths relative to repo root (single Go module, UI in `web/`, see plan.md)

---

## Phase 1: Setup (Shared Infrastructure)

**Purpose**: Project initialization and basic structure

- [X] T001 Initialize Go module `github.com/delvivo/dumpling` (go 1.26) in `go.mod`, add deps `modernc.org/sqlite`, `golang.org/x/crypto`, `filippo.io/age`, `github.com/microsoft/go-mssqldb`, `github.com/robfig/cron/v3`; create empty package dirs per plan.md under `cmd/dumpling/` and `internal/{store,secret,engine,artifact,runner,schedule,server}/`
- [X] T002 [P] Scaffold Vite + React 19 + TypeScript app in `web/` (`package.json` scripts `dev`, `build`, `test`; deps `react`, `react-dom`, `react-router`; dev deps `vite`, `@vitejs/plugin-react`, `typescript`, `vitest`); Vite dev proxy `/api` → `http://127.0.0.1:9898` in `web/vite.config.ts`
- [X] T003 [P] Create `web/embed.go` (package `web`, `//go:embed all:dist` as `embed.FS`) and a placeholder `web/dist/index.html` so `go build` works before the first UI build
- [X] T004 [P] Create `.gitignore` entries: `/data/`, `/backups/`, `web/node_modules/`, `web/dist/*` except placeholder, `/dumpling`, `/dumpling.exe`, `testdata/mssql-share/*`
- [X] T005 [P] Create `testdata/compose.yaml` with services `pg-a`, `pg-b` (postgres:17), `mysql-a`, `mysql-b` (mysql:8.4), `mssql-a`, `mssql-b` (mcr.microsoft.com/mssql/server:2022-latest, Developer), fixed host ports, known test passwords, both mssql containers bind-mounting `./mssql-share` to `/var/opt/mssql/share`; seed scripts creating database `shop` with 2 tables + rows in each `-a` instance
- [X] T006 [P] Create `testdata/load.sh`: loop inserting rows into `shop.orders` on a given engine/host for N seconds, printing count of failed writes (used by SC-004 checks)

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: Core infrastructure that MUST be complete before ANY user story

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [X] T007 Write SQLite schema in `internal/store/schema.sql` (embedded) for tables `user`, `session`, `connection`, `destination`, `plan`, `backup`, `operation` exactly as in data-model.md, incl. enums as CHECK constraints (`engine IN ('postgres','mysql','mssql')`; operation `type IN ('backup','restore','prune','verify')`; operation `status IN ('pending','running','success','warning','failed','interrupted','skipped')`; backup `status IN ('ok','corrupted','deleted')`), unique `name` on connection/destination/plan, indexes `(status)`, `(type)`, `(plan_id)`, `(connection_id)` on operation; `backup.plan_id`, `backup.connection_id` and all operation FKs `ON DELETE SET NULL`; columns `backup.plan_name`, `backup.connection_name`
- [X] T008 Implement `internal/store/store.go`: `Open(path)` (WAL mode, `foreign_keys=ON`, apply schema), sortable ID generator `NewID()` (48-bit ms timestamp + 80-bit `crypto/rand`, Crockford base32), time helpers (UTC RFC 3339)
- [X] T009 [P] Implement `internal/secret/secret.go`: AES-256-GCM `Seal/Open` (random nonce prefix); `LoadKey(dataDir)` reads `DUMPLING_MASTER_KEY` (base64, 32 bytes) or creates `<data-dir>/master.key` with mode 0600; test round-trip, tamper detection and wrong-key failure in `internal/secret/secret_test.go`
- [X] T010 [P] Implement `internal/artifact/writer.go`: `Create(destDir, relPath, opts{Gzip bool, Passphrase string}) (io.WriteCloser)` pipeline `[gzip] → [age scrypt] → sha256 tee → <final>.partial`; `Commit(meta)` = flush, fsync, move to final name without ever overwriting an existing file (`os.Link` + `os.Remove`, fail if target exists), write sidecar `<final>.json` atomically; `Abort()` deletes `.partial`; plus `internal/artifact/reader.go` `Open(path, passphrase, gzip) (io.ReadCloser)` and `Checksum(path) (sha256, error)`; tests in `internal/artifact/artifact_test.go` (round-trip with/without gzip/age, abort leaves nothing, wrong passphrase fails, checksum stable, commit refuses to overwrite) plus `TestPipelineBoundedMemory`: stream 2 GiB pseudo-random through writer+reader with gzip+age and assert `runtime.ReadMemStats` `HeapInuse` stays < 64 MiB throughout (SC-008)
- [X] T011 [P] Implement `internal/runner/oplog.go`: per-operation log file `<data-dir>/logs/<id>.log`, `Redactor` that replaces registered secret strings with `***` on every written line, line fan-out callback for SSE; test redaction (secret split across writes too) in `internal/runner/oplog_test.go`
- [X] T012 Define `internal/engine/engine.go`: `Conn` struct (fields per data-model `connection`, password plaintext in memory only), `Engine` interface — `Test(ctx, Conn) (version string, dbs []string, warnings []string, err error)`, `Backup(ctx, Conn, db string, w io.Writer, log io.Writer) (warnings []string, err error)`, `Exists(ctx, Conn, db) (bool, error)`, `Restore(ctx, Conn, db string, overwrite bool, r io.Reader, opts RestoreOpts, log io.Writer) error`; `For(engine string) Engine`; `Tools()` detection with `exec.LookPath` + version parse for `pg_dump`, `pg_restore`, `psql`, `mysqldump`, `mysql` (report MySQL client variant; MariaDB client flagged unsupported because it lacks `--set-gtid-purged`); `Gzip()` flag per engine (postgres false, others true); shared `run(ctx, cmd, env, stdin, stdout, log)` helper that never puts secrets on argv
- [X] T013 Implement `internal/runner/runner.go`: `Runner` with store, oplog, event callback; `start(op)` sets `running`/`started_at`, `finish(op, status, message)` sets terminal state + `finished_at` and publishes event; in-memory lock sets keyed by plan id and by `(connection_id, database)` for restores; `Recover()` at startup: `pending`/`running` → `interrupted`, delete `*.partial` in all destination dirs and `<share_path_local>/<op-id>.bak` files of those ops (FR-026); test Recover in `internal/runner/runner_test.go`
- [X] T014 [P] Implement `internal/server/server.go`: `http.ServeMux` with Go 1.22 patterns, JSON helpers (`writeJSON`, `readJSON` with 1 MiB limit, error body `{"error","code"}`), middleware: require `X-Requested-With: dumpling` on POST/PUT/DELETE, auth required for `/api/*` except `GET/POST /api/setup` and `POST /api/login`; serve `web.FS` with SPA fallback to `index.html`
- [X] T015 Implement `internal/server/auth.go`: `GET/POST /api/setup` (POST allowed only while `user` table is empty, else 409 `already_setup`; username 1–64 chars), `POST /api/login` (bcrypt compare, 32-byte random token, cookie `dumpling_session` HttpOnly, SameSite=Strict, Secure when TLS; store SHA-256 of token; sliding 7-day expiry), `POST /api/logout`, per-IP rate limit 5 attempts/min → 429; tests in `internal/server/auth_test.go` (setup once, bad password 401, rate limit, missing CSRF header 403, expired session 401)
- [X] T016 [P] Implement `internal/server/events.go`: SSE hub `GET /api/events` publishing `operation` (row JSON) and `log` (`{"id","line"}`) events, heartbeat comment every 25 s, drops slow clients
- [X] T017 [P] Implement `GET /api/system` in `internal/server/system.go` returning version, `engine.Tools()` result, `tls`, `bind`
- [X] T018 Implement `cmd/dumpling/main.go`: flags `--data-dir` (default `./data`), `--bind` (default `127.0.0.1:9898`), `--tls-cert`, `--tls-key`; create data dir + `logs/` + `tmp/`; open store, load secret key, `runner.Recover()`, start HTTP(S); log prominent warning when bind host is not loopback and TLS is off (FR-027a); graceful shutdown on SIGINT/SIGTERM
- [X] T019 [P] Implement web shell: `web/src/api.ts` (fetch wrapper adding `X-Requested-With: dumpling`, JSON errors → thrown `ApiError{code,message}`), `web/src/events.ts` (`EventSource` hook `useEvents()`), `web/src/main.tsx` + `web/src/App.tsx` router with layout/nav, `web/src/pages/Setup.tsx`, `web/src/pages/Login.tsx` (redirects: setup needed → Setup; 401 → Login), `web/src/styles.css`

**Checkpoint**: Foundation ready — login works, empty UI served from the binary.

---

## Phase 3: User Story 1 - On-demand backup of a registered database (Priority: P1) 🎯 MVP

**Goal**: Register connections/destinations and take verified hot backups of PostgreSQL, MySQL, SQL Server.

**Independent Test**: Register one connection per engine, back up `shop` under `testdata/load.sh` load; each backup listed as `ok` with checksum/metadata, load reports 0 failed writes.

### Implementation for User Story 1

- [X] T020 [P] [US1] Implement `internal/engine/postgres.go`: `Test` via `psql -Atc` (version from `SHOW server_version`, dbs from `pg_database WHERE NOT datistemplate`), warning if local `pg_dump` major < server major; `Backup` = `pg_dump -Fc -d <db>` to `w`; env `PGHOST/PGPORT/PGUSER/PGPASSWORD/PGSSLMODE` (from `tls_mode` `disable`/`require`/`verify-full`); test argv/env construction (password never in argv) in `internal/engine/postgres_test.go`
- [X] T021 [P] [US1] Implement `internal/engine/mysql.go`: write temp `--defaults-extra-file` in `<data-dir>/tmp` mode 0600 (user, password, host, port, ssl-mode), always removed; `Test` via `mysql -N -B -e` (`SELECT VERSION()`, `SHOW DATABASES` minus system schemas); `Backup` = `mysqldump --single-transaction --routines --triggers --events --hex-blob --set-gtid-purged=OFF <db>`; before dump query `information_schema.TABLES WHERE TABLE_SCHEMA=? AND ENGINE<>'InnoDB'` → return warning listing tables; test argv/defaults-file content in `internal/engine/mysql_test.go`
- [X] T022 [P] [US1] Implement `internal/engine/mssql.go` with `go-mssqldb`: `Test` (`@@VERSION`/`SERVERPROPERTY('ProductVersion')`, `sys.databases WHERE database_id > 4`), share probe `BACKUP DATABASE model TO DISK='<share_path_server>/probe-<id>.bak' WITH COPY_ONLY` then check readable at `share_path_local`, delete; `Backup` = `BACKUP DATABASE [db] TO DISK='<share_path_server>/<op-id>.bak' WITH COPY_ONLY, CHECKSUM`, then stream file from `share_path_local` into `w`, delete `.bak` (also on error); quote identifiers with `QUOTENAME`-style escaping; TLS from `encrypt`/`trust_server_cert`
- [X] T023 [US1] Implement connection store queries in `internal/store/connections.go` (password column sealed with `secret`), and handlers in `internal/server/connections.go`: `GET/POST /api/connections`, `PUT/DELETE /api/connections/{id}`, `POST /api/connections/test`, `GET /api/connections/{id}/databases` per contracts/api.md; validation: name "1–64 chars", port "1–65535", engine enum, mssql requires `share_path_server` and existing `share_path_local`; responses expose `password_set` never `password`; PUT without password keeps stored; DELETE → 409 `in_use` if any plan references it; test errors never echo the password
- [X] T024 [P] [US1] Implement destinations in `internal/store/destinations.go` + `internal/server/destinations.go`: CRUD per contracts/api.md; path "absolute, must exist and be writable at save time" (probe by creating/removing a temp file) → else 400 `not_writable`; `free_bytes` in list (`syscall.Statfs` on unix, `GetDiskFreeSpaceEx` on windows, in `internal/server/diskfree_*.go`); DELETE → 409 `in_use` when referenced by a plan or non-deleted backups
- [X] T025 [US1] Implement `internal/runner/backup.go`: `Backup(ctx, conn, db, dest, planID, passphrase)` creates `backup` operation, registers password/passphrase with the log redactor, copies `plan_name`/`connection_name` into the backup row, artifact path `<plan-name|adhoc>/<connection-name>/<database>/<YYYYMMDDTHHMMSSZ>-<op-id last 6>.<ext>` (ext `pgdump` | `sql.gz` | `bak.gz`, plus `.age` when encrypted), pipes `engine.Backup` into `artifact` writer, on success `Commit` sidecar + insert `backup` row (status `ok`, size, sha256, server_version, encrypted) and finish `success` or `warning` (with warnings text), on error `Abort` and finish `failed` with stderr tail; inserts no `backup` row on failure (FR-008)
- [X] T026 [US1] Implement `internal/server/backups.go`: `POST /api/backups` (on-demand, `{connection_id, databases[], destination_id, passphrase?}`, one operation per database, runs async, returns `operation_ids`), `GET /api/backups` with filters `plan_id, connection_id, database, status`, newest first, `GET /api/backups/{id}`
- [X] T027 [P] [US1] Implement `web/src/pages/Connections.tsx`: list, create/edit form (engine-dependent fields: TLS mode, mssql share paths), "Test connection" showing version, databases, warnings or error; password field write-only ("leave empty to keep")
- [X] T028 [P] [US1] Implement `web/src/pages/Destinations.tsx`: list with free space, create/edit/delete
- [X] T029 [US1] Implement `web/src/pages/Backups.tsx`: list (time, connection, database, size, checksum short, encrypted, status) with filters, "Backup now" dialog (connection → pick databases from `/databases`, destination, optional passphrase with "losing it makes the backup unrecoverable" warning), live status of started operations via `useEvents()`; show missing tools from `/api/system` as a banner (FR-004)

**Checkpoint**: MVP backup path works for all three engines.

---

## Phase 4: User Story 2 - Restore a backup to a chosen target (Priority: P1)

**Goal**: Restore any `ok` backup to any same-engine connection, optionally renamed, with safe overwrite.

**Independent Test**: Back up `shop` from `*-a`, restore to `*-b` as `shop_copy`, compare schema + row counts; restore again → confirmation required; corrupted artifact → refused, target untouched.

### Tests for User Story 2

- [X] T030 [P] [US2] Integration round-trip test (build tag `integration`) in `internal/runner/roundtrip_integration_test.go`: for each engine using `testdata/compose.yaml`: run `load.sh`-equivalent writer goroutine → `Backup` → assert 0 failed writes (SC-004) → `Restore` to `-b` as `shop_copy` → compare table list + `COUNT(*)` per table against source snapshot rows recorded at backup start (SC-003) → restore again with overwrite → same result; plus encrypted variant
- [X] T031 [P] [US2] Unit tests for restore guards in `internal/runner/restore_test.go`: engine mismatch, backup not `ok`, target exists without/with wrong confirmation, concurrent restore to same `(connection, database)`, encrypted without passphrase, checksum mismatch marks backup `corrupted` and never calls `engine.Restore`

### Implementation for User Story 2

- [X] T032 [P] [US2] Add `Exists` + `Restore` to `internal/engine/postgres.go`: overwrite ⇒ `DROP DATABASE "<db>" WITH (FORCE)` (server ≥ 13; else `pg_terminate_backend` all `pg_stat_activity` rows for db, then `DROP DATABASE`), `CREATE DATABASE "<db>"`, `pg_restore --exit-on-error -d <db>` from stdin, `--no-owner --no-acl` when `opts.SkipOwnership`
- [X] T033 [P] [US2] Add `Exists` + `Restore` to `internal/engine/mysql.go`: overwrite ⇒ `DROP DATABASE`, `CREATE DATABASE`, then `mysql <db>` with dump on stdin (defaults-extra-file credentials); when `opts.SkipOwnership`, pipe the dump through `stripDefiner` (`bufio.Reader` line by line, regex ``DEFINER=`[^`]*`@`[^`]*` ``, also inside `/*!50013 ... */`) with tests in `internal/engine/mysql_test.go`
- [X] T034 [P] [US2] Add `Exists` + `Restore` to `internal/engine/mssql.go`: write stream to `<share_path_local>/<op-id>.bak`, `RESTORE FILELISTONLY` for logical names, target paths from `SERVERPROPERTY('InstanceDefaultDataPath')`/`('InstanceDefaultLogPath')` with files named `<target>_<n>.mdf/.ldf`; if exists `ALTER DATABASE [t] SET OFFLINE WITH ROLLBACK IMMEDIATE`; `RESTORE DATABASE [t] FROM DISK=... WITH REPLACE, CHECKSUM, MOVE ...` (set ONLINE again on failure); always delete `.bak`
- [X] T035 [US2] Implement `internal/runner/restore.go`: `Preflight(req)` → `{target_exists, version_warning, passphrase_required}` (version warning when target server_version major < backup server_version major, FR-020); `Restore(req)` validates in order per data-model "Restore Request" rules 1–6, acquires `(target_connection_id, target_database)` lock (409 `restore_in_progress`), pass 1 `artifact.Checksum` must equal stored sha256 else set backup `corrupted` + fail, pass 2 stream `artifact.Open` → `engine.Restore(overwrite = target_exists)`; log "overwrite confirmed by <user> at <time>"; passphrase from plan (decrypt) or request
- [X] T036 [US2] Implement `internal/server/restores.go`: `POST /api/restores/preflight` and `POST /api/restores` with responses/codes exactly as contracts/api.md (`confirmation_required` 409 with `target_exists` + warnings, `engine_mismatch`, `backup_not_ok`, `passphrase_required`, `restore_in_progress`)
- [X] T037 [US2] Implement `web/src/pages/RestoreWizard.tsx` (opened from a backup row): step 1 target connection (only same engine listed), step 2 target database name (default = source name), preflight; if `target_exists` show warning "active connections will be terminated, database dropped and recreated" and enable Restore only when typed text === target name; version warning; passphrase field when required; "skip ownership" checkbox for postgres and mysql targets; extract the gating rule into `web/src/restoreGate.ts` with test `web/src/restoreGate.test.ts`

**Checkpoint**: Backup + restore (MVP core value) complete for all engines.

---

## Phase 5: User Story 3 - Scheduled backup plans with retention (Priority: P2)

**Goal**: Plans run automatically on cron schedules and prune by retention.

**Independent Test**: Plan every minute with `last=3`; after 5 runs exactly 3 backups remain, prune operations logged; pause stops runs; overlapping run skipped.

### Tests for User Story 3

- [X] T038 [P] [US3] Table-driven tests in `internal/schedule/retention_test.go`: last N; daily/weekly(ISO)/monthly buckets keep newest per bucket; union of rules; newest successful always kept; failed/deleted backups ignored
- [X] T039 [P] [US3] Tests in `internal/schedule/scheduler_test.go` with injectable clock: fires at next cron time, paused plan not fired, plan edit re-plans, overlap ⇒ `skipped` operation (FR-015)

### Implementation for User Story 3

- [X] T040 [P] [US3] Implement `internal/schedule/retention.go`: `Keep(backups []Backup, p Policy, now time.Time) map[id]bool` per research R10; policy `{last, daily, weekly, monthly}` "ints ≥ 0, at least one > 0"
- [X] T041 [US3] Implement plan store `internal/store/plans.go` (passphrase sealed with `secret`, `databases` "json array, ≥ 1", schedule validated with `cron.ParseStandard` incl. `CRON_TZ=` prefix) and handlers `internal/server/plans.go`: CRUD, `POST /api/plans/{id}/run`, `/pause`, `/resume` per contracts/api.md; list includes `next_run` and `last_operation`; `passphrase: ""` removes encryption for future backups; errors `invalid_schedule`, `invalid_retention`
- [X] T042 [US3] Implement `internal/runner/prune.go`: after each successful plan backup, run `prune` operation: `schedule.Keep` over plan's `ok` backups, delete artifact + sidecar of the rest, mark them `deleted`, log each deletion
- [X] T043 [US3] Implement `internal/schedule/scheduler.go`: loop with single timer to the earliest `Next()` across non-paused plans, `Reload()` on plan changes, on fire call `runner.Backup` for each plan database under the plan lock; if lock held create `skipped` operation; wire into `cmd/dumpling/main.go`
- [X] T044 [US3] Implement `web/src/pages/Plans.tsx`: list (next run, last status, paused), create/edit form (connection, databases multiselect, destination, schedule preset hourly/daily-at/weekly or raw cron, retention fields, optional passphrase with loss warning), run now/pause/resume/delete; preset→cron mapping in `web/src/cronPresets.ts` with test `web/src/cronPresets.test.ts`

**Checkpoint**: Automation works on top of US1/US2.

---

## Phase 6: User Story 4 - Operation history, logs and backup verification (Priority: P3)

**Goal**: Unified, filterable timeline with live logs and on-demand verification.

**Independent Test**: After backups/restore/verify, filter by status `failed` and by plan; open each log; verify shows result.

### Implementation for User Story 4

- [X] T045 [P] [US4] Implement `internal/server/operations.go`: `GET /api/operations` with filters `type, status, plan_id, connection_id`, cursor `before=<id>`, `limit` (default 50, max 200), newest first; `GET /api/operations/{id}`; `GET /api/operations/{id}/log` as `text/plain`
- [X] T046 [P] [US4] Implement `internal/runner/verify.go` + `POST /api/backups/{id}/verify` in `internal/server/backups.go`: `verify` operation recomputes sha256; mismatch ⇒ backup `corrupted`, op `failed`; match ⇒ `success`
- [X] T047 [P] [US4] Implement `DELETE /api/backups/{id}` in `internal/server/backups.go` requiring body `{"confirm": "<database>"}`; deletes artifact + sidecar, marks `deleted`
- [X] T048 [US4] Implement `web/src/pages/Operations.tsx` (filters type/status/plan/connection, infinite scroll by cursor, live row updates via `useEvents()`) and `web/src/pages/OperationDetail.tsx` (metadata, full log, live tail via `log` events while running); add Verify and Delete (typed confirmation) actions to `web/src/pages/Backups.tsx`

**Checkpoint**: All user stories independently functional.

---

## Phase 7: Polish & Cross-Cutting Concerns

- [X] T049 [P] Secret leak test in `internal/server/leak_test.go`: create connection + plan with known password/passphrase, run failing and succeeding ops against a fake engine, assert the strings appear nowhere in API responses, log files, or raw bytes of `dumpling.db` (SC-005)
- [X] T050 [P] Create `Dockerfile` (multi-stage: node build `web/dist`, go build, runtime debian-slim with `postgresql-client-17` from PGDG and MySQL 8.4 client from Oracle repo; `--bind 0.0.0.0:9898` with documented TLS/reverse-proxy note)
- [X] T051 [P] Create `README.md` (what it is, install, flags, master key handling and its limits per research R4, SQL Server share setup, engine caveats e.g. non-InnoDB tables, pg_dump version rule) and `CHANGELOG.md` (Unreleased: initial feature)
- [X] T052 Update `CLAUDE.md` with real build/test/run commands and package map
- [X] T053 Run `go vet ./...`, `go test ./...`, `go test -tags integration ./internal/...`, `cd web && npm test && npm run build`, then execute quickstart.md manual scenarios 1–14 and record results in `specs/001-db-backup-webui/checklists/quickstart-run.md`

---

## Dependencies & Execution Order

### Phase Dependencies

- **Setup (Phase 1)**: none
- **Foundational (Phase 2)**: after Setup — blocks all stories
- **US1 (Phase 3)**: after Foundational
- **US2 (Phase 4)**: after Foundational; needs backups to exist ⇒ practically after US1 (T025)
- **US3 (Phase 5)**: after US1 (reuses `runner.Backup`); independent of US2
- **US4 (Phase 6)**: after Foundational; meaningful once US1 produces operations
- **Polish**: after desired stories

### Key task dependencies

- T007 → T008 → (T013, T015, T023, T024, T041)
- T012 → T020/T021/T022 → T025 → T026 → T029
- T010 + T011 + T013 → T025, T035, T042, T046
- T020–T022 → T032–T034 (same files, same engine: sequential per engine)
- T035 → T036 → T037; T040 → T042 → T043

### Parallel Opportunities

- Setup: T002–T006
- Foundational: T009, T010, T011 together; T014, T016, T017, T019 together
- US1: T020, T021, T022 (one per engine) and T024, T027, T028
- US2: T030, T031 tests; T032, T033, T034 per engine
- US3: T038, T039, T040
- US4: T045, T046, T047

## Parallel Example: User Story 1

```text
Task: "Implement internal/engine/postgres.go (T020)"
Task: "Implement internal/engine/mysql.go (T021)"
Task: "Implement internal/engine/mssql.go (T022)"
Task: "Implement destinations store + handlers (T024)"
```

## Implementation Strategy

### MVP First

1. Phase 1 + Phase 2
2. Phase 3 (US1) → validate quickstart scenarios 1–4
3. Phase 4 (US2) → validate 5–8, 11 (backup without restore is not yet a usable product: ship MVP after US2)

### Incremental Delivery

1. Setup + Foundational → login + empty UI
2. US1 → backups
3. US2 → restore (**first releasable version**)
4. US3 → scheduling + retention
5. US4 → history/verify
6. Polish

## Notes

- Commit after each task or logical group, on `feature/001-db-backup-webui`
- After each applied change update README.md / CLAUDE.md / CHANGELOG.md per CLAUDE.md
- Stop at any checkpoint to validate the story independently
