# Research: S3 and SFTP Storage Destinations

## R1. S3 client library

- **Decision**: `github.com/minio/minio-go/v7`.
- **Rationale**: one module, speaks any S3-compatible provider (AWS, MinIO, B2, Wasabi, R2),
  supports streaming `PutObject` with unknown size (`size = -1`, multipart, aborts the upload on
  error), `BucketLookup` path-style option, `ListObjects` by prefix, `ListIncompleteUploads` /
  `RemoveIncompleteUpload` for crash cleanup.
- **Alternatives**: `aws-sdk-go-v2` (≈10 modules, AWS-centric config, heavier); hand-written
  SigV4 + multipart (hundreds of lines of protocol code to own — violates Principle V more than a
  dependency does).

## R2. Streaming upload memory (SC-003)

- **Decision**: upload with `size = -1` and explicit `PartSize = 64 MiB`.
- **Rationale**: with unknown size minio-go buffers one part in memory; left unset it sizes parts
  for a 5 TiB object (≈525 MiB buffer), breaking SC-003. 64 MiB × 10 000 parts = 625 GiB max
  artifact, ~64 MiB RAM.
- **Alternatives**: spool locally then upload with known size — needs free local disk equal to the
  artifact and doubles I/O; rejected for backup (kept for restore, see R5).

## R3. SFTP client library

- **Decision**: `github.com/pkg/sftp` over `golang.org/x/crypto/ssh` (x/crypto already a dependency).
- **Rationale**: de-facto Go SFTP client; streaming `File` writes; `Rename` per SFTP v3 fails if the
  target exists — gives "never overwrite" for free.
- **Alternatives**: shelling out to `sftp`/`scp` (secrets on argv/stdin plumbing, runtime tool
  requirement, worse error handling).

## R4. Atomic commit / no overwrite per backend

- **Local**: unchanged — `.partial` + `os.Link`.
- **SFTP**: write `<name>.partial`, then `Rename` → fails if target exists; on error remove partial.
- **S3**: objects become visible only when the (multipart) upload completes, so no partial object is
  ever listed. "Never overwrite": `StatObject` before upload; names embed the operation id suffix, so
  a collision needs a concurrent writer using the same key.
  `ponytail:` stat-then-put race accepted; upgrade path is `If-None-Match: *` once all target
  providers support conditional writes.
- **Sidecar** `<name>.json` written after the artifact commits (same as today); rescan ignores an
  artifact without sidecar.

## R5. Restore / verify from remote (clarification Q2)

- **Decision**: download once to `<data-dir>/tmp/<op-id>.artifact`, then reuse existing
  `artifact.Checksum` / `artifact.Open` on that file; `defer os.Remove`; `Recover()` deletes leftover
  `tmp/*.artifact`. Local destinations keep reading in place (no copy).
- **Rationale**: one download, file cannot change between checksum and restore, zero change to the
  restore pipeline.

## R6. SFTP host key pinning (clarification Q1)

- **Decision**: `POST /api/destinations/test` returns the server's SHA256 fingerprint
  (`ssh.FingerprintSHA256`). Create/update for SFTP requires the client to send back
  `host_key_fingerprint`; the server re-connects and rejects the save if the presented key differs.
  All later connections use a `HostKeyCallback` comparing against the pinned fingerprint.
- **Alternatives**: `ssh.InsecureIgnoreHostKey` (MITM — rejected), known_hosts import (out of scope).

## R7. Rescan (clarification Q3)

- **Decision**: list `*.json` sidecars under the destination root/prefix, parse, and import when:
  artifact object/file exists, its size equals `size_bytes`, required fields present
  (`engine`, `database`, `sha256`, `size_bytes`, `encrypted`, `gzip`). Skip when the path is already
  known for that destination, or when `operation_id` belongs to an operation in this DB (covers
  native backups still in flight — avoids a race with `InsertBackup`). No checksum during rescan
  (verify does that). Synchronous API call returning `{imported, skipped[]}`.
  `ponytail:` synchronous; move to a background operation if destinations with tens of thousands
  of backups appear.
- **Not an operation type**: adding `rescan` to the `operations.type` CHECK would require an SQLite
  table rebuild; the API response is the record.

## R8. Unlock imported encrypted backups (clarification Q4)

- **Decision**: validate a passphrase by opening the artifact stream and running `age.Decrypt` with
  a scrypt identity — header MAC check fails on a wrong passphrase, no full download needed.
  Passphrase sealed with `internal/secret` into `backups.passphrase`. "Apply to same origin" tries
  every locked backup with the same `origin_plan_id` (or, when empty, same engine + database).
- Restore passphrase resolution order: request body → backup's stored passphrase → plan passphrase.

## R9. Schema migration

- **Decision**: `PRAGMA user_version` + ordered list of `ALTER TABLE ... ADD COLUMN` statements in
  `store.Open`. Only additive columns — no table rebuilds.
- **Rationale**: first migration in the project; `CREATE TABLE IF NOT EXISTS` cannot add columns
  to existing databases.

## R10. Verify outcome classification (FR-010)

- Backends map "not found" to `fs.ErrNotExist`. Verify: not found → backup `corrupted`, message
  "artifact missing"; other I/O/network error → operation failed "destination unreachable", backup
  status unchanged; checksum differs → `corrupted`, "checksum mismatch".

## R11. Test infrastructure

- S3: RustFS (`rustfs/rustfs`, bucket created by an `amazon/aws-cli` init container — MinIO stopped publishing Docker images) and an OpenSSH SFTP server (`atmoz/sftp`) added to `testdata/compose.yaml`; an in-process SFTP server (`x/crypto/ssh` + `sftp.InMemHandler`) covers SFTP in plain `go test`.
- Backend contract test (`internal/storage`) runs against local always and against S3/SFTP when
  `DUMPLING_TEST_S3_*` / `DUMPLING_TEST_SFTP_*` env vars are set (the `it` container sets them).
- Existing round-trip integration test parameterised over destination type.
