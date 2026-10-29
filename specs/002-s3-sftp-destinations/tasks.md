---

description: "Task list for S3 and SFTP storage destinations"
---

# Tasks: S3 and SFTP Storage Destinations

**Input**: Design documents from `specs/002-s3-sftp-destinations/`

**Prerequisites**: plan.md, spec.md, research.md, data-model.md, contracts/api.md, quickstart.md

**Tests**: Included — Constitution VI requires tests for non-trivial logic (storage commit, migration,
rescan, unlock, verify classification) and a real round-trip per destination type.

**Organization**: Grouped by user story. Story order follows priority: US1 (P1), US3 (P1), US2 (P2),
US4 (P2), US5 (P3). US3 is placed right after US1 because a backup without restore has no value.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies on incomplete tasks)
- **[Story]**: US1…US5 from spec.md

---

## Phase 1: Setup (Shared Infrastructure)

- [X] T001 Add dependencies `github.com/minio/minio-go/v7` and `github.com/pkg/sftp` (latest stable) via `go get`, run `go mod tidy`; update `go.mod`/`go.sum`
- [X] T002 [P] Add test services to `testdata/compose.yaml`: `minio` (`minio/minio`, `MINIO_ROOT_USER=minioadmin`, `MINIO_ROOT_PASSWORD=minioadmin`, ports 9000/9001, healthcheck), `minio-init` (`minio/mc` creating bucket `dumpling`, depends on minio healthy), `sftp` (`atmoz/sftp`, user `dumpling:Dumpling-Test-1:::upload`, port 2222→22); give the `it` service env `DUMPLING_TEST_S3_ENDPOINT=minio:9000`, `DUMPLING_TEST_S3_BUCKET=dumpling`, `DUMPLING_TEST_S3_KEY=minioadmin`, `DUMPLING_TEST_S3_SECRET=minioadmin`, `DUMPLING_TEST_SFTP_ADDR=sftp:22`, `DUMPLING_TEST_SFTP_USER=dumpling`, `DUMPLING_TEST_SFTP_PASSWORD=Dumpling-Test-1`, `DUMPLING_TEST_SFTP_DIR=/upload` and `depends_on` minio-init (completed) + sftp

---

## Phase 2: Foundational (Blocking Prerequisites)

**Purpose**: storage abstraction with the local backend, schema migration and destination model — no behavior change for existing local destinations.

**⚠️ CRITICAL**: No user story work can begin until this phase is complete

