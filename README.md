# 🥟 Dumpling

Self-hosted web UI for **hot backups and restores of databases** — PostgreSQL, MySQL and
Microsoft SQL Server — inspired by [Backrest](https://github.com/garethgeorge/backrest).
Unlike Backrest, the targets are databases: any backup can be restored into **a database of your
choice** (same or another instance of the same engine, optionally under a different name).

- Backups of live databases with no downtime, using each engine's own consistent-snapshot tooling
- Scheduled plans (cron or presets) with restic-style retention (last N / daily / weekly / monthly)
- Restore to any same-engine target; overwriting an existing database requires typing its name
- Every artifact is checksummed (SHA-256) and carries a JSON metadata sidecar; verify on demand
- Optional encryption of artifacts with a passphrase ([age](https://age-encryption.org) format)
- Live operation log and filterable history
- One binary (Go) that embeds the web UI (React); GPL-3.0

## Install

### Docker (client tools included)

```sh
docker build -t dumpling .
docker run -d -p 127.0.0.1:9898:9898 -v dumpling-data:/data -v /srv/backups:/backups dumpling
```

The image listens on all interfaces *inside* the container: publish the port on loopback as above,
or serve it behind TLS (`--tls-cert/--tls-key`, or a TLS reverse proxy).

### Try it with Docker Compose (test databases included)

```sh
docker compose -f testdata/compose.yaml up -d --build --wait dumpling   # http://127.0.0.1:9898
docker compose -f testdata/compose.yaml down -v                         # tear down + delete volumes
```

This starts Dumpling plus two instances each of PostgreSQL 17, MySQL 8.4 and SQL Server 2022,
an S3-compatible server (RustFS) and an SFTP server. The comments in `testdata/compose.yaml` list
the host names, ports and test credentials to enter in the UI; use `/backups` as local
destination path.

## Destinations

Backups can be stored in three kinds of destination:

| Type | Settings | Notes |
|------|----------|-------|
| Local directory / mount | absolute path | must exist and be writable; free space is shown |
| S3-compatible bucket | endpoint (empty = AWS, or `https://host:port`), region, bucket, optional key prefix, access key + secret, path-style toggle | AWS S3, MinIO, Backblaze B2, Wasabi, Cloudflare R2, RustFS… static keys only |
| SFTP server | host, port, user, password **or** private key (+ optional passphrase), absolute remote directory | server host key pinned on save |

- **Test** writes, reads and deletes a probe file and reports why it failed (auth, missing
  bucket/directory, permission, unreachable). Saving a destination runs the same test.
- **SFTP host key**: Test shows the server's SHA256 fingerprint; click *Trust this host key*, then
  save. Every later connection refuses a different key ("host key mismatch") — after a legitimate
  server re-key, edit the destination, test and trust the new key.
- Uploads stream (no full copy in memory or on local disk; S3 parts are 64 MiB, max artifact
  625 GiB). An artifact appears on the destination only once completely written, is never
  overwritten, and failed or interrupted uploads leave nothing listed (startup cleans up partial
  files and incomplete S3 multipart uploads).
- **Restore/verify from S3 or SFTP** download the artifact once to `<data-dir>/tmp`, check the
  SHA-256 and restore from that copy, which is then deleted: the host needs free temporary space
  for one artifact. Verify distinguishes *artifact missing*, *destination unreachable* (backup
  status unchanged) and *checksum mismatch*.
- Type and location (path, endpoint/bucket/prefix, host/port/directory) cannot change while a
  destination holds backups; credentials can.

### Disaster recovery: rescan and unlock

Each artifact has a `.json` metadata sidecar next to it. Creating a destination automatically
**rescans** it, and the *Rescan* button does it on demand: backups found there and unknown to this
install are imported into history (nothing on the destination is modified; entries with missing or
inconsistent metadata are skipped and reported). Imported backups belong to no plan and are never
pruned by retention.

Imported **encrypted** backups are *locked*, because their passphrase lived in the lost install.
*Unlock* asks for the passphrase, checks it by decrypting the artifact header, stores it encrypted
and can apply it to all locked backups of the same original plan. Alternatively enter the
passphrase in the restore dialog for a one-off restore.

### From source

Requires Go 1.26+ and Node 22+.

```sh
cd web && npm ci && npm run build && cd ..
go build -o dumpling ./cmd/dumpling
./dumpling --data-dir ./data            # http://127.0.0.1:9898
```

Client tools needed on the host (the UI warns when they are missing):

| Engine | Tools |
|--------|-------|
| PostgreSQL | `psql`, `pg_dump`, `pg_restore` — major version **≥ the server's** |
| MySQL | `mysql`, `mysqldump` from **Oracle MySQL** (the MariaDB client lacks `--set-gtid-purged`) |
| SQL Server | none (native `BACKUP`/`RESTORE` through the Go driver) |

## Configuration

Every flag can also be set by environment variable; an explicit flag wins over the variable.

| Flag | Environment variable | Default | |
|------|----------------------|---------|--|
| `--data-dir` | `DUMPLING_DATA_DIR` | `./data` (image: `/data`) | SQLite state, operation logs, master key |
| `--bind` | `DUMPLING_BIND` | `127.0.0.1:9898` (image: `0.0.0.0:9898`) | listen address; non-loopback without TLS logs a warning |
| `--tls-cert`, `--tls-key` | `DUMPLING_TLS_CERT`, `DUMPLING_TLS_KEY` | – | enable HTTPS with your certificate |
| – | `DUMPLING_MASTER_KEY` | generated `<data-dir>/master.key` | base64 of 32 bytes, encrypts stored secrets |
| – | `TZ` | system time zone | time zone of plan schedules without a `CRON_TZ=` prefix |

On first launch the UI asks you to create the admin account.

## Security notes

- Connection passwords, destination secrets (S3 secret key, SFTP password/private key/key
  passphrase), plan passphrases and unlock passphrases are encrypted at rest (AES-256-GCM) with a master key
  taken from `DUMPLING_MASTER_KEY` (base64 of 32 bytes) or generated in `<data-dir>/master.key` (0600).
  **Limit:** if the key file sits on the same disk as `dumpling.db`, this protects against leaks of
  the database file alone, not against full access to the data directory. Use the env variable to
  keep the key elsewhere. Losing the key makes stored credentials unreadable (re-enter them).
- Secrets never go on command lines (PostgreSQL: `PGPASSWORD` in the child environment; MySQL: a
  temporary 0600 option file) and are redacted from logs.
- Losing an encryption passphrase makes those backups unrecoverable. On-demand backups never store
  it; plans store it (encrypted) so scheduled runs work unattended.

## How each engine is backed up

| Engine | Backup | Restore over an existing database |
|--------|--------|-----------------------------------|
| PostgreSQL | `pg_dump -Fc` (single repeatable-read snapshot) | `DROP DATABASE … WITH (FORCE)`, `CREATE DATABASE`, `pg_restore --exit-on-error` |
| MySQL | `mysqldump --single-transaction --routines --triggers --events --hex-blob --set-gtid-purged=OFF` | `DROP DATABASE`, `CREATE DATABASE`, `mysql < dump` |
| SQL Server | `BACKUP DATABASE … WITH COPY_ONLY, CHECKSUM` into a shared directory | `SET OFFLINE WITH ROLLBACK IMMEDIATE`, `RESTORE … WITH REPLACE, CHECKSUM, MOVE …` |

Caveats:
- **MySQL**: only InnoDB tables are snapshot-consistent; a backup containing other engines (MyISAM…)
  completes with a *warning* listing those tables.
- **"Skip ownership"** on restore: PostgreSQL adds `--no-owner --no-acl`; MySQL strips `DEFINER`
  clauses. Use it when the target lacks the original roles/users.
- A restore first re-checks the artifact's SHA-256; on mismatch the backup is marked *corrupted*
  and the target is not touched.

### SQL Server shared directory

SQL Server writes the `.bak` itself, so each SQL Server connection defines one directory seen by
both sides: *share path as seen by SQL Server* (e.g. `/var/opt/mssql/share` or `D:\share`) and
*the same directory as seen by Dumpling* (local path or mount). "Test connection" verifies it by
having SQL Server write a small probe file that Dumpling must be able to read. The temporary
`.bak` is removed after each backup/restore; the stored artifact is gzip-compressed.

## Development

```sh
go test ./...                         # unit tests
cd web && npm test                    # UI logic tests (Vitest)

# integration: real backup → restore round-trip on all three engines × local/S3/SFTP destinations
docker compose -f testdata/compose.yaml up -d --wait pg-a pg-b mysql-a mysql-b mssql-a mssql-b s3 sftp
docker compose -f testdata/compose.yaml run --rm it
docker compose -f testdata/compose.yaml down
```

`cd web && npm run dev` serves the UI with a proxy to a running backend on `127.0.0.1:9898`.
Specification, plan and tasks live in `specs/001-db-backup-webui/` and
`specs/002-s3-sftp-destinations/`. Storage tests (`internal/storage`) run against local and an
in-process SFTP server; set `DUMPLING_TEST_S3_ENDPOINT`, `DUMPLING_TEST_S3_BUCKET`,
`DUMPLING_TEST_S3_KEY`, `DUMPLING_TEST_S3_SECRET` (and `DUMPLING_TEST_SFTP_*`) to include real
services, e.g. the compose ones on `127.0.0.1:19000` / `127.0.0.1:12222`.

## License

GPL-3.0 — see [LICENSE](LICENSE).
