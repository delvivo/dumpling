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

This starts Dumpling plus two instances each of PostgreSQL 17, MySQL 8.4 and SQL Server 2022.
The header comment of the `dumpling` service in `testdata/compose.yaml` lists the host names,
ports and test password to enter in the UI; use `/backups` as destination path.

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

- Connection passwords and plan passphrases are encrypted at rest (AES-256-GCM) with a master key
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

# integration: real backup → restore round-trip on all three engines
docker compose -f testdata/compose.yaml up -d --wait pg-a pg-b mysql-a mysql-b mssql-a mssql-b
docker compose -f testdata/compose.yaml run --rm it
docker compose -f testdata/compose.yaml down
```

`cd web && npm run dev` serves the UI with a proxy to a running backend on `127.0.0.1:9898`.
Specification, plan and tasks live in `specs/001-db-backup-webui/`.

## License

GPL-3.0 — see [LICENSE](LICENSE).