- [X] T003 Create `internal/store/migrate.go`: read `PRAGMA user_version`, apply ordered `[]string` of statements in one transaction each, bump `user_version`; call from `Open` in `internal/store/store.go` after `schema` exec. Migration 1: `ALTER TABLE destinations ADD COLUMN type TEXT NOT NULL DEFAULT 'local'`, `ADD COLUMN config TEXT NOT NULL DEFAULT '{}'`, `ADD COLUMN secret TEXT NOT NULL DEFAULT ''`; `ALTER TABLE backups ADD COLUMN imported INTEGER NOT NULL DEFAULT 0`, `ADD COLUMN origin_plan_id TEXT NOT NULL DEFAULT ''`, `ADD COLUMN passphrase TEXT NOT NULL DEFAULT ''`
- [X] T004 [P] Test in `internal/store/migrate_test.go`: open a DB created with the old schema (no new columns, one destination + one backup row), reopen with migration → columns exist, existing destination has `type='local'`, reopening again is a no-op
- [X] T005 Extend `Destination` in `internal/store/entities.go`: fields `Type` (`local`|`s3`|`sftp`), `S3 *S3Config`, `SFTP *SFTPConfig` (JSON in `config`), S3 `endpoint` normalized from URL (`https://host:port` → host + `use_tls`) or accepted as host[:port], secrets struct sealed with `s.Box` into `secret` (`secret_access_key`; `password`, `private_key`, `key_passphrase`), `HasSecret map[string]bool` and `BackupCount int` for API output; secret values never JSON-serialized (`json:"-"`). Update `Destinations`, `Destination`, `SaveDestination` (blank secret field on update keeps stored value). Validation per data-model.md: "name 1–64 chars, unique"; s3 requires `bucket`, `region`, `access_key_id`, `secret_access_key`; sftp requires `host`, `username`, `remote_dir` (absolute), one of password/private_key, `host_key_fingerprint`; port default 22
- [X] T006 In `SaveDestination` (`internal/store/entities.go`) return new `ErrLocationLocked` when the destination has non-deleted backups and any location field changes: `type`, local `path`, s3 `endpoint`/`bucket`/`prefix`, sftp `host`/`port`/`remote_dir`
- [X] T007 Extend `Backup` in `internal/store/entities.go` with `Imported bool`, `OriginPlanID string`, sealed `Passphrase` (`json:"-"`) plus derived `Locked` (`imported && encrypted && passphrase == ""`), `DestinationType`, `Location` for API; add `SetBackupPassphrase(id, plain)`, `BackupByPath(destID, path)` (status ≠ deleted), `OperationExists(id)`; include new columns in `InsertBackup`/scan
- [X] T008 [P] Tests in `internal/store/entities_test.go`: secrets sealed in DB row and absent from `json.Marshal(Destination)`; blank secret on update keeps old; `ErrLocationLocked` when bucket changes with a backup present, allowed for credential change
- [X] T009 Create `internal/storage/storage.go`: `Backend` interface `Create(rel) (Upload, error)`, `Open(rel) (io.ReadCloser, error)`, `Stat(rel) (int64, error)`, `Remove(rel) error`, `List(suffix string) ([]string, error)` (rel paths), `Cleanup() error` (partials / incomplete uploads), `Location(rel) string`, `Close() error`; `Upload` = `io.Writer` + `Commit() error` + `Abort()`; `Open(d store.Destination) (Backend, error)` factory; doc that "not found" errors wrap `fs.ErrNotExist`; helper `PutBytes(b Backend, rel, data)` for sidecars
- [X] T010 Create `internal/storage/local.go`: move current behavior from `internal/artifact/artifact.go` — `MkdirAll`, refuse existing final, write `<final>.partial` (`O_EXCL`, 0640), `Commit` = fsync + close + `os.Link` (never overwrite) + remove partial; `Cleanup` walks root removing `*.partial`; `List` walks for suffix
- [X] T011 Refactor `internal/artifact/artifact.go`: `Create(up storage.Upload, gz, passphrase)` writes layers into `up` (checksum + count unchanged); `Commit(b storage.Backend, rel, meta)` closes layers, `up.Commit()`, then writes sidecar `<rel>.json` via `storage.PutBytes`; `Abort` calls `up.Abort()`; `Open(rc io.ReadCloser, gz, passphrase)` and `Checksum(r io.Reader)` take streams; keep `ErrPassphrase`. Add `CheckPassphrase(r io.Reader, passphrase) error` (age.Decrypt header only)
- [X] T012 Update `internal/artifact/artifact_test.go` to the new signatures using the local backend (keep the 2 GiB streaming test, `-short` skips it); add test for `CheckPassphrase` right/wrong
- [X] T013 Create `internal/storage/storage_test.go`: contract test run for local (always), S3 when `DUMPLING_TEST_S3_ENDPOINT` set, SFTP when `DUMPLING_TEST_SFTP_ADDR` set — create+commit then Open/Stat equal; second Create of same rel fails (no overwrite); Abort leaves nothing in `List`; Remove then Open → `errors.Is(err, fs.ErrNotExist)`; `Cleanup` removes leftover partials; "crash" case (SC-004): `Create`, write 32 MiB, `Close()` the backend without `Commit`/`Abort`, open a new backend → `List(".json")` and `Stat(rel)` do not find the artifact; after `Cleanup()` no `.partial` files (local, SFTP) and no incomplete multipart uploads (S3, checked via `ListIncompleteUploads`) remain
- [X] T014 Refactor `internal/runner/backup.go` to open the destination backend (`storage.Open`), `artifact.Create(b.Create(rel), …)`, commit through backend; sidecar meta adds `"plan_id"`; `l.Printf` uses `b.Location(rel)`; close backend at the end; after `storage.Open` call `l.AddSecret` for every destination secret (`secret_access_key`, `password`, `private_key`, `key_passphrase`)
- [X] T015 Add `fetch(o, dest, rel) (path string, cleanup func(), err)` in `internal/runner/runner.go`: local → absolute path, no copy; remote → stream to `<data-dir>/tmp/<op-id>.artifact` and return remover. Refactor `internal/runner/restore.go` and `StartVerify` in `internal/runner/maintain.go` to use it; `deleteFiles` uses `backend.Remove` for artifact and `.json`; every backend opened for restore/verify/delete calls `l.AddSecret` for all destination secrets
- [X] T016 Verify classification in `internal/runner/maintain.go` (research R10): `fs.ErrNotExist` → backup `corrupted`, message "artifact missing"; other error → op failed "destination unreachable", status unchanged; mismatch → `corrupted` "checksum mismatch"
- [X] T017 Update `Recover()` in `internal/runner/runner.go`: remove `<data-dir>/tmp/*.artifact` synchronously; remote cleanup runs in a background goroutine after startup (startup never blocks on remotes): for each destination `storage.Open(d)` + `Cleanup()` with a 30 s context timeout, log and continue on error
- [X] T018 [P] Add tests in `internal/runner/runner_test.go`: verify outcomes missing / mismatch / ok against local backend; fetch temp file removed after restore failure; `go test -short ./...` green (no regressions)

