# Feature Specification: Database Hot Backup & Restore Web UI

**Feature Branch**: `feature/001-db-backup-webui`

**Created**: 2026-10-27

**Status**: Draft

**Input**: User description: "Web UI app self-hosted (backend Go, frontend React) per gestire l'hot backup dei database, inizialmente PostgreSQL, MySQL e MS SQL Server. Ispirata a Backrest: configurazione via UI, piani di backup schedulati, retention, storico operazioni e log. Differenza chiave: i target sono database (non filesystem). L'utente registra connessioni a database sorgente, crea piani di backup (db, schedule, retention, destinazione di storage), consulta lo storico dei backup e può fare restore di un backup verso un database a scelta (stessa istanza o un'altra istanza dello stesso engine, eventualmente con nome db diverso), con conferma esplicita per restore distruttivi."

## Clarifications

### Session 2026-10-27

- Q: How is SQL Server backed up and where is the artifact written? → A: Native
  `BACKUP DATABASE` (.bak) to a shared directory reachable by both SQL Server and the app;
  path configured per connection.
- Q: Where is the encryption passphrase of a scheduled plan kept? → A: Stored encrypted at rest
  in the plan config; restore uses it automatically, user can type it for other backups.
- Q: What maximum single-database size must v1 handle well? → A: Up to ~100 GB, streamed,
  never fully held in memory.
- Q: How is the web UI exposed on the network? → A: Default bind `127.0.0.1`; bind address
  configurable; optional HTTPS with user cert/key; warning when non-local bind without TLS.
- Q: What does a confirmed restore onto an existing database do? → A: Terminate active
  connections, drop and recreate the database, then restore; result equals the backup.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - On-demand backup of a registered database (Priority: P1)

An operator logs into the web UI, registers a database connection (engine, host, port,
credentials), tests it, selects a database and starts a backup immediately. The backup runs
while the database stays online; the operator sees progress, then the result (success with
size/duration, or failure with a readable error) in the backup history.

**Why this priority**: Producing a valid backup of a live database is the core value; everything
else builds on it.

**Independent Test**: Register a PostgreSQL, a MySQL and a SQL Server connection, trigger a backup
for each while the database receives writes, and verify each appears in history as successful
with checksum and metadata.

**Acceptance Scenarios**:

1. **Given** a valid connection, **When** the operator clicks "Test connection", **Then** the UI
   reports success and lists the databases visible with those credentials.
2. **Given** invalid credentials or unreachable host, **When** testing the connection, **Then**
   the UI shows a clear error and the password is not echoed anywhere.
3. **Given** a registered connection, **When** the operator starts a backup of a database under
   active write load, **Then** the backup completes without blocking writes and is listed as
   successful with timestamp, size, duration, engine version and checksum.
4. **Given** the backup tool fails mid-way, **When** the run ends, **Then** the run is shown as
   failed with the error output and no partial artifact is presented as restorable.

---

### User Story 2 - Restore a backup to a chosen target (Priority: P1)

The operator picks a backup from history and restores it to a target of their choice: the
original database, a new database name on the same instance, or a database on another registered
instance of the same engine. If the restore would overwrite existing data, the operator must
explicitly confirm by typing the target database name.

**Why this priority**: A backup is only valuable if it can be restored; restore to an arbitrary
target is the key differentiator from filesystem-oriented tools.

**Independent Test**: Take a backup from instance A, restore it into a new database name on
instance B (same engine), and verify row counts and schema match the source at backup time.

**Acceptance Scenarios**:

1. **Given** a successful backup, **When** the operator restores it to a non-existent database
   name on a compatible instance, **Then** the database is created and populated, and the restore
   is recorded in history.
2. **Given** the target database already exists, **When** the operator starts the restore,
   **Then** the UI warns that active connections will be terminated and the database dropped and
   recreated, and proceeds only after the operator types the exact target database name.
3. **Given** a backup from engine X, **When** the operator selects a target of engine Y,
   **Then** the target is not offered (or is rejected) with an explanation.
4. **Given** the backup checksum no longer matches the stored artifact, **When** a restore is
   requested, **Then** the restore is refused and the backup is flagged as corrupted.
5. **Given** the target engine version is older than the source, **When** selecting it,
   **Then** the UI warns that the restore may fail before the operator confirms.

---

### User Story 3 - Scheduled backup plans with retention (Priority: P2)

The operator creates a backup plan: source connection, one or more databases, a schedule,
a retention policy and a storage destination. The system runs backups automatically and prunes
old backups according to retention.

**Why this priority**: Automation turns a manual tool into a backup system, but on-demand
backup/restore already delivers value without it.

**Independent Test**: Create a plan with a short schedule and "keep last 3" retention, let it run
5 times, verify exactly the 3 most recent successful backups remain.

**Acceptance Scenarios**:

