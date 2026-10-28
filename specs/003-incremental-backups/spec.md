# Feature Specification: Incremental Backups

**Feature Branch**: `feature/003-incremental-backups`

**Created**: 2026-10-28

**Status**: Draft

**Input**: User description: "Incremental backups for all three engines. Plans can schedule incremental backups chained to a full base backup, in addition to existing full logical dumps. SQL Server: native differential (and optionally log) backups against the latest full native backup. PostgreSQL 17+: physical incremental base backups using the previous backup manifest (whole cluster, requires replication privilege and WAL summarization). MySQL: binary log capture since the position recorded at the last full dump. Each incremental goes through the existing compress→encrypt→checksum artifact pipeline and storage backends (local/S3/SFTP), with metadata that records its parent/chain. Restore of an incremental resolves and applies the whole chain (full + incrementals in order) to the target; verify checks every link. Retention must never prune a full backup or link that a kept incremental depends on. UI shows chains, backup type (full/incremental), and blocks incremental restore when a link is missing. Engines/servers that don't meet prerequisites are reported clearly and plans fall back or refuse rather than silently producing broken chains."

## Clarifications

### Session 2026-10-28

- Q: What does restore produce for a PostgreSQL physical chain? → A: A reconstructed cluster data
  directory at a user-chosen path on the Dumpling host; the user starts a server on it.
- Q: What do retention rules count — individual backups or whole chains? → A: Individual backups
  (fulls and incrementals alike); each kept backup also keeps its base and intermediate links.
- Q: What does the database selection mean on a PostgreSQL plan in incremental mode? → A: Nothing —
  the plan becomes server-wide; database field hidden, shown as "entire server" / "cluster".
- Q: How is the SQL Server non-copy-only full backup impact on other tools handled? → A: Warning plus
  explicit acknowledgment required before the plan can be saved with incremental mode.
- Q: What does a scheduled incremental run do when a prerequisite stops being met after enabling? →
  A: Take a full backup, flag the run with a visible "prerequisite missing" warning (history + plan
  view), and repeat that on every run until fixed.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Scheduled SQL Server differential backups (Priority: P1)

The user edits a SQL Server backup plan and enables incremental mode. They set a schedule for full
backups (e.g. weekly) and a separate, more frequent schedule for incremental backups (e.g. hourly).
Each incremental run captures only the changes since the most recent full backup of that chain and
is stored on the plan's destination like any other backup. History shows each backup's type and the
chain it belongs to.

**Why this priority**: SQL Server supports differential backups natively and Dumpling already uses
native backup/restore for it, so this delivers incremental value with the least new machinery and
proves the chain model (metadata, retention, restore of a chain) for the other engines.

**Independent Test**: Enable incremental mode on a SQL Server plan, run a full and then two
incrementals with data changes in between, confirm each incremental is smaller than the full and is
linked to it in history.

**Acceptance Scenarios**:

1. **Given** a SQL Server plan, **When** the user enables incremental mode, **Then** a warning about
   the effect on other backup tools is shown and the plan cannot be saved until the user explicitly
   acknowledges it.
2. **Given** a SQL Server plan with incremental mode enabled and no full backup yet, **When** the
   incremental schedule fires, **Then** the system takes a full backup instead and records why.
3. **Given** a successful full backup in the chain, **When** an incremental runs, **Then** the
   artifact contains only the changes since that full backup, records its parent, and is marked
   successful only after it is fully written and checksummed.
4. **Given** the server's backup history shows a newer full backup taken outside Dumpling, **When**
   an incremental would run, **Then** the system detects that the chain base changed and takes a
   new full backup instead of producing an incremental that cannot be restored from Dumpling's
   artifacts.

---

### User Story 2 - Restore and verify a chain (Priority: P1)

The user picks any incremental backup in history and chooses "Restore". The system shows the full
chain needed (base full + each incremental up to the chosen one), confirms every link is present
and intact, and then restores them in order to the target the user selects. "Verify" on an
incremental checks every link of its chain.

**Why this priority**: Incremental backups are worthless unless the chain restores reliably; this
is the integrity guarantee of the feature.

**Independent Test**: Restore the second incremental of a chain to a new database and confirm its
contents match the source as of that incremental's time.

**Acceptance Scenarios**:

1. **Given** a complete chain, **When** the user restores incremental N, **Then** the system applies,
   in order, every link incremental N depends on and incremental N itself, and the target matches
   the source at the time of incremental N.
2. **Given** one link of the chain is missing or fails its checksum, **When** the user opens restore
   for any backup that depends on it, **Then** restore is blocked, the missing/broken link is named,
   and nothing touches the target.
3. **Given** an incremental, **When** the user runs verify, **Then** every link is checked and the
   result reports each link's status.
4. **Given** a restore that overwrites an existing target, **When** the user confirms, **Then** the
   same explicit, target-naming confirmation as for full restores is required.

---

### User Story 3 - Retention that keeps chains restorable (Priority: P1)

Retention rules (keep last N, daily, weekly, ...) apply to plans with incremental mode. When
retention decides to keep a backup, everything that backup needs to be restored is also kept. When
a full backup is pruned, all incrementals that depend on it are pruned with it.

**Why this priority**: Without this, routine pruning silently destroys restorability, violating the
data integrity principle.

**Independent Test**: Create a plan with "keep last 3", run full + 5 incrementals, prune, and confirm
the kept incrementals are restorable and their full base still exists.

**Acceptance Scenarios**:

1. **Given** retention keeps an incremental, **When** prune runs, **Then** its full base and all
   intermediate links are kept even if retention alone would remove them.
2. **Given** a full backup no kept backup depends on, **When** prune removes it, **Then** all of its
   dependent incrementals are removed too, so no orphaned incrementals remain.

---

### User Story 4 - MySQL binary log incrementals (Priority: P2)

The user enables incremental mode on a MySQL plan. Full backups record the server's binary log
position at the time of the dump. Each incremental captures the binary log events since the
previous link's end position. Restoring an incremental replays the full dump and then the captured
events, filtered to the plan's database, onto the target.

**Why this priority**: Common self-hosted engine, but depends on server-side configuration (binary
logging enabled, retention of logs, privileges) that many installs lack.

**Independent Test**: With binary logging enabled on a test server, run full + incremental with data
changes between, restore the incremental to a different database, and compare contents.

**Acceptance Scenarios**:

1. **Given** the server has binary logging disabled or the account lacks the required privilege,
   **When** the user enables incremental mode or tests the connection, **Then** the system reports
   the specific missing prerequisite and refuses to enable incremental mode.
2. **Given** the binary logs needed since the previous link were already purged by the server,
   **When** an incremental runs, **Then** the system detects the gap and takes a new full backup
   instead, recording why.
3. **Given** a restore to a database with a different name than the source, **When** events are
   replayed, **Then** they are applied to the chosen target database, not the source name.
4. **Given** a MySQL plan, **When** the user enables incremental mode, **Then** a warning that each
   full backup briefly holds a global read lock at its start is shown and the plan cannot be saved
   until the user explicitly acknowledges it.
5. **Given** write statements running for more than 10 seconds, or a global read lock that cannot
   be acquired within 5 seconds, **When** a full backup in incremental mode starts, **Then** the dump
   is stopped before writes stay blocked longer than 5 seconds, no backup is produced by that run,
   the run is reported as a warning naming the cause, and the next scheduled run tries again.

---

### User Story 5 - PostgreSQL physical incrementals (Priority: P3)

The user enables incremental mode on a PostgreSQL plan against a server version 17 or later with WAL
summarization enabled and an account with replication privilege. Full and incremental backups in
this mode are physical backups of the whole server cluster (all databases), not a logical dump of
one database. Restoring a chain reconstructs a complete cluster data directory in a folder the user
chooses inside Dumpling's restore folder on the Dumpling host; the user then starts a PostgreSQL
server (same major version) on that directory. Dumpling does not load physical backups into an existing server.

**Why this priority**: Highest prerequisites and the biggest departure from Dumpling's
"restore one database to any target" model.

**Independent Test**: Against a PostgreSQL 17 server with WAL summarization on, run full +
incremental, restore the chain per the chosen restore model, and confirm data matches.

**Acceptance Scenarios**:

1. **Given** a server older than 17, WAL summarization off, or an account without replication
   privilege, **When** the user enables incremental mode, **Then** the system names the missing
   prerequisite and refuses.