**Checkpoint**: local destinations behave exactly as before through the new abstraction.

---

## Phase 3: User Story 1 - Back up to an S3-compatible bucket (Priority: P1) 🎯 MVP

**Goal**: create/test an S3 destination and run backups into it.

**Independent Test**: create S3 destination on MinIO, on-demand backup → object + `.json` in bucket under prefix, backup `ok`.

- [X] T019 [US1] Create `internal/storage/s3.go`: `minio.New(endpoint or "s3.amazonaws.com", Creds: credentials.NewStaticV4, Secure: use_tls, Region, BucketLookup: BucketLookupPath if path_style else Auto)`; key = `prefix + "/" + rel` (no leading slash); `Create` → `StatObject` must return NoSuchKey else error "already exists" (`ponytail:` stat-then-put race, upgrade `If-None-Match: *`), then `io.Pipe` + goroutine `PutObject(ctx, bucket, key, pr, -1, PutObjectOptions{PartSize: 64 << 20})`; `Commit` closes writer and waits for result; `Abort` closes pipe with error (minio aborts multipart); `Open` = `GetObject` + `Stat` to surface NoSuchKey as `fs.ErrNotExist`; `List` = `ListObjects(prefix, Recursive)` filtered by suffix; `Cleanup` = `ListIncompleteUploads` under prefix + `RemoveIncompleteUpload`; `Location` = `s3://bucket/prefix/rel`
- [X] T019a [P] [US1] Memory test in `internal/storage/storage_test.go` (S3 only, skipped with `-short`): upload 2 GiB of pseudo-random data via `artifact.Create` on the S3 backend, sample `runtime.ReadMemStats().HeapInuse` every 100 ms, fail if the peak exceeds the starting value by more than 200 MiB (SC-003)
- [X] T020 [US1] Add `Test(ctx, d store.Destination) (TestResult, error)` in `internal/storage/storage.go`: write probe `.dumpling-probe-<id>`, read back, remove; map errors to codes `auth_failed`, `not_found`, `unreachable`, `permission_denied` with secret-free messages (S3: minio `ErrorResponse.Code` AccessDenied/InvalidAccessKeyId/SignatureDoesNotMatch → auth_failed, NoSuchBucket → not_found, net errors → unreachable)
- [X] T021 [US1] Server in `internal/server/handlers.go` + routes in `internal/server/server.go`: `POST /api/destinations/test` (body as create, optional `id` to reuse stored secrets) → `{ok, host_key_fingerprint?}` / 400 `test_failed`; `POST`/`PUT /api/destinations` accept `type` and `s3` object per contracts/api.md, run test before save, map `ErrLocationLocked` → 409 `location_locked`; list returns `type`, `s3`, `sftp`, `has_secret`, `backup_count`, `free_bytes` only for local
- [X] T022 [US1] Backups list/detail in `internal/server/handlers.go` add `destination_type` and `location` (via `storage` location formatting without connecting)
- [X] T023 [P] [US1] Extend `internal/server/leak_test.go`: create S3 destination with secret `S3-SECRET-XYZ`, assert absent from GET/POST/PUT responses and op logs; also run a backup to an S3 destination with wrong credentials and assert `S3-SECRET-XYZ` is absent from that operation's log and message
- [X] T024 [P] [US1] UI types in `web/src/types.ts`: `Destination` gains `type`, `s3?`, `sftp?`, `has_secret`, `backup_count`; `Backup` gains `imported`, `locked`, `destination_type`, `location`
- [X] T025 [US1] UI `web/src/pages/Destinations.tsx`: type selector (Local / S3 / SFTP placeholder disabled until US2); S3 form fields endpoint, TLS checkbox (default on), region, bucket, prefix, access key, secret (password input, placeholder "stored — leave blank to keep" when `has_secret`), path-style checkbox; "Test" button calling `/destinations/test` showing ok/error; table column "Location" instead of "Path"
- [X] T026 [US1] UI `web/src/pages/Backups.tsx`: show `location` column/tooltip and destination type badge

