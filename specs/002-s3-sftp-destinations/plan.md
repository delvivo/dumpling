# Implementation Plan: S3 and SFTP Storage Destinations

**Branch**: `feature/002-s3-sftp-destinations` | **Date**: 2026-10-27 | **Spec**: [spec.md](spec.md)

**Input**: Feature specification from `specs/002-s3-sftp-destinations/spec.md`

## Summary

Add S3-compatible and SFTP destination types next to local. A new `internal/storage` package
defines one small `Backend` interface (local, s3, sftp) that the artifact pipeline writes into and
the runner reads/deletes/lists through. Backups stream straight to the remote (no local spool);
restore/verify download once to a temp file and reuse the existing checksum/restore path. Adds
SFTP host-key pinning, destination rescan (auto on create) to import existing backups, and an
unlock flow for imported encrypted backups. Schema changes are additive columns behind a
`user_version` migration.

## Technical Context

**Language/Version**: Go 1.26 (backend), TypeScript + React (web, existing Vite setup)

**Primary Dependencies**: existing (age, go-mssqldb, robfig/cron, x/crypto, modernc sqlite) +
`github.com/minio/minio-go/v7` (S3) + `github.com/pkg/sftp` (SFTP over existing `x/crypto/ssh`)

**Storage**: SQLite (app state); artifacts on local FS, S3 bucket, or SFTP directory

**Testing**: `go test` (unit + storage contract), Vitest (UI helpers), docker-compose integration
round-trip with MinIO and `atmoz/sftp`

**Target Platform**: Linux server / container (Windows dev host for build)

**Project Type**: web-service with embedded SPA (single binary)

**Performance Goals**: 2 GiB upload with < 200 MiB RAM above idle (SC-003); streaming both ways

**Constraints**: secrets never on argv/logs/API; no partial artifact ever listed; no overwrite

**Scale/Scope**: single-user self-hosted; destinations with up to low thousands of backups (rescan synchronous)

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | How |
|-----------|--------|-----|
| I. Data Integrity | PASS | success only after remote commit + checksum; SFTP `.partial` + no-overwrite rename; S3 objects visible only on complete upload; crash → incomplete uploads/partials cleaned by `Recover()`; rescan skips inconsistent metadata; verify distinguishes missing/unreachable/mismatch |
| II. Safe Restore | PASS | restore path unchanged; remote artifact downloaded and checksummed before target touched; locked backups refused |
| III. Hot Backup | PASS | engine dump logic untouched |
| IV. Credential Security | PASS | destination secrets + unlock passphrases sealed with `internal/secret`; write-only in API; redacted in op logs (`l.AddSecret`); SFTP host key pinned (no insecure callback); leak test extended |
| V. Simplicity | PASS (justified) | 2 new deps (see Complexity Tracking); one interface with 3 real implementations; no rescan operation type, no table rebuilds |
| VI. Tests | PASS | storage contract test per backend, migration, rescan, unlock, verify classification unit tests; round-trip integration per engine × destination type |

Post-design re-check: PASS — design artifacts introduce no further dependencies or abstractions.

## Project Structure

### Documentation (this feature)

```text
specs/002-s3-sftp-destinations/
├── plan.md
├── research.md
├── data-model.md
├── quickstart.md
├── contracts/api.md
└── tasks.md            # /speckit-tasks
```

### Source Code (repository root)

```text
internal/storage/            # NEW
├── storage.go               # Backend interface, Upload, Open(dest) factory, ErrNotExist mapping
├── local.go                 # current os.* behavior (.partial + Link)
├── s3.go                    # minio-go: PutObject(-1, PartSize 64MiB), Stat, Get, Remove, List, incomplete-upload cleanup
├── sftp.go                  # pkg/sftp: .partial + Rename, pinned HostKeyCallback, fingerprint probe
└── storage_test.go          # contract test: local always; s3/sftp when env set
internal/artifact/artifact.go   # Create/Commit write into storage.Upload instead of *os.File
internal/store/
├── schema.sql               # unchanged base
├── migrate.go               # NEW: user_version + ALTER TABLE ADD COLUMN list
└── entities.go              # Destination{Type, S3, SFTP, secrets sealed}, Backup{Imported, OriginPlanID, Passphrase}, location lock check
internal/runner/
├── backup.go                # write via backend; sidecar gains plan_id
├── maintain.go              # verify/delete via backend; R10 classification
├── restore.go               # remote → tmp download; passphrase order body → backup → plan; locked check
├── rescan.go                # NEW: Rescan(dest) + Unlock(backup, pass, applyOrigin)
└── runner.go                # Recover(): per-backend partial/incomplete cleanup, tmp/*.artifact
internal/server/handlers.go  # destination CRUD/test/rescan, backup unlock, new fields
web/src/pages/Destinations.tsx   # type selector, S3/SFTP forms, test + fingerprint confirm, rescan button
web/src/pages/Backups.tsx        # location column, locked badge, unlock dialog
testdata/compose.yaml        # + minio, minio-init (bucket), sftp; it gets DUMPLING_TEST_S3_*/SFTP_* env
internal/runner/roundtrip_integration_test.go  # loop over destination types
```

**Structure Decision**: existing single Go module + `web/`; one new package `internal/storage`
so runner and artifact depend on an interface, not on S3/SFTP SDKs directly.

### Backend interface (shape, not code)

`Create(rel) → Upload{Write, Commit, Abort}`, `Open(rel) → ReadCloser`, `Stat(rel) → size`,
`Remove(rel)`, `List(prefix) → []rel`, `Cleanup()` (partials / incomplete uploads), `Close()`.
Opened per operation from a `store.Destination`; S3/SFTP errors mapped so "not found" is
`fs.ErrNotExist`.

## Key Decisions (details in research.md)

- Backup streams to remote; restore/verify stage to `<data-dir>/tmp/<op>.artifact` (R2, R5).
- S3 no-overwrite via stat-before-put (`ponytail:` race noted, R4).
- SFTP fingerprint returned by test, required and re-checked on save (R6).
- Rescan synchronous, idempotent, skips own operations' sidecars (R7).
- Unlock validated by age header decrypt, no full download (R8).
- First DB migration via `user_version` (R9).

## Complexity Tracking

| Violation | Why Needed | Simpler Alternative Rejected Because |
|-----------|------------|-------------------------------------|
| New dep `minio-go/v7` | S3 protocol (SigV4, multipart, listing) for any provider | hand-rolled SigV4/multipart is large protocol code to own; `aws-sdk-go-v2` heavier and AWS-centric |
| New dep `pkg/sftp` | SFTP protocol over SSH | shelling out to `sftp` puts secrets in process plumbing and adds a runtime tool |
| `storage.Backend` interface | 3 concrete implementations used by backup, restore, verify, prune, rescan, recover | `switch dest.Type` in 6 call sites duplicates logic and couples runner to SDKs |