2. **Given** a plan in PostgreSQL incremental mode, **When** viewing or editing the plan, **Then** the
   database selection is hidden and replaced by an "entire server" label, history shows its backups
   as "cluster", and the UI explains how restore works for them.
3. **Given** a complete chain, **When** the user restores incremental N to a chosen path, **Then**
   the system writes a reconstructed data directory there that a PostgreSQL server of the same
   major version starts on and whose contents match the source at the time of incremental N.
4. **Given** the chosen path exists and is not empty, **When** the user starts the restore, **Then**
   the system refuses rather than overwriting it.

---

### Edge Cases

- Incremental schedule fires while a full backup of the same plan is still running → incremental
  is skipped and recorded (existing per-plan lock), never bases itself on an unfinished full.
- Full backup fails → the previous chain stays current; next incremental continues the previous
  chain if still valid, otherwise a full is taken.
- Plan's database or connection changes, or the connection's host/port now reaches a different
  server → the current chain is closed; the next run is a full backup. A password-only change keeps
  the chain.
- Plan's destination changes while backups exist (already locked) → chains stay on their original
  destination.
- Encryption passphrase changes mid-chain → next run starts a new chain with a full backup; every
  link of a chain uses the same passphrase.
- Incremental mode disabled on a plan → existing chains remain restorable and prunable; new runs are
  full backups.
- Backups imported via rescan on a fresh install → chains are reconstructed from the per-artifact
  metadata; incomplete imported chains show the missing link and block restore.
- Prerequisite lost after enabling (binary logging turned off, replication privilege revoked, WAL
  summarization disabled) → every incremental run becomes a full backup flagged "prerequisite
  missing" until fixed; incremental mode is not silently disabled.
- An incremental run produces no changes → still recorded as a valid (small) link.
- Restore chosen at a full backup in incremental mode → behaves as a plain full restore.
- Restore of a chain fails midway → a target created by the restore is removed; an overwritten
  target is reported as partially restored, never as success.
- Chain grows very long because no full backup is ever scheduled → the system forces a full after a
  maximum chain length.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: Users MUST be able to enable incremental mode per plan, with separate schedules for
  full and incremental backups.
- **FR-002**: Every backup MUST record its type (full or incremental), the chain it belongs to, and
  for incrementals its parent link, both in the app's records and in the metadata stored next to the
  artifact on the destination.
- **FR-003**: Incremental artifacts MUST go through the same compression, optional encryption and
  checksum pipeline and the same destinations (local, S3, SFTP) as full backups.
- **FR-004**: An incremental MUST only be reported successful when its parent chain is valid and the
  artifact was fully written and checksummed.
- **FR-005**: When an incremental cannot be produced validly (no full yet, base changed outside
  Dumpling, log gap, configuration change, chain-length limit reached (FR-017)), the system MUST
  take a full backup instead and record the reason in the operation log.
- **FR-006**: The system MUST check engine/server prerequisites when incremental mode is enabled and
  at each run, and report the specific missing prerequisite; it MUST refuse to enable incremental
  mode when prerequisites are not met. If a prerequisite stops being met after enabling, each
  incremental run MUST take a full backup instead and carry a "prerequisite missing" warning, naming
  the prerequisite, that is visible in history and on the plan until the prerequisite is restored.
- **FR-007**: SQL Server incrementals MUST be native differential backups based on a full backup
  taken by Dumpling for that chain. Because those fulls become the differential base for every
  tool on that database, enabling incremental mode on a SQL Server plan MUST show a warning and
  require an explicit acknowledgment before the plan is saved.
- **FR-008**: MySQL incrementals MUST capture binary log events since the end position of the
  previous link; full backups in incremental mode MUST record the starting position. Because
  recording that position briefly holds a global read lock at the start of each full, enabling
  incremental mode on a MySQL plan MUST show a warning and require explicit acknowledgment (as
  FR-007). A full MUST NOT start while write statements have been running for more than 10 seconds,
  and MUST be stopped if the global read lock is not acquired within 5 seconds; in both cases the
  run is reported as a warning and produces no backup.
- **FR-009**: PostgreSQL incrementals MUST be physical incremental backups requiring server version
  17+, WAL summarization, and replication privilege. A PostgreSQL plan in incremental mode is
  server-wide: it has no database selection, and its backups are labeled "cluster". Restoring a
  PostgreSQL chain MUST write a reconstructed cluster data directory to a user-chosen folder inside
  the configured restore folder on the Dumpling host, and MUST refuse a folder that is not empty or
  that resolves outside the restore folder.