**Checkpoint**: S3 backups work end-to-end (restore covered by US3).

---

## Phase 4: User Story 3 - Restore and verify from remote destinations (Priority: P1)

**Goal**: restore/verify any remote backup with checksum-before-touch.

**Independent Test**: restore S3 backup into fresh db, compare; verify ok, then corrupt object → mismatch; delete object → missing.

- [X] T027 [US3] In `internal/runner/restore.go` ensure remote fetch happens inside the operation (logged "downloading <location>"), checksum on the temp file before `eng.Restore`, cleanup deferred; passphrase resolution order body → `backup.Passphrase` (unsealed) → plan passphrase; locked backup without body passphrase → 400 `backup_locked` in `plan()`
- [X] T028 [US3] Parameterise `internal/runner/roundtrip_integration_test.go`: for each engine run the round-trip for destination types `local`, `s3` (if `DUMPLING_TEST_S3_ENDPOINT`), `sftp` (if `DUMPLING_TEST_SFTP_ADDR`, after US2) as subtests; s3 subtest also runs verify ok, overwrites object bytes via minio client → verify `checksum mismatch`, removes object → verify `artifact missing`
- [X] T029 [P] [US3] UI `web/src/pages/RestoreWizard.tsx`: show "downloading from <location>" hint for remote backups; handle `backup_locked` error with passphrase prompt

**Checkpoint**: MVP = US1 + US3 (S3 backup + restore).

---

## Phase 5: User Story 2 - Back up to an SFTP server (Priority: P2)

**Goal**: SFTP destination with pinned host key.

**Independent Test**: create SFTP destination on `atmoz/sftp`, backup → file in `/upload`; recreate container → host key mismatch error.

- [X] T030 [US2] Create `internal/storage/sftp.go`: `ssh.Dial` with `HostKeyCallback` comparing `ssh.FingerprintSHA256(key)` to pinned `host_key_fingerprint` (mismatch → error "host key mismatch"); auth `ssh.Password` or `ssh.ParsePrivateKey`/`ParsePrivateKeyWithPassphrase`; `sftp.NewClient`; `Create` → `MkdirAll(dir)`, refuse existing final (`Stat`), `OpenFile(final+".partial", O_WRONLY|O_CREATE|O_EXCL)`; `Commit` = close + `Rename(partial, final)` (fails if exists) else remove partial; `Abort` = close + remove partial; not-exist → `fs.ErrNotExist`; `List` via `client.Walk`; `Cleanup` removes `*.partial`; `Location` = `sftp://host:port/remote_dir/rel`
- [X] T031 [US2] `ProbeHostKey(ctx, host, port) (fingerprint string, err)` in `internal/storage/sftp.go` (HostKeyCallback capturing key, auth not required); `Test` for sftp returns `host_key_fingerprint` and, if the request carried a fingerprint, fails with `host_key_mismatch` when different
- [X] T032 [US2] Server in `internal/server/handlers.go`: sftp body per contracts/api.md; on create/update re-probe host key and require equality with posted `host_key_fingerprint` → else 400 `host_key_mismatch`; secrets `password`/`private_key`/`key_passphrase` write-only
- [X] T033 [P] [US2] Unit test in `internal/storage/sftp_test.go` using an in-process SSH server (`x/crypto/ssh` + `sftp.NewServer`) with known host key: pinned fingerprint ok; different fingerprint → "host key mismatch", nothing written; private-key login with and without key passphrase
- [X] T034 [US2] UI `web/src/pages/Destinations.tsx`: enable SFTP form — host, port (22), username, auth radio password/key, password or private key textarea + key passphrase, remote dir; Test shows fingerprint with "Trust this host key" confirmation; Save sends the confirmed fingerprint; show pinned fingerprint read-only on edit
- [X] T035 [US2] Extend `internal/server/leak_test.go` with SFTP password and private key marker strings; also run a backup to an SFTP destination with a wrong password and assert the marker is absent from that operation's log and message

