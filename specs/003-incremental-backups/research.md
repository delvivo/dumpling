# Research: Incremental Backups

All Technical Context unknowns resolved below. Format: Decision / Rationale / Alternatives.

## R1. Chain model (all engines)

- **Decision**: A chain is per (plan, database). Every backup row gets `kind` (`full`|`incr`),
  `chain_id` (= id of the chain's full backup; `''` for standalone backups made outside incremental
  mode), `parent_id` (the link it applies on top of), `seq` (0 for the full) and `marker` (small
  engine resume state, JSON). The sidecar carries the same fields so rescan rebuilds chains.
  The parent depends on the engine:
  - SQL Server: differentials are **cumulative** since the base → `parent_id` = the full. Restoring
    diff N needs only full + diff N.
  - MySQL, PostgreSQL: each link applies on top of the previous one → `parent_id` = previous link.
  Restore/verify/retention follow `parent_id` pointers, so the engine difference is in one place
  (who the parent is at backup time), not spread over the code.
- **Rationale**: A single generic "dependency" pointer covers both cumulative and differential
  chains; retention closure and restore resolution are a parent walk.
- **Alternatives**: separate `chains` table — rejected: duplicate state the backups rows already
  hold; nothing to store on a chain itself.

## R2. When a run becomes a full (FR-005, FR-006, FR-017)

- **Decision**: An incremental run takes a full instead when any of these holds, logging the reason
  in the operation log (and message for prerequisite cases):
  1. no `ok` link of an open chain for (plan, database), or the chain head's ancestors are not all
     present and `ok` ("chain broken: link <id> <status>");
  2. chain length reached `plans.max_chain` (default 30);
  3. plan changed connection, destination, databases or passphrase after the chain started
     (`plans.chain_reset_at` > chain full `started_at`; set by plan update when any of those change);
  4. prerequisite check fails → full + operation status `warning`, message
     `prerequisite missing: <what>` (shown in history and on the plan via `last_operation`);
  5. engine detects a broken base at capture (`engine.ErrChainBroken`: SQL Server base GUID differs,
     MySQL start binlog purged, PostgreSQL WAL summaries since the previous link gone — the server
     answers "WAL summaries are required on timeline … but the summaries for that timeline and LSN
     range are incomplete" (or "… no summaries … exist"), reproduced by deleting
     `pg_wal/summaries` after a full — or the server identity in the marker — `@@server_uuid`,
     `pg_control_system().system_identifier` — differs from the server reached now) → upload of the
     incremental is aborted and a full runs in the same operation.
- **Rationale**: Every invalid case ends in a protecting full, never in a link that cannot restore
  (SC-004). Reason-in-log satisfies FR-005; warning status reuses existing UI surfaces.
- **Alternatives**: failing the run — rejected in clarification (Q4).

## R3. SQL Server differential

