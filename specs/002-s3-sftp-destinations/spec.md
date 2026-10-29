# Feature Specification: S3 and SFTP Storage Destinations

**Feature Branch**: `feature/002-s3-sftp-destinations`

**Created**: 2026-10-27

**Status**: Draft

**Input**: User description: "Aggiungere S3-compatible bucket e SFTP come destinazioni di backup (oltre al filesystem locale): salvataggio artefatti, lettura per restore/verify, retention/prune remoti, credenziali cifrate, configurazione da UI."

## Clarifications

### Session 2026-10-27

- Q: How is trust in the SFTP server host key established on first connection? → A: Visible trust-on-first-use: fingerprint shown during test, pinned on save.
- Q: Do restore/verify from remote destinations stage the artifact locally or stream it twice? → A: Download once to a local temporary copy, checksum it, restore from that copy, delete it afterwards.
- Q: Can a fresh install discover and import backups already present on a destination? → A: Yes — manual "Rescan" on any destination, plus automatic rescan when a destination is created.
- Q: How does the user restore an imported encrypted backup whose passphrase is unknown to this install? → A: "Unlock" action on the backup: passphrase validated by trial decryption, stored encrypted, optionally applied to all imported backups of the same origin.

## User Scenarios & Testing *(mandatory)*

### User Story 1 - Back up to an S3-compatible bucket (Priority: P1)

The user creates a storage destination of type "S3", entering endpoint, region, bucket, optional
key prefix, access key and secret key. They test it from the UI, then select it in a backup plan
(or for an on-demand backup). Backups land in the bucket and appear in history exactly like local
ones, with their remote location shown.

**Why this priority**: Off-site object storage is the most requested destination and removes the
single point of failure of keeping backups on the same host as the app.

**Independent Test**: Create an S3 destination pointing at a test bucket, run an on-demand backup,
confirm the artifact and its metadata exist in the bucket and the backup is listed as successful.

**Acceptance Scenarios**:

1. **Given** valid S3 settings, **When** the user clicks "Test", **Then** the system confirms it can
   write, read and delete a probe object, and reports the specific failure otherwise (auth denied,
   bucket missing, endpoint unreachable).
2. **Given** a plan using an S3 destination, **When** the backup runs, **Then** the artifact and its
   metadata are stored in the bucket under the configured prefix and the backup is marked
   successful only after the upload is complete and its checksum recorded.
3. **Given** the upload fails midway, **When** the run ends, **Then** the backup is marked failed and
   no partial object remains visible as a valid backup.

---

### User Story 2 - Back up to an SFTP server (Priority: P2)

The user creates a destination of type "SFTP" with host, port, username, password or private key,
and remote directory. On test, the system shows the server's host key fingerprint; on save the
fingerprint is pinned. Backups are uploaded to the remote directory.

**Why this priority**: Many self-hosters have a NAS or VPS reachable via SSH but no object storage.

**Independent Test**: Create an SFTP destination against a test server, run a backup, confirm the
artifact exists in the remote directory and the backup is successful.

**Acceptance Scenarios**:

1. **Given** valid SFTP settings, **When** the user tests the destination, **Then** the system shows
   the server host key fingerprint and confirms write/read/delete in the remote directory.
2. **Given** a saved SFTP destination, **When** the server presents a different host key, **Then**
   every operation on that destination fails with a clear host-key-mismatch error and nothing is
   uploaded or downloaded.
3. **Given** an upload interrupted midway, **When** the run ends, **Then** the backup is marked failed
   and any temporary remote file is removed (or left under a temporary name never listed as a
   backup).

---

### User Story 3 - Restore and verify from remote destinations (Priority: P1)

The user selects a backup stored on S3 or SFTP and restores it to a target database, or runs a
verify. The flow, confirmations and safety checks are identical to local backups.

**Why this priority**: A backup that cannot be restored is worthless (Principle I/II); without this,
stories 1–2 deliver no real value.

**Independent Test**: Restore a backup stored on each remote type into a fresh database and compare
contents; run verify on it and on a deliberately corrupted copy.

**Acceptance Scenarios**:

1. **Given** a backup on a remote destination, **When** the user restores it, **Then** the artifact is
   fetched, its checksum is verified before the target is touched, and the restore proceeds with
   the usual confirmation rules.
2. **Given** a remote artifact whose content no longer matches its checksum, **When** the user runs
   verify or restore, **Then** the system reports a checksum failure and does not touch the target.
3. **Given** a remote artifact that was deleted outside the app, **When** the user runs verify,
   **Then** the backup is reported as missing.

---

### User Story 4 - Retention and pruning on remote destinations (Priority: P2)