1. **Given** a plan with a schedule, **When** the scheduled time arrives, **Then** a backup run
   starts automatically and is recorded in history linked to the plan.
2. **Given** retention "keep last N" and/or "keep daily/weekly/monthly for N periods", **When** a
   new backup succeeds, **Then** backups outside the policy are deleted and the deletion is logged.
3. **Given** a backup run fails, **When** retention is evaluated, **Then** no successful backup is
   deleted because of the failed run.
4. **Given** a plan, **When** the operator pauses it, **Then** no scheduled runs start until it is
   resumed; manual "run now" remains available.
5. **Given** a previous run of the same plan is still in progress, **When** the next schedule
   fires, **Then** the new run is skipped and the skip is logged.

---

### User Story 4 - Operation history, logs and backup verification (Priority: P3)

The operator browses a unified timeline of operations (backups, restores, retention prunes,
verifications) filterable by plan, connection and status, opens each operation's log, and can
verify any backup on demand.

**Why this priority**: Observability and trust; useful once backups and restores exist.

**Independent Test**: Run backups, a restore and a verification, then filter history by status
"failed" and by plan, and open the log of each.

**Acceptance Scenarios**:

1. **Given** past operations, **When** the operator filters by plan/connection/status/type,
   **Then** only matching operations are shown, newest first.
2. **Given** an operation, **When** the operator opens it, **Then** its full log is shown with
   no secrets in it.
3. **Given** a backup, **When** the operator clicks "Verify", **Then** the artifact checksum is
   re-computed and the result recorded.
4. **Given** an operation is running, **When** the operator views it, **Then** status and log
   update live without page reload.

---

### Edge Cases

- Storage destination full or unreachable during backup → run fails, partial artifact removed.
- Required engine client tool missing on the host → connection/plan shows a clear "tool missing"
  error before any run starts.
- Source database dropped or renamed after plan creation → run fails with a clear error; plan
  stays configured.
- Engine features that prevent a consistent hot backup (e.g. non-transactional MySQL tables)
  → backup runs but the run is flagged with a warning explaining consistency limits.
- Application restarted while an operation is running → on startup the operation is marked as
  interrupted/failed, never left "running" forever, and partial artifacts are cleaned up.
- Two restores targeting the same database concurrently → second one is refused.
- Restore into the same database the backup came from → treated as destructive, requires
  confirmation.
- Connection deleted while plans reference it → deletion refused until plans are removed or
  reassigned; existing backups remain restorable to other targets.

## Requirements *(mandatory)*

### Functional Requirements

**Connections**
- **FR-001**: Users MUST be able to create, edit, test and delete database connections for
  PostgreSQL, MySQL and Microsoft SQL Server (engine, host, port, username, password, optional
  TLS settings).
- **FR-001a**: SQL Server connections MUST also define a shared directory as seen by the SQL Server
  instance and as seen by the app (same directory, possibly different paths); connection test
  MUST verify that a file written by SQL Server there is readable by the app.
- **FR-002**: System MUST list the databases available on a connection after a successful test.
- **FR-003**: System MUST store connection credentials encrypted at rest and MUST NOT expose them
  in the UI after saving, in logs, or in error messages.
- **FR-004**: System MUST detect whether the client tools required for each engine are available
  and report missing ones.

**Backup**
- **FR-005**: Users MUST be able to start an on-demand backup of one or more databases of a
  connection.
- **FR-006**: Backups MUST be taken while the source database stays online, using a
  transactionally consistent method for each engine, without stopping the database.
- **FR-007**: System MUST record for each backup: source connection, engine, engine version,
  database name, start/end time, size, checksum, status, and storage location.
- **FR-008**: System MUST mark a backup successful only after the backup process completed
  without error and the stored artifact's checksum was recorded; otherwise it MUST be marked
  failed and any partial artifact removed.
- **FR-009**: Backups MUST be stored in a storage destination of type local or mounted
  filesystem directory (e.g. local disk, NFS/SMB mount); other destination types are out of
  scope for v1.
- **FR-010**: Each backup MUST cover exactly one database; a run over multiple selected databases
  produces one independent backup per database. Whole-instance backups (all databases, server
  logins/roles) are out of scope for v1.
- **FR-011**: Backup artifacts MUST always be compressed; a plan (or on-demand backup) MAY enable
  encryption with a user-provided passphrase. Encrypted backups MUST be restorable only with that
  passphrase, and the UI MUST warn that losing it makes the backup unrecoverable.
- **FR-011a**: A plan's passphrase MUST be stored encrypted at rest (same protection as
  credentials, FR-003) so scheduled runs can encrypt unattended; restoring a backup of that plan
  MUST use the stored passphrase automatically, otherwise the UI MUST prompt for it.