- **Decision**: In incremental mode, fulls are `BACKUP DATABASE … WITH CHECKSUM, INIT` (no
  `COPY_ONLY`), then the app reads `backup_set_uuid` of that backup from `msdb.dbo.backupset`
  (latest type `D` row for the db) and stores it as marker `{"base_guid": "…"}`. Incrementals:
  first compare `sys.master_files.differential_base_guid` (file_id 1) with the chain's `base_guid`;
  mismatch → `ErrChainBroken` (someone else took a full). Otherwise
  `BACKUP DATABASE … WITH DIFFERENTIAL, CHECKSUM, INIT`.
  Restore: write full and chosen diff to the share, `RESTORE … WITH NORECOVERY[, REPLACE]` from the
  full, `RESTORE … WITH RECOVERY` from the diff. On failure after a NORECOVERY step the database is
  dropped if it did not exist before; if it existed, it was already replaced, so it is left in
  `RESTORING` state and the error says so (same risk class as today's failed overwrite).
  Non-incremental plans keep `COPY_ONLY` unchanged.
- **Rationale**: Native, already-used mechanism; cumulative diffs keep restore to two steps.
- **Alternatives**: transaction log backups — out of scope (spec Assumptions).

## R4. MySQL binary log

- **Decision**:
  - Full in incremental mode: `mysqldump … --source-data=2` (records `CHANGE REPLICATION SOURCE TO
    SOURCE_LOG_FILE=…, SOURCE_LOG_POS=…` as a comment near the top); the engine parses it from the
    stream (first ~100 lines) into marker `{"file":"binlog.000042","pos":157}`. Needs `RELOAD`. The MySQL
    docs confirm `--source-data` with `--single-transaction` still takes `FLUSH TABLES WITH READ
    LOCK` at dump start and "the MySQL server may get stalled until those statements finish" when
    long updates run; no lock-free way to tie the binlog position to the dump snapshot exists
    (`log_status` cannot share mysqldump's snapshot; GTID capture has known consistency gaps).
    Allowed by Principle III v1.1.0 under two conditions: (a) explicit acknowledgment on the plan
    (R9); (b) abort instead of stall — before the dump, query `information_schema.PROCESSLIST` for
    any command other than `Sleep`/`Binlog Dump`/`Daemon` with `TIME > 10` and a non-`SELECT`
    `INFO`; if found, return `ErrLockUnsafe`: the runner records the operation as `warning`
    ("long-running statement; full postponed"), produces no backup and does not fall back.
    Seeing other users' sessions needs the `PROCESS` privilege; without it the pre-check only sees
    Dumpling's own sessions, so `Prereqs` reports a warning (not a blocker) — the watchdog only
    inspects and kills the account's own dump session and still applies.
    (c) watchdog — the guarantee: while the dump starts, a second connection polls
    `information_schema.PROCESSLIST` every 250 ms for the dump's session (connection attribute
    `program_name=dumpling-<opid>`; fallback: same user, `COMMAND='Query'`,
    `INFO LIKE 'FLUSH TABLES%'`). If its `STATE` is `Waiting for global read lock`,
    `Waiting for table flush` or `Waiting for commit lock` for more than 5 s → `KILL QUERY <id>`;
    mysqldump exits with an error, the upload is aborted and the result is `ErrLockUnsafe`. The
    watchdog stops once the `SOURCE_LOG_FILE` line has been read from the stream (lock released).
    Writes are therefore blocked for at most ~5 s. Verify during implementation whether
    `program_name` can be set from the defaults file (else `--init-command` or the fallback match)
    and whether `mysqldump` accepts `lock-wait-timeout` in `[mysqldump]` as an extra bound (not the
    guarantee).
  - Incremental: `SHOW BINARY LOG STATUS` (8.4; fallback `SHOW MASTER STATUS` for 8.0) gives end
    file/pos; `SHOW BINARY LOGS` must still contain the start file, else `ErrChainBroken` (purged).
    Fetch raw files start..end with `mysqlbinlog --read-from-remote-server --raw` into
    `<data-dir>/tmp/<op>/`, then stream them into the artifact as a **tar** (stdlib `archive/tar`).
    Marker: `{"start_file","start_pos","file","pos"}` (end = next link's start).
  - Raw (not decoded SQL) is stored because row events embed the source database name and must be
    rewritten at restore to the user-chosen target name.
  - Restore of a chain: restore the full dump into the target (existing path), then for each link in
    order: untar with the shared safe extractor (R11) to a temp dir, run `mysqlbinlog --skip-gtids --start-position=<start_pos>
    --stop-position=<pos> --database=<target> --rewrite-db=<source>-><target> <files…> | mysql`.
    `--skip-gtids` is mandatory: restoring onto the source server would otherwise silently skip
    transactions whose GTIDs are already executed. (`--database` is evaluated after `--rewrite-db`,
    hence the target name — verify against MySQL 8.4 docs during implementation.)
  - Prerequisites: `log_bin=ON`; grants `REPLICATION SLAVE` (replica) and `REPLICATION CLIENT`,
    `RELOAD`; `binlog_format` ≠ `ROW` → warning (statement-based filtering limitation, spec
    Assumptions); `mysqlbinlog` on PATH (Oracle client, same MariaDB check as today).
- **Rationale**: Only server-side incremental mechanism MySQL Community has; logical full keeps
  "restore under any name".
- **Alternatives**: decoded SQL text at capture — rejected (cannot retarget row events); GTID-based
  positions — rejected (not all servers run GTID mode; file/pos always works).

## R5. PostgreSQL physical incremental (17+)

- **Decision**:
  - Full: `pg_basebackup -D - -Ft -X fetch --checkpoint=fast` (single tar on stdout, WAL included,
    manifest injected into the tar as `backup_manifest`). Incremental: same plus
    `--incremental=<prev manifest file>`.
  - The backup stream is teed through `archive/tar` to capture `backup_manifest`, which the engine
    writes to the `state` writer the runner passes (R12); the runner stores it (a) as a second object `<rel>.manifest` on the destination via the artifact pipeline
    (encrypted with the plan passphrase if any) so imports keep working, and (b) in a local cache
    `<data-dir>/manifests/<backup_id>`; the next incremental uses the cache and downloads on miss.
    Marker: `{"manifest": "<rel>.manifest"}`.
  - The app gzips the tar (`engine.Gzip` becomes per artifact: postgres physical = gzip).
  - Restore (clarified: option A): user gives `target_path` relative to the restore root
    (`--restore-dir` / `DUMPLING_RESTORE_DIR`, default `<data-dir>/restores`); after `filepath.Clean`
    and resolving symlinks of its parent it must stay inside the root (else `target_path_outside_root`)
    and must not exist or be empty. Download + checksum every link first; extract each tar with the shared safe extractor (R11) into
    `<data-dir>/tmp/<op>/<seq>/`; run `pg_combinebackup <full> <incr1> … <incrN> -o <target_path>`;
    remove temps. Success message tells the user to start PostgreSQL <major> on that directory (owned
    by the server user, mode 0700).
  - Restoring the full alone uses the same flow with one directory (no combine: plain copy via
    `pg_combinebackup <full> -o` also works and keeps one code path).
  - Prerequisites: server ≥ 17; `summarize_wal = on`; role has `REPLICATION` (or superuser); client
    `pg_basebackup`, `pg_combinebackup` ≥ server major on PATH; single tablespace (tar-to-stdout
    rejects more — surfaced as the tool error). pg_hba replication access cannot be checked via SQL:
    the first full fails with the tool error if missing.
  - Plan scope: the plan has no database (clarification Q2). SQLite `plans.databases` has a
    `json_array_length >= 1` CHECK that cannot change without a table rebuild, so a cluster plan
    stores `["*"]`; backups store `database = "*"`. API/UI render `*` as "Entire server".
- **Verify during implementation**: manifest injection into `-D - -Ft` output on PG 17 (expected;
  if absent, fall back to `-D <tmpdir> -Ft` and stream `base.tar` + read `backup_manifest` from disk).
- **Rationale**: only native block-level incremental PostgreSQL offers; physical restore cannot go
  through a client connection, so directory output is the honest product (clarification Q1).
- **Alternatives**: WAL archiving/PITR — needs `archive_command` on the server host, out of reach of
  a remote web UI; temporary server + logical re-dump (option B) — rejected in clarification.

## R6. Retention (FR-013)

- **Decision**: `schedule.Keep` unchanged (counts individual backups, clarification Q3), followed by
  a closure: for each kept backup, walk `parent_id` and keep every ancestor. Dependents of a dropped
  backup are never kept (a kept dependent would have kept its ancestors), so no orphans arise.
  For incremental plans `Keep` runs per (plan, database) (chain correctness); non-incremental plans
  keep today's plan-wide grouping (no behavior change). Manual delete of a backup that has
  dependents deletes the dependents too, after a confirmation that lists their count.
- **Rationale**: one small pure function, easy to unit-test.
- **Alternatives**: chain-counting retention — rejected in clarification.

## R7. Scheduling

- **Decision**: `plans.schedule` stays the full schedule; new `plans.incr_schedule` (`''` = incremental
  mode off). Scheduler computes both; when both fire in the same tick, the full wins.
  `Scheduler.Run` gains a `kind` argument. "Run now" takes `?kind=full|incremental` (default full).
  Overlap still handled by the plan lock (skipped run recorded).
- **Rationale**: minimal change to the existing loop.

## R8. Restore/verify resolution

- **Decision**: Resolve chain = parent walk from the chosen backup to the full. Preflight returns the
  chain (`[{id, kind, seq, status, started_at}]`) and `chain_error` if any link is not `ok` or is
  missing; restore refuses (400 `chain_broken`, naming the link). Restore downloads and checksums
  **all** links before touching the target (Principle II). Verify on a link checksums every link of
  its chain, marks failing ones `corrupted`, and reports per-link results in the log and message.
- **Rationale**: matches existing "checksum before touching target" invariant.

## R9. Risk acknowledgment (FR-007, FR-008)

- **Decision**: Saving a SQL Server or MySQL plan with `incr_schedule != ''` requires
  `ack_incremental_risk: true` in the request when incremental mode is being turned on; stored as
  `plans.incr_ack_at`. Missing → 400 `ack_required`. UI shows the engine-specific warning (SQL
  Server: differential base of other tools; MySQL: brief global read lock at each full) with a
  required checkbox.

## R10. New dependencies

- **Decision**: none in Go (`archive/tar` stdlib). New runtime client tools: `mysqlbinlog`,
  `pg_basebackup`, `pg_combinebackup`, required only when a plan uses incremental mode for that
  engine; detected and reported like existing tools. Test compose: pg-a gets `summarize_wal=on`;
  MySQL images already have binlog on by default in 8.4.

## R11. Safe tar extraction

- **Decision**: one helper `untar(r io.Reader, dir string) error` in `internal/engine/tarx.go`, used
  for MySQL binlog and PostgreSQL links: rejects absolute names, `..` components, entries resolving
  outside `dir`, symlinks/hardlinks, and any type other than regular file or directory.
- **Rationale**: artifacts may be imported from a destination another party can write to; one tested
  extractor instead of two ad-hoc ones.

## R12. Engine interface for chains

- **Decision**:
  ```go
  type Link struct {
      Marker string // engine resume JSON
      Path   string // decoded payload file (restore only)
      State  string // local file of engine state from that link (pg: backup_manifest); "" otherwise
  }
  type Incremental interface {
      Prereqs(ctx context.Context, c Conn, db string) (missing, warnings []string, err error)
      BackupFull(ctx context.Context, c Conn, db string, w, state, log io.Writer) (marker string, warnings []string, err error)
      BackupIncr(ctx context.Context, c Conn, db string, prev Link, w, state, log io.Writer) (marker string, warnings []string, err error)
  }
  type ChainRestorer interface {
      RestoreChain(ctx context.Context, c Conn, db string, overwrite bool, links []Link, opts RestoreOpts, log io.Writer) error
  }
  ```
  `state` is nil for SQL Server and MySQL. Errors: `ErrChainBroken` (→ full), `ErrLockUnsafe`
  (→ warning, no backup).
- **Rationale**: optional interfaces keep standalone engines unchanged; `state` carries
  PostgreSQL's manifest without a side channel.
