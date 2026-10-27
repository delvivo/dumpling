# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

Self-hosters first: an individual running their own PostgreSQL, MySQL or SQL Server instances at
home or on a VPS, sole admin, visiting occasionally to confirm backups ran and to restore when
something breaks. The product must also hold up for small-team production use (a dev/ops person
managing several instances), where a restore happens under pressure and a wrong target is
irreversible. One authenticated admin login; no multi-user roles.

## Product Purpose

Hot backup and restore of live databases from a self-hosted web UI. Register connections and
destinations, run backups on demand or on schedule with retention, browse history and logs,
verify artifacts, restore. Success: backups that are provably valid (checksummed, verifiable) and
restores that land on the intended target without surprises.

## Positioning

Two claims, equally weighted:

- **One UI, three engines, one binary.** PostgreSQL, MySQL and SQL Server in a single Go binary
  with an embedded UI, each using its engine's native consistent-snapshot tooling (`pg_dump`,
  `mysqldump --single-transaction`, native `BACKUP DATABASE`).
- **Restore anywhere, safely.** Any backup restores into a database of the user's choice: same
  database, a new name, or another instance of the same engine. Overwriting an existing database
  requires typing its exact name.

Inspired by Backrest, but targets are databases rather than filesystems.

## Operating Context

- Workflows: connections (test, list databases) → destinations (local dir, S3-compatible, SFTP
  with pinned host key) → plans (cron or presets, restic-style retention, optional age
  encryption) → backups list → restore wizard / verify → operations timeline with live log (SSE).
- Deployed via Docker or bare binary; binds `127.0.0.1` by default, optional TLS, warning when
  exposed without TLS.
- Engine terminology is kept as-is: connection, destination, plan, backup, artifact, sidecar,
  operation, verify, prune, rescan, unlock.

## Capabilities and Constraints

- Constitution (`.specify/memory/constitution.md`) binds: data integrity (never report a backup
  as successful unless complete and checksummed; failures always surfaced), safe restore
  (explicit target, typed confirmation for destructive restores, cross-engine rejected up front),
  zero-downtime hot backups, credential security (secrets never shown in logs, errors, API
  responses), simplicity (YAGNI, stdlib first), tests for non-trivial logic.
- Frontend: React + TypeScript + Vite, plain CSS, no UI library; embedded in the Go binary.
- Limits: artifacts up to ~100 GB streamed; S3 static keys only.

## Brand Commitments

- Name: **Dumpling**.
- Mark: the 🥟 emoji.

## Evidence on Hand

- `README.md`, `specs/001-db-backup-webui/` (spec, plan, API contract), `testdata/compose.yaml`
  (demo stack with two instances per engine, S3 and SFTP).
- No testimonials, users, benchmarks or press exist; do not fabricate them.

## Product Principles

1. **Trust is the product.** Never imply a backup is good unless it is proven; show failures
   plainly with the real error.
2. **Restore is the riskiest moment.** Make the target unmistakable and destructive actions
   deliberate; speed never outranks clarity there.
3. **Glanceable health for the occasional visitor.** Someone who checks in weekly should see at
   once whether backups are running and passing.
4. **Engines differ; say so.** Surface engine-specific limits instead of hiding them behind a
   uniform facade.
5. **Small and self-contained.** One binary, no extra services, no dependency without a reason.