- **FR-010**: Restoring a backup MUST resolve every link it depends on (its ancestors), verify
  presence and checksum of each of them and of the backup itself before touching the target, and
  apply them in order.
- **FR-011**: Restore MUST be blocked when any required link is missing or fails verification, naming
  the link.
- **FR-012**: Verify on an incremental MUST check every link in its chain and report per-link status.
- **FR-013**: Retention rules MUST count individual backups (full and incremental alike, unchanged
  rule semantics). Retention MUST then keep every link required to restore any kept backup, and
  MUST remove dependent incrementals together with their pruned base, leaving no orphaned
  incrementals. Manual deletion of a backup that others depend on MUST require confirmation and
  delete its dependents too.
- **FR-014**: The UI MUST show each backup's type, its chain membership (base and position), and
  link health, and MUST show the chain to be applied before a restore is confirmed.
- **FR-015**: Restore of a chain MUST follow existing safe-restore rules: explicit target choice,
  target-naming confirmation for destructive restores, rejection across engines. For PostgreSQL
  physical chains the "target" is the output directory path, not a database connection.
- **FR-016**: Rescan/import MUST reconstruct chains from destination metadata.
- **FR-017**: The system MUST force a full backup after a maximum number of consecutive incrementals
  in a chain (configurable per plan, sensible default).
- **FR-018**: Credentials used for incremental capture MUST follow existing secret handling: never on
  process arguments, in logs, or in API responses.

### Key Entities

- **Backup** (extended): gains type (full/incremental), chain identifier, parent backup, position in
  chain, and engine-specific resume marker (e.g. log position or manifest reference) needed to
  produce the next link.
- **Chain**: one full backup plus its ordered incrementals for a plan; restorable up to any link
  when all earlier links are present and intact.
- **Plan** (extended): incremental mode flag, full schedule, incremental schedule, maximum chain
  length. In PostgreSQL incremental mode the plan has no database (scope = entire server).

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: For a database with ≤5% changed data between runs, an incremental backup is at most
  20% of the size of a full backup and completes in at most half its time (size: verified by
  automated tests; time: verified manually via quickstart).
- **SC-002**: 100% of round-trip tests restoring the last link of a full + 3-incremental chain
  produce a target identical to the source at that point, for each supported engine.
- **SC-003**: After any retention prune, 100% of kept backups remain restorable (no kept backup has a
  missing link).
- **SC-004**: Every incremental run that cannot be produced validly results in a successful full
  backup or a clearly reported failure — zero runs reported successful with a broken chain.
- **SC-005**: A user can identify, from the history view alone, which backups a restore of a given
  incremental will apply, without opening logs.

## Assumptions

- Incremental mode is opt-in per plan; existing plans keep producing full logical dumps unchanged.
- Point-in-time recovery to an arbitrary timestamp between links is out of scope; restore targets
  the end state of a chosen link. SQL Server transaction log backups are out of scope (differentials
  only).
- SQL Server full backups in incremental mode are taken without the copy-only option (required for
  differentials to be based on them); this affects differential bases of other backup tools on the
  same database, so enabling incremental mode requires an explicit acknowledgment (FR-007).
- MySQL incremental replay is filtered to the plan's database; cross-database statements in the
  binary log may be skipped or misapplied in statement-based logging — documented as a limitation.
- MySQL full backups in incremental mode remain logical dumps, extended to record the log position;
  recording it takes a brief global read lock at dump start, permitted by constitution Principle III
  (v1.1.0) only with the user's acknowledgment and abort-instead-of-stall behavior (FR-008).
- The restore folder defaults to a `restores` folder inside Dumpling's data directory and is
  configurable at startup; PostgreSQL restores can only be written inside it.
- Retention grouping per database applies to incremental plans only; non-incremental plans keep
  today's plan-wide retention.
- Default maximum chain length: 30 incrementals.
- All links of a chain live on the same destination and use the same encryption passphrase.
- Required client tools for the new modes (binary log reader, physical base backup and combine
  tools) are runtime requirements detected and reported like existing tools.
