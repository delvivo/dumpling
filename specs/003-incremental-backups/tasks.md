# Tasks: Incremental Backups

**Input**: Design documents from `specs/003-incremental-backups/`
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.md, quickstart.md

**Tests**: included — Constitution VI requires automated tests for scheduling, retention, command
construction and restore safety, and a real round-trip per engine.

**Organization**: by user story. US1–US3 are P1 and build on each other in the order SQL Server
backup → chain restore/verify → retention; US4 (MySQL) and US5 (PostgreSQL) plug engines into the
same chain machinery.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: different files, no dependency on an incomplete task
- Paths are repo-relative; design references: R# = research.md section

---

## Phase 1: Setup

- [ ] T001 Verify `pg_basebackup`, `pg_combinebackup` (from `postgresql-client-17`) and `mysqlbinlog` (from `mysql-client`) exist in the `tools` stage; add packages if missing in `Dockerfile`
- [ ] T002 [P] Start pg-a with `command: ["postgres", "-c", "summarize_wal=on"]` (keep pg-b PG 16 without it) in `testdata/compose.yaml`

---

## Phase 2: Foundational (blocks all stories)

- [ ] T003 Add migration #2 in `internal/store/migrate.go`: `plans` ADD COLUMN `incr_schedule TEXT NOT NULL DEFAULT ''`, `max_chain INTEGER NOT NULL DEFAULT 30`, `chain_reset_at TEXT NOT NULL DEFAULT ''`, `incr_ack_at TEXT NOT NULL DEFAULT ''`; `backups` ADD COLUMN `kind TEXT NOT NULL DEFAULT 'full'`, `chain_id TEXT NOT NULL DEFAULT ''`, `parent_id TEXT NOT NULL DEFAULT ''`, `seq INTEGER NOT NULL DEFAULT 0`, `marker TEXT NOT NULL DEFAULT ''`; extend `internal/store/migrate_test.go` for user_version 2
- [ ] T004 Extend `Plan` (`IncrSchedule`, `MaxChain`, `ChainResetAt`, `IncrAckAt`; JSON `incr_schedule`, `max_chain`, `incr_ack_at`) and `Backup` (`Kind`, `ChainID`, `ParentID`, `Seq`, `Marker`; JSON `kind`, `chain_id`, `parent_id`, `seq`, `marker`) with cols/scan/insert/update in `internal/store/entities.go`; constant `ClusterDB = "*"`
- [ ] T005 Add store queries `ChainHead(planID, database string) (Backup, bool, error)` (newest `ok` backup with `chain_id != ''`), `Ancestors(b Backup) ([]Backup, error)` (parent walk to the full, oldest first; returns an error naming the first missing or non-`ok` link) and `Dependents(id string) ([]Backup, error)` (transitive, status ≠ deleted) in `internal/store/entities.go`
- [ ] T006 [P] Test `ChainHead`/`Ancestors`/`Dependents` (complete chain, missing link, corrupted link, mssql star-shaped chain where every diff's parent is the full) in `internal/store/chain_test.go`
- [ ] T007 Define in `internal/engine/engine.go` per research R12: `ErrChainBroken`, `ErrLockUnsafe`; `type Link struct{ Marker, Path, State string }`; optional interfaces `Incremental { Prereqs(ctx, c Conn, db string) (missing, warnings []string, err error); BackupFull(ctx, c, db string, w, state, log io.Writer) (marker string, warnings []string, err error); BackupIncr(ctx, c, db string, prev Link, w, state, log io.Writer) (marker string, warnings []string, err error) }` (`state` nil except postgres) and `ChainRestorer { RestoreChain(ctx, c Conn, db string, overwrite bool, links []Link, opts RestoreOpts, log io.Writer) error }`; `Gzip(engine string, physical bool) bool` (postgres physical → true); `Ext` for chain artifacts (`bak.gz` mssql diff, `binlog.tar.gz` mysql incr, `tar.gz` postgres physical); add `mysqlbinlog`, `pg_basebackup`, `pg_combinebackup` to `Tools()` and `IncrTools(engine)`; update callers of `Gzip` in `internal/runner/`
- [ ] T008 [P] Shared safe extraction `untar(r io.Reader, dir string) error` per R11 (reject absolute names, `..` components, entries resolving outside `dir`, symlinks/hardlinks, types other than regular file/dir) in `internal/engine/tarx.go` with tests in `internal/engine/tarx_test.go`
- [ ] T009 [P] Add `--restore-dir` flag (default `<data-dir>/restores`, env `DUMPLING_RESTORE_DIR`, created 0700 at startup) passed to the runner in `cmd/dumpling/main.go` and `internal/runner/runner.go`
- [ ] T010 [P] Add `schedule.Closure(keep map[string]bool, all []store.Backup) map[string]bool` (adds every ancestor via `ParentID`) in `internal/schedule/retention.go` with tests (kept incr pulls full + intermediates; dropped full leaves no kept dependents) in `internal/schedule/retention_test.go`
- [ ] T011 [P] Scheduler with two schedules: `Next` for `Schedule` and `IncrSchedule`, `due` returns `(plan, kind)` pairs, full wins when both fire in the same tick; `Run func(store.Plan, string)` in `internal/schedule/scheduler.go`; tests in `internal/schedule/scheduler_test.go`; adapt callback in `cmd/dumpling/main.go`
- [ ] T012 Runner kind decision `decideKind(p store.Plan, db string, requested string) (kind string, parent store.Backup, reason string)` implementing R2: (1) no open chain → "no base"; (2) `store.Ancestors(head)` errors → "chain broken: link <id> <status>"; (3) chain length ≥ `max_chain` → "chain length limit"; (4) `chain_reset_at` > chain full `started_at` → "plan changed" in `internal/runner/chain.go`, with a table test per rule in `internal/runner/chain_test.go`
- [ ] T013 Backup flow in `internal/runner/backup.go`: `BackupReq.Kind`; for plans with `IncrSchedule != ''` and engines implementing `Incremental` call `Prereqs` (missing → full + op status `warning`, message `prerequisite missing: <list>`; warnings appended to the op warnings), then `BackupFull`/`BackupIncr` (passing a temp file as `state` for postgres); on `ErrChainBroken` abort the upload and run a full in the same operation logging the reason; on `ErrLockUnsafe` abort the upload, record op `warning` "long-running statement; full postponed", produce no backup, no fallback; set `Kind`, `ChainID` (own id for full), `ParentID`, `Seq`, `Marker` on the row and add them plus `backup_id` to the sidecar; `RunPlan(p, kind)`
- [ ] T014 Plan API in `internal/server/handlers.go`: accept/return `incr_schedule` (400 `invalid_schedule`), `max_chain` (1–1000, 400 `invalid`), `ack_incremental_risk` (mssql or mysql turning incremental on without it and without stored `incr_ack_at` → 400 `ack_required`; stores `incr_ack_at`), postgres + incremental requires `databases == ["*"]` (else 400 `invalid`), prerequisite check on enabling via `Incremental.Prereqs` (400 `prerequisite_missing` `{"missing": [...]}`); `databases` containing `"*"` with `incr_schedule == ''` → 400 `invalid`; turning `incr_schedule` to `''` clears `incr_ack_at`; set `chain_reset_at = now` when connection, destination, databases or passphrase change; `next_incr_run` in list; `POST /api/plans/{id}/run?kind=full|incremental`
- [ ] T015 [P] Backup list/detail add `kind`, `chain_id`, `parent_id`, `seq`, `chain_ok` (all ancestors exist and `ok`) in `internal/server/handlers.go`
- [ ] T016 [P] Types for new plan/backup/preflight fields in `web/src/types.ts` and calls (`runPlan(id, kind)`) in `web/src/api.ts`

**Checkpoint**: chain columns, scheduling, kind decision and API in place; no engine produces incrementals yet.

---

## Phase 3: User Story 1 — SQL Server differentials (P1) 🎯 MVP

**Goal**: SQL Server plans in incremental mode produce a full then cumulative differentials.

**Independent test**: quickstart §1 steps 1–4.

- [ ] T017 [US1] `mssql` implements `Incremental` in `internal/engine/mssql.go`: `BackupFull` = `BACKUP DATABASE @p1 TO DISK = @p2 WITH CHECKSUM, INIT` (no COPY_ONLY) then read `backup_set_uuid` from `msdb.dbo.backupset` (latest `type='D'` for the db) → marker `{"base_guid"}`; `BackupIncr` compares `sys.master_files.differential_base_guid` (`file_id = 1`) with `prev` marker → `ErrChainBroken` on mismatch, else `WITH DIFFERENTIAL, CHECKSUM, INIT`, marker carries same `base_guid`; `Prereqs` → none (share already probed by Test)
- [ ] T018 [US1] Runner parent rule for mssql: `ParentID` = chain full, `Seq` = diffs in chain + 1 in `internal/runner/backup.go`
- [ ] T019 [P] [US1] Unit test fallback paths with a fake engine (no base → full; corrupted intermediate → full; `ErrChainBroken` → upload aborted + full in same op; prerequisite missing → warning; `ErrLockUnsafe` → warning, no backup row) in `internal/runner/chain_fallback_test.go`
- [ ] T020 [US1] Plans page: "Incremental backups" toggle, incremental schedule (reuse cron presets), max chain, engine-specific warning with required acknowledgment checkbox (SQL Server: differential base of other tools; blocks save) in `web/src/pages/Plans.tsx`; "Run incremental now" row action
- [ ] T021 [US1] Backups page: Type column (`Full`/`Incremental #seq`, `data-label`), chain membership (link to base), broken-chain badge when `chain_ok` false in `web/src/pages/Backups.tsx`

**Checkpoint**: US1 works; restore of a diff not yet supported (blocked in UI).

---

## Phase 4: User Story 2 — Restore and verify a chain (P1)

**Goal**: restore any link by applying the links it depends on; verify checks every link.

**Independent test**: quickstart §1 step 5 and §2.

- [ ] T022 [US2] Restore planning in `internal/runner/restore.go`: resolve `store.Ancestors`; resolve the passphrase for **each** link (request → link's unlocked passphrase → plan): a locked link without one → 400 `backup_locked` `{"link": id}`, any other missing → 400 `passphrase_required` `{"link": id}`, all in preflight before downloading anything; preflight adds `chain` (`[{id, kind, seq, status, started_at}]`) and `chain_error`; `StartRestore` refuses with 400 `chain_broken` `{"link": id}`; for chain backups download + checksum the backup and **all** its ancestors (mark mismatches `corrupted`) before touching the target, then call `ChainRestorer.RestoreChain` with decoded payload files in order; standalone backups unchanged
- [ ] T023 [US2] `mssql` implements `ChainRestorer` in `internal/engine/mssql.go`: write full and the chosen diff to the share, `RESTORE DATABASE … WITH NORECOVERY, CHECKSUM[, REPLACE]` + MOVE from the full, `RESTORE DATABASE … WITH RECOVERY, CHECKSUM` from the diff (only the full when restoring the full); on failure drop the database if it did not exist before, else report it is left in RESTORING state
- [ ] T024 [US2] Verify chain in `internal/runner/maintain.go`: `StartVerify` on a link checksums every ancestor, marks failing ones `corrupted`, message lists per-link results
- [ ] T025 [P] [US2] Unit tests: preflight with broken chain refuses and never calls the engine; locked intermediate link → `backup_locked` naming it, nothing downloaded; checksum mismatch on any link aborts before target in `internal/runner/restore_chain_test.go`
- [ ] T026 [US2] Restore wizard shows the links to be applied, blocks with the named broken link, keeps existing confirm rules in `web/src/pages/RestoreWizard.tsx`; gate on `chain_error` in `web/src/restoreGate.ts` with test in `web/src/restoreGate.test.ts`
- [ ] T027 [US2] Integration round-trip: mssql full + 3 diffs, ≤ 5% of rows changed between links; assert each diff `SizeBytes <= full.SizeBytes/5`, log durations (no time assertion); restore last diff to mssql-b and compare; external full → base-changed fallback in `internal/runner/roundtrip_integration_test.go`

**Checkpoint**: SQL Server incremental feature complete end to end.

---

## Phase 5: User Story 3 — Retention keeps chains restorable (P1)

**Goal**: pruning never breaks a kept backup; no orphans.

**Independent test**: quickstart §3.

- [ ] T028 [US3] `prune` in `internal/runner/maintain.go`: when `p.IncrSchedule != ''` group backups by database and `keep = schedule.Closure(schedule.Keep(group, p.Retention), group)`; otherwise unchanged (`Keep` over all plan backups); also delete `<rel>.manifest` objects and `<data-dir>/manifests/<id>` when present
- [ ] T029 [US3] Cascade delete: `DeleteBackup(id, confirm, cascade)` returns 409 `has_dependents` `{"dependents": N}` without cascade, else deletes dependents (newest first) then the backup in `internal/runner/maintain.go`; body `cascade` in `internal/server/handlers.go`
- [ ] T030 [P] [US3] Test prune with keep-last-3 over full + 5 incrementals (mysql-shaped and mssql-shaped chains), unchanged behavior for non-incremental plans, and cascade delete in `internal/runner/prune_chain_test.go`
- [ ] T031 [US3] Delete confirm dialog shows dependent count and offers cascade (uses `Confirm` from `Modal.tsx`) in `web/src/pages/Backups.tsx`

**Checkpoint**: all P1 stories done — shippable MVP for SQL Server.

---

## Phase 6: User Story 4 — MySQL binary log incrementals (P2)

**Goal**: MySQL plans capture binlog increments and replay them renamed.

**Independent test**: quickstart §4.

- [ ] T032 [US4] `mysql.Prereqs` in `internal/engine/mysql.go`: `log_bin` ON; grants include `REPLICATION SLAVE` (or `REPLICATION REPLICA`), `REPLICATION CLIENT`, `RELOAD` (parse `SHOW GRANTS`, accept `ALL PRIVILEGES`); `PROCESS` missing → returned in `warnings` ("without PROCESS the pre-check cannot see other sessions; the 5 s watchdog still applies"); `mysqlbinlog` present and not MariaDB; `binlog_format <> 'ROW'` → returned in `warnings` (not missing)
- [ ] T033 [US4] `mysql.BackupFull` in `internal/engine/mysql.go`: first the lock-safety pre-check (R4) — `information_schema.PROCESSLIST` rows with `COMMAND NOT IN ('Sleep','Binlog Dump','Binlog Dump GTID','Daemon')`, `TIME > 10` and `INFO` not starting with `SELECT` → `ErrLockUnsafe`; then start the watchdog (R4 c): second connection polls PROCESSLIST every 250 ms, dump session identified via connection attribute `program_name=dumpling-<opid>` (fallback: same user, `INFO LIKE 'FLUSH TABLES%'`), `KILL QUERY` if it waits on the global read lock (`Waiting for global read lock` / `table flush` / `commit lock`) for more than 5 s, stops once the `SOURCE_LOG_FILE` line has been read; a killed dump maps to `ErrLockUnsafe`; then `mysqldumpArgs` + `--source-data=2`; parse `SOURCE_LOG_FILE='…', SOURCE_LOG_POS=…` from the first 100 lines of the stream (tee, no buffering of the whole dump) → marker `{"server_uuid","file","pos"}` (`SELECT @@server_uuid`); error if not found; check (ctx7, MySQL 8.4 docs) whether `[mysqldump] lock-wait-timeout` is supported and add it to `defaultsFile` if so
- [ ] T034 [US4] `mysql.BackupIncr` in `internal/engine/mysql.go`: `SELECT @@server_uuid` ≠ `prev` marker `server_uuid` → `ErrChainBroken`; `SHOW BINARY LOG STATUS` (fallback `SHOW MASTER STATUS`) → end file/pos; `SHOW BINARY LOGS` must contain `prev.file` else `ErrChainBroken`; `mysqlbinlog --defaults-extra-file … --read-from-remote-server --raw --result-file=<tmpdir>/ <files start..end>`; stream files into `w` as tar (`archive/tar`); marker `{"server_uuid","start_file","start_pos","file","pos"}`
- [ ] T035 [US4] `mysql.RestoreChain` in `internal/engine/mysql.go`: restore full via existing `Restore`; for each incr link extract with `untar` (T008) to a temp dir and pipe `mysqlbinlog --skip-gtids --start-position=<start_pos> --stop-position=<pos> --database=<target> --rewrite-db='<source>-><target>' <files…>` into `mysql <target>` (verify `--database` vs `--rewrite-db` order against MySQL 8.4 docs via ctx7 first; fix the arg if it filters on the source name); on any failure after the full has loaded: drop the target if it did not exist before the restore, otherwise fail with "target database `<name>` is partially restored (full + links 1..k)"
- [ ] T036 [P] [US4] Unit tests: position parsing (8.4 `CHANGE REPLICATION SOURCE TO`, 8.0 `CHANGE MASTER TO`), PROCESSLIST lock-safety filter, watchdog decision (state + elapsed → kill / no kill) as a pure table-tested function, server identity mismatch → `ErrChainBroken`, file range selection, mysqlbinlog arg construction (no secrets on argv) in `internal/engine/engine_test.go`
- [ ] T037 [US4] Runner parent rule for mysql: `ParentID` = previous link, `Seq` = parent.seq + 1 in `internal/runner/backup.go`
- [ ] T038 [US4] Integration round-trip in `internal/runner/roundtrip_integration_test.go`:
  - full + 3 incrementals with insert/update/delete, ≤ 5% of rows changed between links; assert each incr `SizeBytes <= full.SizeBytes/5`
  - restore the 3rd incr to mysql-b, and to mysql-a under a new name (GTID skip)
  - purge → full fallback
  - long-running write → `ErrLockUnsafe` warning, no backup
  - long `UPDATE` / `LOCK TABLES … WRITE` started after the pre-check → dump killed within 6 s (5 s limit + 250 ms polling + kill latency), warning, no backup; concurrent writes resume
  - connection pointed at mysql-b → server identity changed → full
- [ ] T039 [US4] Plans page: MySQL acknowledgment checkbox (global read lock at each full; binlog required; statement-based limitation), blocks save, in `web/src/pages/Plans.tsx`

---

## Phase 7: User Story 5 — PostgreSQL physical incrementals (P3)

**Goal**: cluster-wide physical chains restored to a data directory inside the restore root.

**Independent test**: quickstart §5.

- [ ] T040 [US5] `postgres.Prereqs` in `internal/engine/postgres.go`: server ≥ 17 (`server_version_num`), `SHOW summarize_wal` = on, `rolreplication OR rolsuper` for current user, `pg_basebackup`/`pg_combinebackup` present with major ≥ server
- [ ] T041 [US5] `postgres.BackupFull`/`BackupIncr` in `internal/engine/postgres.go`: `SELECT system_identifier FROM pg_control_system()` stored in the marker by the full, mismatch with `prev` → `ErrChainBroken`; `pg_basebackup -D - -Ft -X fetch --checkpoint=fast [--incremental=<prev.State>]` with `pgEnv`; tee stdout through an `archive/tar` reader and write member `backup_manifest` to `state` (first verify on PG 17 that the manifest is injected; if not, switch to `-D <tmpdir> -Ft`, stream `base.tar` into `w` and copy `backup_manifest` from disk into `state` — R5)
- [ ] T042 [US5] Runner for postgres chains in `internal/runner/backup.go`: database `store.ClusterDB`, one operation per run; after commit upload the `state` temp file as `<rel>.manifest` via `artifact.Create` (plan passphrase) and copy it to `<data-dir>/manifests/<backup_id>`; for incr set `prev.State` from the cache, else download + decrypt from the destination; marker `{"system_id": "…", "manifest": "<rel>.manifest"}`; `ParentID` = previous link
- [ ] T043 [US5] `postgres.RestoreChain` in `internal/engine/postgres.go`: extract each link with `untar` (T008) into `<data-dir>/tmp/<op>/<seq>/`, run `pg_combinebackup <dirs oldest→newest> -o <target_path>`, cleanup temps
- [ ] T044 [US5] Restore request `target_path` for postgres chain backups in `internal/runner/restore.go` and `internal/server/handlers.go`:
  - required (400 `target_path_required`)
  - relative to the restore root (T009); after `filepath.Clean` and `filepath.EvalSymlinks` of the existing parent it must stay inside the root (400 `target_path_outside_root`)
  - must be missing or empty (400 `target_path_not_empty`); preflight `target_path_ok`
  - no target connection; restore lock key `restore:path:<abs>`
  - success message gives the absolute path, the PostgreSQL major, and "chown to the server user, mode 0700"
  - tests for traversal (`../x`, symlinked parent) in `internal/runner/restore_chain_test.go`
- [ ] T045 [P] [US5] Unit tests: `pg_basebackup` arg construction, prerequisite parsing, system identifier mismatch → `ErrChainBroken` in `internal/engine/engine_test.go`
- [ ] T046 [US5] Integration round-trip in `internal/runner/roundtrip_integration_test.go`:
  - pg-a full + 3 incrementals with changes in two databases
  - restore into the restore root, start `postgres` from it inside the `it` container (or `pg_verifybackup`/`pg_ctl` if available) and compare
  - pg-b (16) → prerequisite missing
- [ ] T047 [US5] PostgreSQL UI in `web/src/pages/Plans.tsx`, `web/src/pages/Backups.tsx`, `web/src/pages/RestoreWizard.tsx`:
  - Plans: PostgreSQL + incremental hides the database picker, shows "Entire server" and the restore explanation; disabling incremental shows an empty database picker that must be filled
  - Backups: renders `*` as "Entire server"
  - Restore wizard: asks for a folder name inside the restore root instead of connection/database

---

## Phase 8: Polish & cross-cutting

- [ ] T048 Rescan imports `kind`, `chain_id`, `parent_id`, `seq`, `marker` and remaps sidecar `backup_id`s to new ids after importing a destination's sidecars; incomplete imported chains show broken in `internal/runner/rescan.go`; test in `internal/runner/rescan_test.go`
- [ ] T049 [P] Secret-leak coverage: `mysqlbinlog` and `pg_basebackup` invocations never carry the password on argv and op logs redact it, in `internal/server/leak_test.go` (or `internal/engine/engine_test.go` argv assertions)
- [ ] T050 [P] Add a pointer to `specs/003-incremental-backups/contracts/api.md` in `specs/001-db-backup-webui/contracts/api.md`
- [ ] T051 [P] Docs:
  - `README.md`: incremental mode, per-engine prerequisites, MySQL read-lock acknowledgment and postponement, SQL Server COPY_ONLY effect, PostgreSQL directory restore and `--restore-dir`
  - `CLAUDE.md`: architecture (chains, engine interfaces, tarx, restore root, new tools)
  - `CHANGELOG.md`: feature entry
- [ ] T052 Run `go vet ./... && go test ./...`, `cd web && npm test && npm run build`, compose `it` round-trips; walk quickstart.md

---

## Dependencies & execution order

- Setup (T001–T002) → Foundational (T003–T016) → US1 (T017–T021) → US2 (T022–T027) → US3 (T028–T031)
- US4 (T032–T039) and US5 (T040–T047) depend on Foundational + US2 restore plumbing (T022) + US3 prune (T028); independent of each other
- Polish after desired stories
- Within Foundational: T003 → T004 → T005; T007, T008, T009 independent of store tasks; T012 needs T005; T013 needs T007+T012; T014 needs T004+T007

## Parallel examples

- Foundational: T006, T008, T009, T010, T011, T016 in parallel once T004/T005 land
- US1: T019 alongside T020/T021
- US4 and US5 phases can proceed in parallel by different implementers (different engine files; shared runner files touched in small, separate functions)

## Implementation strategy

1. MVP = Phases 1–5 (SQL Server incrementals with chain restore, verify, retention). Validate quickstart §1–§3, ship.
2. Add US4 (MySQL), validate §4.
3. Add US5 (PostgreSQL), validate §5.
4. Polish (rescan, leak coverage, docs) before merge to `main`.
