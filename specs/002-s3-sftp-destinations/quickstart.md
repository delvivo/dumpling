# Quickstart: validate S3 and SFTP destinations

## Prerequisites

Docker. Test services added to `testdata/compose.yaml`: `s3` (RustFS, S3-compatible — MinIO no longer publishes images; keys `minioadmin`/`minioadmin`,
bucket `dumpling` created by an init step) and `sftp` (`atmoz/sftp`, user `dumpling`, dir `/upload`).

```sh
docker compose -f testdata/compose.yaml up -d --build --wait dumpling s3 sftp pg-a pg-b
```

## Automated

```sh
go vet ./... && go test -short ./...                 # unit: storage contract (local), migration, rescan, unlock
docker compose -f testdata/compose.yaml run --rm it  # round-trip per engine × {local, s3, sftp}
```

Expected: all pass; round-trip restores data identical to source for every engine/destination pair (SC-002).

## Manual (UI at http://127.0.0.1:9898)

1. **S3** — Destinations → New → S3: endpoint `s3:9000`, TLS off, region `us-east-1`, bucket
   `dumpling`, prefix `prod`, path-style on, keys `minioadmin`. Test → ok. Save.
2. **SFTP** — New → SFTP: host `sftp`, user `dumpling`, password, dir `/upload`. Test → fingerprint
   shown. Save → pinned.
3. Back up `pg-a` on-demand to each destination → success; history shows `s3://dumpling/prod/…` and
   `sftp://sftp:22/upload/…`.
4. Restore each into `pg-b` under a new name → success.
5. Plan "keep last 2" on S3, run 3× → 2 objects remain in the bucket (`aws --endpoint-url http://127.0.0.1:19000 s3 ls --recursive s3://dumpling`).
6. Kill the upload: start a backup of a large db, `docker compose restart dumpling` mid-run →
   operation `interrupted`, no new backup listed, no incomplete multipart upload left (SC-004;
   automated by the "crash" case in T013).
7. Host key: recreate the `sftp` container (new host key) → next backup fails `host key mismatch`.
8. **Rescan / DR** — stop dumpling, delete its data volume, start fresh, create the same S3
   destination → response reports imported backups; encrypted ones show "locked". Unlock one with
   "apply to same origin" → all of that plan unlock; restore one.
9. Secret leak: `grep` API responses and `data/logs` for the S3 secret / SFTP password → no match (SC-005).
