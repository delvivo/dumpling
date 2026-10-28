# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project state

Dumpling: self-hosted web UI for hot backup/restore of PostgreSQL, MySQL and SQL Server databases (inspired by Backrest). Feature `001-db-backup-webui` is implemented; spec/plan/tasks in `specs/001-db-backup-webui/`, principles in `.specify/memory/constitution.md`. Product/UX context for design work in `PRODUCT.md`, visual system in `DESIGN.md` (+ `.impeccable/design.json`) (Impeccable).

## Commands

```sh
cd web && npm ci && npm run build && cd ..   # build UI into web/dist/ui (embedded by go:embed)
go build -o dumpling ./cmd/dumpling          # single binary
./dumpling --data-dir ./data                 # http://127.0.0.1:9898
go vet ./... && go test ./...                # unit tests (artifact test streams 2 GiB; -short skips it)
cd web && npm test                           # Vitest (restoreGate, cronPresets)
cd web && npm run dev                        # UI dev server, proxies /api to :9898

docker compose -f testdata/compose.yaml up -d --build --wait dumpling   # app + test DBs on :9898

# integration round-trip on real engines (host lacks client tools → runs in a container)
docker compose -f testdata/compose.yaml up -d --wait pg-a pg-b mysql-a mysql-b mssql-a mssql-b s3 sftp
docker compose -f testdata/compose.yaml run --rm it   # runner round-trip × local/s3/sftp + storage contract
docker compose -f testdata/compose.yaml down
```

On Git Bash, `docker run -v` paths need `MSYS_NO_PATHCONV=1`.

Release: `.github/workflows/release.yml` — on `v*` tag push builds UI, vet/test `-short`, cross-compiles (`CGO_ENABLED=0`, `-X main.version=<tag>`) linux/darwin/windows × amd64/arm64 into tar.gz/zip + `SHA256SUMS`, then `gh release create`. `workflow_dispatch` only uploads the `dist` artifact.

## Architecture

- `cmd/dumpling` — flags (each defaults to a `DUMPLING_*` env var), `slog` text handler on stderr with `--log-level`/`DUMPLING_LOG_LEVEL` (debug|info|warn|error, default info), wiring, startup recovery, scheduler loop, loopback/TLS warning.
- `internal/store` — SQLite schema (`schema.sql`, embedded) plus additive migrations (`migrate.go`, `PRAGMA user_version`) and queries; secrets sealed via `internal/secret` (AES-256-GCM, master key from `DUMPLING_MASTER_KEY` or `<data-dir>/master.key`). Destinations (`destinations.go`): `type` + `config` JSON + sealed `secret` JSON; `Public()` strips secrets for responses; location fields locked while backups exist.
- `internal/engine` — `Engine` interface with `postgres` (psql/pg_dump/pg_restore, password via `PGPASSWORD`; restore = `pg_restore -f - | psql --single-transaction`, drops the created db on failure, strips pg_dump 17's `SET transaction_timeout` for servers < 17; pg-b in compose is PG 16 to cover it), `mysql` (mysql/mysqldump, credentials via temp `--defaults-extra-file`), `mssql` (go-mssqldb, native BACKUP/RESTORE through a shared directory). Secrets never on argv.
- `internal/storage` — `Backend` interface (`local`, `s3` via minio-go, `sftp` via pkg/sftp): no-overwrite `Create`/`Commit`, `Open`, `Stat`, `Remove`, `List`, `Cleanup`; "not found" wraps `fs.ErrNotExist`. `Test` probes write/read/delete; SFTP host key pinned (`ProbeHostKey`). Tests: local + in-process SFTP always, real S3/SFTP via `DUMPLING_TEST_S3_*`/`DUMPLING_TEST_SFTP_*`.
- `internal/artifact` — streaming pipeline gzip → age → sha256 into a `storage.Upload` + JSON sidecar (`<rel>.json`, includes `plan_id`).
- `internal/runner` — backup/restore/verify/prune operations, in-memory locks (per plan, per restore target, per rescan), op logs with secret redaction (destination secrets via `openDest`), `Recover()`. Remote restore/verify download to `<data-dir>/tmp/<op>.artifact` (`fetch`) and checksum before touching the target. `rescan.go`: `Rescan` imports sidecars unknown to this install, `Unlock` stores passphrases of imported encrypted backups. `openStorage`/`newEngine` are test hooks.
- `internal/schedule` — cron parsing (robfig/cron), `Keep()` retention, scheduler loop. Imported by runner (retention); scheduler calls runner via a callback to avoid an import cycle.
- `internal/server` — stdlib mux API per `specs/001-db-backup-webui/contracts/api.md`, session auth + `X-Requested-With: dumpling` CSRF header, SSE hub (`/api/events`), SPA serving.
- `web/` — Vite + React + TS, plain CSS, `react-router` declarative routes; pages in `web/src/pages/`, wrapped in `ErrorBoundary` (keyed by path). Dialogs use `Modal.tsx` (native `<dialog>` + `showModal()`), never `<dialog open>`; destructive confirmations use its `Confirm`, never `window.confirm`/`prompt`. List tables carry explicit ARIA table roles and their `<td>`s a `data-label` (column name), used to stack rows on narrow screens without losing table semantics. API list fields must always be JSON arrays, never `null` (UI maps over them).

## Handling change requests (mandatory)

Whenever a change is requested, first ask the user whether to use Spec Kit or proceed without it.

**With Spec Kit:**
- Not on a feature branch → create `feature/<branch-name>`, where `<branch-name>` is the name Spec Kit generates.
- Already on a `feature/*` branch and the change fits that feature's scope → stay on it; name the new spec with the current feature name as its root (e.g. on `feature/001-auth`, a new spec is `001-auth-<suffix>`).
- Already on a `feature/*` branch but the change is outside its scope → ask the user whether to continue on the current branch or open a new feature branch.

**Without Spec Kit:** enter plan mode before making changes.

**After every applied change**, update the docs:
- `README.md` — create it if missing
- `CLAUDE.md` — keep commands/architecture current
- `CHANGELOG.md` — create it if missing; do NOT log changes that only touch `README.md` or `CLAUDE.md`

## Workflow: Spec-Driven Development (Spec Kit)

Features are built through the Spec Kit skills in `.claude/skills/`, in this order (defined in `.specify/workflows/speckit/workflow.yml`):

1. `/speckit-constitution` — amend `.specify/memory/constitution.md` (v1.0.0 ratified)
2. `/speckit-specify <description>` — creates a sequentially numbered feature directory with `spec.md`
3. `/speckit-clarify` (optional) — resolve underspecified areas in the spec
4. `/speckit-plan` — produces `plan.md` and design artifacts
5. `/speckit-tasks` — produces dependency-ordered `tasks.md`
6. `/speckit-analyze` (optional) — cross-artifact consistency check
7. `/speckit-implement` — executes `tasks.md`; `/speckit-converge` appends tasks for any remaining unbuilt work

Notes:
- Helper scripts run via Python (`"script": "py"` in `.specify/init-options.json`); PowerShell equivalents live alongside in `.specify/scripts/powershell/`.
- The active feature directory is resolved from `SPECIFY_FEATURE_DIRECTORY` env var or `.specify/feature.json` (`feature_directory` key).
- Templates for spec/plan/tasks/checklist are in `.specify/templates/`.

## Git

Work happens on `develop`; `main` is the PR target.
