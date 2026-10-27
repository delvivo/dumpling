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
