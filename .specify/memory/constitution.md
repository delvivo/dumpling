<!--
Sync Impact Report
- Version change: 1.0.0 → 1.1.0 (MINOR: materially expanded guidance in Principle III)
- Modified principles: III. Hot Backup, Zero Downtime (added bounded exception for a brief
  global lock at backup start: opt-in, acknowledged, abort instead of stall)
- Added sections: none
- Removed sections: none
- Templates: none modified (templates read the constitution at runtime)
- Deferred TODOs: none
- Trigger: feature 003-incremental-backups (MySQL binlog position via mysqldump --source-data)
-->

# Dumpling Constitution

Dumpling is a self-hosted web UI for hot backup and restore of databases (initially
PostgreSQL, MySQL, Microsoft SQL Server), inspired by Backrest. Unlike Backrest, targets are
databases: any backup MUST be restorable to a database of the user's choice, not only its origin.

## Core Principles

### I. Data Integrity (NON-NEGOTIABLE)

- A backup MUST NOT be reported as successful unless the dump tool exited cleanly and the
  artifact was fully written and checksummed.
- Every backup artifact MUST carry metadata (source engine, engine version, database, timestamp,
  size, checksum) sufficient to verify and restore it.
- Artifacts MUST be verifiable on demand (checksum re-check at minimum).
- Failures MUST be surfaced to the user; partial artifacts MUST be marked failed or removed,
  never presented as valid.

Rationale: a backup tool that silently produces bad backups is worse than none.

### II. Safe Restore

- Restore target is chosen explicitly by the user and MAY differ from the backup source
  (different host, database name, or instance of the same engine).
- Any restore that overwrites or drops existing data MUST require explicit confirmation naming
  the target database; no default destructive action.
- Restore across incompatible engines (e.g. MySQL dump into PostgreSQL) MUST be rejected up front.

Rationale: restore is the moment of highest risk; wrong-target mistakes are irreversible.

### III. Hot Backup, Zero Downtime

- Backups MUST run against live databases without stopping them or blocking normal writes,
  using each engine's consistent-snapshot mechanism (e.g. `pg_dump` snapshot,
  `--single-transaction` for InnoDB, native `BACKUP DATABASE` for SQL Server).
- Engine-specific limitations (e.g. non-transactional MySQL tables) MUST be documented and
  surfaced, not hidden.
- A brief global lock at backup start is permitted only when an engine offers no lock-free way to
  record a consistent replay position, the user explicitly opts in (acknowledgment stored with the
  plan), and the backup aborts instead of stalling writes when the lock cannot be acquired
  promptly.

### IV. Credential Security

- Database credentials and repository secrets MUST be encrypted at rest.
- Secrets MUST NEVER appear in logs, error messages, API responses, or process arguments visible
  to other users (pass via environment or files with restrictive permissions).
- The web UI MUST require authentication.

### V. Simplicity

- YAGNI: build only what a current spec requires; no speculative abstractions.
- Prefer Go stdlib and native platform features over new dependencies; each new dependency
  MUST be justified in the plan.
- Prefer delegating to each engine's official dump/restore tools over reimplementing dump logic.

### VI. Tests for Non-Trivial Logic

- Non-trivial logic (scheduling, retention, command construction, encryption, restore safety
  checks) MUST have automated tests.
- Each supported engine MUST have at least one end-to-end backup→restore round-trip test
  against a real instance (e.g. via containers) before it is declared supported.
- Trivial glue code does not require dedicated tests.

## Technology & Distribution Constraints

- Backend: Go. Frontend: React. License: GPL-3.0.
- Distribution: a single Go binary that serves the API and the embedded frontend build.
- External engine client tools (`pg_dump`, `mysqldump`, `sqlcmd`, etc.) MAY be runtime
  requirements; the app MUST detect missing tools and report them clearly.

## Development Workflow

- Features follow the Spec Kit flow: specify → (clarify) → plan → tasks → (analyze) → implement.
- Work happens on `feature/*` branches off `develop`; `main` is the PR target.
- Each plan MUST include a Constitution Check against these principles; violations MUST be
  justified in the plan's Complexity Tracking or removed.
- After every applied change, `README.md`, `CLAUDE.md`, and `CHANGELOG.md` are updated per
  `CLAUDE.md`.

## Governance

- This constitution supersedes other practices. Amendments are made via `/speckit-constitution`,
  documented in the Sync Impact Report, and versioned with semver: MAJOR for principle removal
  or redefinition, MINOR for new principles/sections or materially expanded guidance, PATCH for
  clarifications.
- Reviews MUST verify compliance with Principles I–IV; added complexity MUST be justified.
- Runtime development guidance lives in `CLAUDE.md`.

**Version**: 1.1.0 | **Ratified**: 2026-10-27 | **Last Amended**: 2026-10-28
