# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Web UI and API for hot backup and restore of PostgreSQL, MySQL and SQL Server databases
  (feature `001-db-backup-webui`).
- Connections with test, database listing and client-tool detection; filesystem destinations.
- On-demand backups streamed through gzip, optional age encryption and SHA-256 into atomic
  artifacts with JSON metadata sidecars.
- Restore to any same-engine target under any name; typed confirmation before drop-and-recreate;
  checksum verification before touching the target; version-mismatch warning; "skip ownership".
- Scheduled plans (cron/presets) with last/daily/weekly/monthly retention and overlap protection.
- Operation history with filters, live logs (SSE), on-demand verification and backup deletion.
- Single-admin authentication (bcrypt, SameSite=Strict session cookie, CSRF header, login rate limit),
  secrets encrypted at rest, loopback-by-default binding, optional HTTPS.
- Startup recovery marking interrupted operations and removing partial files.
- Dockerfile bundling PostgreSQL 17 and MySQL 8.4 clients; Docker Compose test environment and
  integration round-trip tests for all three engines.
- Environment variables for every flag (`DUMPLING_DATA_DIR`, `DUMPLING_BIND`, `DUMPLING_TLS_CERT`,
  `DUMPLING_TLS_KEY`); explicit flags take precedence.
- `dumpling` service in `testdata/compose.yaml` to try the app next to the test databases.

### Fixed

- Time zones (`TZ`, `CRON_TZ=` in plan schedules) now work in images without tzdata: zoneinfo is
  embedded in the binary.
- Blank page when a PostgreSQL connection test failed (wrong host or credentials): the API returned
  `warnings: null` and the UI crashed while rendering it. The API now always returns arrays, and an
  error boundary shows render errors instead of a blank page.
- Client tool errors are no longer double-prefixed (`psql: psql: error: …`).