**Checkpoint**: SFTP backup/restore works (round-trip subtest from T028 now active).

---

## Phase 6: User Story 4 - Retention and pruning on remote destinations (Priority: P2)

**Goal**: plan retention deletes remote backups; per-item failures → warning.

**Independent Test**: plan keep last 2 on S3, run 3× → 2 objects remain.

- [X] T036 [US4] In `prune` (`internal/runner/maintain.go`) open the backend once per prune; on per-backup delete error keep backup in history and finish operation with status `warning` naming failed backups (was `failed`); retention query unchanged (`plan_id`) so imported backups are never pruned; `l.AddSecret` for all destination secrets
- [X] T037 [US4] Integration check in `internal/runner/roundtrip_integration_test.go` (s3 subtest): plan with `retention.last = 2`, run 3 backups → `List(".json")` returns 2 and history has 2 `ok`
- [X] T038 [P] [US4] Unit test in `internal/runner/runner_test.go`: fake backend whose `Remove` fails for one rel → prune status `warning`, that backup still `ok`

---

## Phase 7: User Story 5 - Manage destinations, rescan and unlock from the UI (Priority: P3)

**Goal**: full CRUD with write-only secrets, rescan/auto-rescan import, unlock imported encrypted backups.

**Independent Test**: fresh install + existing bucket → create destination imports backups; locked encrypted backup unlocked with "apply to same origin"; restore works.

- [X] T039 [US5] Create `internal/runner/rescan.go` `Rescan(dest) (RescanResult, error)` per research R7: `List(".json")`, for each sidecar: parse; skip `known` if `BackupByPath` exists or `OperationExists(operation_id)`; `bad_metadata` if any of `engine`, `database`, `sha256`, `size_bytes`, `encrypted`, `gzip`, `started_at` missing or engine not `postgres|mysql|mssql` (missing `connection` → `""`); `no_artifact` if `Stat(rel)` not-exist; `size_mismatch` if size differs; else `InsertBackup` with `Imported=true`, `OriginPlanID=meta.plan_id`, `PlanName=meta.plan`, `ConnectionName=meta.connection`, status `ok`, no plan/operation/connection FK. Never writes to the destination. `ponytail:` synchronous, move to background op if destinations grow to tens of thousands of backups
- [X] T040 [US5] `Unlock(backupID, passphrase, applyToOrigin) ([]string, error)` in `internal/runner/rescan.go`: 400 `not_locked` if not locked; open artifact stream via backend, `artifact.CheckPassphrase` → wrong → 400 `wrong_passphrase`, nothing stored; else `SetBackupPassphrase`; if `applyToOrigin` try every locked backup with same `OriginPlanID` (or when empty same `Engine`+`Database`) and store only where `CheckPassphrase` succeeds; return unlocked ids
- [X] T041 [P] [US5] Tests in `internal/runner/rescan_test.go` (local backend): import of valid sidecars; idempotent second run (`known`); sidecar with own `operation_id` skipped; missing artifact → `no_artifact`; truncated artifact → `size_mismatch`; garbage JSON → `bad_metadata`; imported encrypted → `locked`; unlock wrong passphrase keeps locked; apply-to-origin unlocks only matching backups; imported backups excluded from prune
- [X] T042 [US5] Server in `internal/server/handlers.go` + `internal/server/server.go`: `POST /api/destinations/{id}/rescan` → `{imported, skipped:[{path, reason}]}` (502 `unreachable`); `POST /api/destinations` returns `201 {destination, rescan}` after automatic rescan; `POST /api/backups/{id}/unlock` `{passphrase, apply_to_origin}` → `{unlocked: []}`; `l.AddSecret`/no logging of passphrase; `DELETE /api/destinations/{id}` → 409 `in_use` with `detail: {plans: [names], backup_count}` (new `store.DestinationUsage(id)` in `internal/store/entities.go`); all list fields JSON arrays never null
- [X] T043 [US5] UI `web/src/pages/Destinations.tsx`: "Rescan" button per row showing imported/skipped summary; after create show auto-rescan summary; on refused delete show "Used by plans: X, Y — N backups"; `location_locked` error shown on edit; location fields read-only when `backup_count > 0`
- [X] T044 [US5] UI `web/src/pages/Backups.tsx`: "imported" and "locked" badges; "Unlock" dialog (passphrase + "apply to all backups of the same origin" checkbox) calling `/backups/{id}/unlock`, shows count unlocked or `wrong_passphrase`
- [X] T045 [US5] Extend `internal/server/leak_test.go`: unlock passphrase never in responses or logs