Plans using remote destinations apply their retention policy after each backup; expired backups
are deleted from the bucket or server and removed from history, just like local ones.

**Why this priority**: Without pruning, remote storage grows unbounded and costs money.

**Independent Test**: Configure "keep last 2" on a plan with an S3 (and an SFTP) destination, run 3
backups, confirm only the 2 newest remain remotely and in history.

**Acceptance Scenarios**:

1. **Given** a plan with retention "keep last N" on a remote destination, **When** a new backup
   succeeds, **Then** backups beyond the policy are deleted remotely (artifact and metadata).
2. **Given** a remote delete fails, **When** prune runs, **Then** the prune operation is marked with a
   warning naming the backups not deleted, and those backups stay in history.

---

### User Story 5 - Manage destinations from the UI (Priority: P3)

The user lists, creates, edits, tests and deletes destinations of any type from the UI. Secrets
are never shown back; editing keeps the stored secret unless a new one is entered.

**Why this priority**: Convenience on top of stories 1–2, which already need create/test.

**Independent Test**: Create one destination per type, edit a non-secret field, confirm the secret
still works; try deleting a destination used by a plan.

**Acceptance Scenarios**:

1. **Given** a destination with saved secrets, **When** the user opens it for edit, **Then** secret
   fields are empty with an indication that a value is stored.
2. **Given** a destination referenced by a plan or by existing backups, **When** the user tries to
   delete it, **Then** deletion is refused with the list of referencing plans/backup count.
3. **Given** a fresh install and a bucket/remote directory already holding Dumpling backups,
   **When** the user creates a destination pointing at it, **Then** the existing backups are
   imported into history automatically and are restorable.
4. **Given** a destination whose backups are already in history, **When** the user clicks
   "Rescan", **Then** only backups not yet known are imported (no duplicates) and a summary of
   imported/skipped items is shown.
5. **Given** an imported encrypted backup marked "locked", **When** the user unlocks it with the
   correct passphrase and chooses "apply to same origin", **Then** it and every other locked
   backup of that origin that the passphrase decrypts become restorable.

---

### Edge Cases

- Bucket/remote directory reachable at creation but credentials later revoked → backup fails with
  an auth error; previous backups remain listed; verify reports "unreachable", not "corrupt".
- Destination unreachable during a scheduled run → run fails, retries follow the existing
  scheduling behavior (no special retry loop).
- An object/file with the target name already exists remotely → never overwritten; the run fails
  (consistent with local behavior).
- Very large artifacts (tens of GiB) → upload must not require holding the artifact in memory.
- S3 provider that does not support virtual-hosted-style addressing (e.g. MinIO) → path-style
  option available.
- App restarted during an upload → startup recovery marks the operation interrupted and cleans or
  ignores leftover temporary remote objects/files.
- Editing a destination's bucket/host/path while it has backups → refused; existing backups must
  stay locatable. Credentials may be changed.
- Rescan finds an artifact without readable metadata, or whose metadata does not match the
  artifact → skipped and reported, never imported as valid.
- Rescan finds encrypted backups whose passphrase this install does not have → imported but
  flagged "locked"; checksum verify still works; restore requires either unlocking it first
  (FR-018) or supplying the passphrase in the restore request (used once, not stored).
- Wrong passphrase entered on unlock → rejected, backup stays locked, passphrase not stored.
- Imported backups reference a source connection or plan that does not exist on this install →
  imported without plan link; retention of existing plans never prunes imported backups.

## Requirements *(mandatory)*

### Functional Requirements

- **FR-001**: System MUST support three destination types: local/mounted filesystem (existing),
  S3-compatible object storage, and SFTP.
- **FR-002**: S3 destinations MUST be configurable with: name, endpoint URL (empty = AWS default),
  region, bucket, optional key prefix, access key ID, secret access key, and a path-style
  addressing toggle.
- **FR-003**: SFTP destinations MUST be configurable with: name, host, port (default 22), username,
  authentication by password or private key (optional passphrase), and remote directory.
- **FR-004**: System MUST pin the SFTP server host key fingerprint when the destination is saved
  (after being shown to the user during test) and MUST refuse any connection whose host key
  differs.
- **FR-005**: All destination secrets (S3 secret key, SFTP password, private key, passphrase) MUST
  be encrypted at rest, MUST NOT be returned by the API, and MUST NOT appear in logs, errors or
  process arguments.
- **FR-006**: Users MUST be able to test any destination from the UI; the test MUST check write,
  read and delete permissions and report a specific, secret-free failure reason.
- **FR-007**: Backups to remote destinations MUST be reported successful only after the artifact and
  its metadata are completely stored remotely and the checksum recorded; a failed upload MUST
  leave no object/file that is listed or restorable as a valid backup.