**Plans, scheduling, retention**
- **FR-012**: Users MUST be able to create, edit, pause, resume, delete and "run now" backup
  plans, each with: source connection, databases, schedule, retention policy, storage destination.
- **FR-013**: Schedules MUST support cron-style expressions and simple presets (hourly, daily at
  time, weekly).
- **FR-014**: Retention MUST support "keep last N" and "keep N daily / N weekly / N monthly";
  retention MUST consider only successful backups and MUST never delete the most recent
  successful backup of a plan.
- **FR-015**: System MUST NOT run two backups of the same plan concurrently; overlapping runs
  are skipped and logged.

**Restore**
- **FR-016**: Users MUST be able to restore any successful backup to a target chosen among
  registered connections of the same engine, with an optional different target database name.
- **FR-017**: System MUST prevent restoring a backup into a connection of a different engine.
- **FR-018**: When the target database exists, System MUST require explicit confirmation by
  typing the target database name before overwriting. A confirmed overwrite MUST terminate
  active connections to the target, drop and recreate the database, then restore, so the result
  matches the backup exactly; the confirmation dialog MUST state this.
- **FR-019**: System MUST verify the backup checksum before restoring and refuse on mismatch.
- **FR-020**: System MUST warn when the target engine version is older than the backup's source
  version.
- **FR-021**: System MUST refuse a restore into a database that already has a restore in progress.

**History, logs, verification**
- **FR-022**: System MUST keep a history of all operations (backup, restore, prune, verify) with
  status, timestamps, related plan/connection and log output.
- **FR-023**: Users MUST be able to filter history by plan, connection, operation type and status.
- **FR-024**: Users MUST be able to view live status and log of running operations.
- **FR-025**: Users MUST be able to verify a backup on demand (checksum re-check).
- **FR-026**: On startup, System MUST mark operations left running by a previous process as
  interrupted and clean up their partial artifacts.

**Access & deployment**
- **FR-027**: The web UI MUST require authentication; first launch MUST prompt creation of the
  admin account.
- **FR-027a**: By default the UI MUST listen only on the local loopback address; the bind address
  MUST be configurable; HTTPS MUST be supported with a user-provided certificate and key; the
  system MUST log a prominent warning when bound to a non-loopback address without HTTPS.
- **FR-028**: The application MUST be deployable as a single self-contained executable that
  serves both the UI and the backend.

### Key Entities

- **Connection**: a reachable database server instance — engine, host, port, username, encrypted
  password, TLS options, detected engine version.
- **Storage Destination**: a filesystem directory where backup artifacts are kept — path,
  free-space info.
- **Backup Plan**: connection + selected databases + schedule + retention policy + storage
  destination + paused flag.
- **Backup**: one artifact for one database — source connection, engine, engine version,
  database name, timestamps, size, checksum, encrypted flag, storage location, status,
  originating plan (optional for on-demand).
- **Operation**: an entry in history — type (backup, restore, prune, verify), status (pending,
  running, success, warning, failed, interrupted, skipped), timestamps, related entities, log.
- **Restore Request**: backup + target connection + target database name + confirmation record.
- **User**: authenticated operator of the UI.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A new user can register a connection and complete a first successful backup in
  under 5 minutes from first launch.
- **SC-002**: A user can restore a backup into a new database on another instance in under
  2 minutes of interaction (excluding data transfer time).
- **SC-003**: 100% of backups marked successful pass on-demand verification and restore cleanly
  in the per-engine round-trip test (restored schema and row counts match the source at backup
  time).
- **SC-004**: During a backup, writes to the source database continue with no observable downtime
  (zero failed writes attributable to the backup in a write-load test).
- **SC-005**: Zero occurrences of plaintext credentials in logs, history, UI after save, or
  persisted configuration (verified by searching all of them for a known test password).
- **SC-006**: Scheduled runs start within 1 minute of their scheduled time.
- **SC-007**: No destructive restore can complete without the explicit typed confirmation.
- **SC-008**: Backup and restore of a database up to 100 GB complete successfully with the app's
  memory usage staying bounded (not proportional to database size) and no temporary disk space
  needed beyond the final artifact (SQL Server: beyond the native .bak in the shared directory).

## Assumptions

- Single-admin model for v1 (like Backrest): one or few operators with full access; role-based
  permissions are out of scope.
- Backups are logical/native per engine (one artifact per database); point-in-time recovery,
  incremental backups and WAL/binlog streaming are out of scope for v1.
- Restore is limited to same-engine targets; cross-engine migration is out of scope.
- The host running the app has network access to the databases and the engine client tools
  installed (or they ship alongside, decided at planning).
- For SQL Server, native backup files are written by the database server itself into the
  connection's shared directory (FR-001a); the app then moves/compresses them into the storage
  destination. Same mechanism in reverse for restore.
- Notifications (email, webhook) are out of scope for v1.
- UI language: English for v1.