---

## Phase 8: Polish & Cross-Cutting Concerns

- [X] T046 [P] Update `specs/001-db-backup-webui/contracts/api.md` Destinations/Backups sections to reference `specs/002-s3-sftp-destinations/contracts/api.md`
- [X] T047 [P] Update `README.md`: destination types, S3/SFTP setup, host key pinning, rescan/disaster recovery, unlock, temp space needed for remote restore
- [X] T048 [P] Update `CLAUDE.md`: `internal/storage` in Architecture, new compose services and test env vars in Commands
- [X] T049 [P] Update `CHANGELOG.md`: S3 and SFTP destinations, rescan, unlock, migration; behavior change: prune with failed deletes now ends `warning` (was `failed`) for local destinations too
- [ ] T050 Run `go vet ./... && go test ./...`, `cd web && npm test && npm run build`, and integration `docker compose -f testdata/compose.yaml run --rm it`; walk `specs/002-s3-sftp-destinations/quickstart.md` manual steps 1–9

---

## Dependencies & Execution Order

### Phase Dependencies

- Setup (T001–T002) → Foundational (T003–T018) → user stories.
- US1 (T019–T026) first; US3 (T027–T029) needs US1 for a remote backend to test against (local path works without it).
- US2 (T030–T035) needs Foundational only; T028's sftp subtest activates after T030.
- US4 (T036–T038) needs Foundational; its integration check needs US1.
- US5 (T039–T045) needs Foundational; rescan/unlock work on any backend.
- Polish after desired stories.

### Key task dependencies

- T003 → T005, T006, T007
- T009 → T010 → T011 → T014, T015
- T011 → T040 (`CheckPassphrase`)
- T019 → T020 → T021 → T025
- T030 → T031 → T032 → T034
- T039 → T042 → T043; T040 → T042 → T044

### Parallel Opportunities

- T002 with T001; T004, T008, T018 alongside following implementation tasks once their target exists.
- After Foundational: US1, US2, US5 backend work in parallel (different files: `s3.go`, `sftp.go`, `rescan.go`).
- UI tasks T024/T029 parallel to backend tasks of the same story.

## Parallel Example: User Story 1

```text
T019 internal/storage/s3.go        (backend)
T024 web/src/types.ts              (UI types)    [P]
T023 internal/server/leak_test.go  (test)        [P]
```

## Implementation Strategy

### MVP First

1. Phase 1–2 (no user-visible change, all existing tests green).
2. US1 + US3 → S3 backup and restore. **Stop and validate** with quickstart steps 1, 3, 4.

### Incremental Delivery

3. US2 (SFTP) → validate steps 2, 7.
4. US4 (remote retention) → step 5.
5. US5 (rescan/unlock/UI management) → step 8.
6. Polish + full quickstart incl. steps 6 and 9.