- **FR-008**: Remote uploads MUST stream the artifact without loading it fully into memory and MUST
  NOT overwrite an existing remote object/file.
- **FR-009**: Restore and verify MUST work for backups on any destination type, with checksum
  verification completed before the restore target is touched (existing safe-restore rules
  apply unchanged). For remote backups the artifact MUST be downloaded once to a local temporary
  copy, checksummed, and restored/verified from that copy; the copy MUST be deleted when the
  operation ends (success or failure), and leftovers MUST be cleaned by startup recovery.
- **FR-010**: Verify MUST distinguish "missing", "unreachable" and "checksum mismatch" outcomes for
  remote backups.
- **FR-011**: Retention pruning MUST delete expired backups (artifact and metadata) from remote
  destinations; per-backup delete failures MUST produce a warning and keep the backup in history.
- **FR-012**: Users MUST be able to list, create, edit, test and delete destinations of every type
  from the UI; secret fields MUST be write-only (blank keeps the stored value).
- **FR-013**: System MUST refuse deleting a destination referenced by plans or backups, and MUST
  refuse changing the type or location fields (bucket/prefix/endpoint, host/remote directory, local path)
  of a destination that holds backups.
- **FR-014**: Startup recovery MUST mark operations interrupted during a remote upload as
  interrupted and MUST NOT list leftover temporary remote data as backups.
- **FR-015**: Backup history MUST show the destination type and remote location for each backup.
- **FR-016**: Users MUST be able to "Rescan" any destination (all types) to import into history
  backups present there but unknown to this install, identified by their stored metadata; rescan
  MUST also run automatically when a destination is created. Rescan MUST be idempotent (no
  duplicates), MUST skip and report entries with missing or inconsistent metadata, and MUST NOT
  modify or delete anything on the destination.
- **FR-017**: Imported backups MUST be restorable and verifiable like native ones, MUST NOT be
  linked to any plan, and MUST be excluded from plan retention pruning.
- **FR-018**: Imported encrypted backups MUST be marked "locked" until unlocked. Users MUST be able
  to "Unlock" a locked backup by entering its passphrase; the system MUST validate it by trial
  decryption before accepting it, MUST store it encrypted at rest (same rules as FR-005), and
  MUST offer to apply it to all other locked backups of the same origin (same original plan as
  recorded in backup metadata), unlocking only those it actually decrypts. A restore request MAY
  carry the passphrase for a locked backup; it is used for that restore only and not stored.

### Key Entities

- **Storage Destination** (extended): name, type (local | s3 | sftp), type-specific location
  settings, encrypted secrets, SFTP pinned host key fingerprint. Local keeps existing path field.
- **Backup**: its storage location is now relative to a destination of any type; gains an
  "imported" origin marker, the original plan identifier from its metadata, and for imported
  encrypted backups a "locked" state plus an optional encrypted passphrase once unlocked.

## Success Criteria *(mandatory)*

### Measurable Outcomes

- **SC-001**: A user can configure, test and use a new S3 or SFTP destination in under 3 minutes.
- **SC-002**: 100% of backups stored on S3 and SFTP pass an end-to-end backup → restore round-trip
  test for each supported engine against real test services.
- **SC-003**: A 2 GiB artifact uploads to a remote destination with memory use of the app staying
  under 200 MiB above idle.
- **SC-004**: In fault-injection tests (upload killed midway), 0 partial remote artifacts are ever
  listed as successful backups.
- **SC-005**: No destination secret appears in any API response, log line or operation log in
  automated secret-leak tests.

## Assumptions

- "S3-compatible" means providers speaking the S3 API (AWS S3, MinIO, Backblaze B2, Wasabi,
  Cloudflare R2, etc.); provider-specific features (storage classes, object lock, lifecycle rules,
  server-side encryption settings) are out of scope. Artifacts are already encrypted by the app
  when the plan uses encryption.
- Authentication for S3 is static access key/secret only; IAM roles, instance profiles and STS are
  out of scope for v1.
- SFTP host key is pinned on first successful test/save (trust on first use with user visibility);
  no import of known_hosts files.
- Restore/verify from a remote destination downloads the artifact to a local temporary working
  area first (FR-009); the host needs enough free temporary space for one artifact.
- No moving/copying of existing backups between destinations, and no multi-destination (replicated)
  plans in this feature.
- Existing local destinations keep working unchanged; existing data is migrated to type "local".
- Free-space reporting is shown only for local destinations.
- Backup metadata written alongside each artifact will be extended to record the originating
  plan; for artifacts written before this feature, "same origin" falls back to same engine +
  source database name.
