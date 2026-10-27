# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- S3-compatible buckets and SFTP servers as backup destinations (feature `002-s3-sftp-destinations`):
  streaming uploads with atomic, never-overwriting commit; restore and verify download once to a
  temporary file and check the SHA-256 first; remote retention pruning; destination test with
  specific failure reasons; write-only, encrypted destination secrets.
- SFTP host key pinning (fingerprint shown on test, confirmed on save, mismatch refused).
- Destination rescan (automatic on create) importing backups found on a destination, and unlock of
  imported encrypted backups with passphrase validation and "apply to same origin".
- Backup history shows destination type and location; verify distinguishes missing, unreachable
  and checksum mismatch; deleting a used destination lists the plans and backup count.
- Startup recovery cleans partial SFTP files, incomplete S3 multipart uploads and temporary downloads.
- Database schema migrations (`PRAGMA user_version`); backup metadata sidecars record `plan_id`.
- RustFS (S3) and SFTP test services in `testdata/compose.yaml`; integration round-trip per engine
  × destination type.

### Fixed

- Dialogs (Backup now, Restore, Unlock) open as real modals: centered with backdrop instead of below
  the table, Esc closes, focus moves inside and returns to the opener.
- Dark mode: error/success/warning colors and text on primary/danger buttons now meet WCAG AA
  contrast; native controls (checkboxes, selects, scrollbars) follow the dark theme.
- Buttons and fields have borders with at least 3:1 contrast, a visible amber keyboard focus ring
  and hover feedback; backup rows no longer show an amber Restore on every row, and row Delete
  buttons are marked in red.
- Mobile: list tables stack into one card per row on narrow screens instead of scrolling the page
  sideways; login/setup forms fit 320px screens; on touch devices controls are at least 44px tall
  and fields use 16px text (no iOS zoom on focus).
- Encryption passphrases (on-demand backup, plan) must be typed twice; mismatch blocks submit.
- Backup now keeps the dialog and its input open on error; Backup now and Save plan can't be
  double-submitted.
- Accessibility: labelled passphrase fields, named filter selects, errors announced
  (`role="alert"`), running operations and operation status announced (`aria-live`); long
  locations in tables wrap instead of widening the page.

### Changed

- A prune whose deletions partly fail now ends with status `warning` (was `failed`), also for local
  destinations; the backups that could not be deleted stay in history.

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
