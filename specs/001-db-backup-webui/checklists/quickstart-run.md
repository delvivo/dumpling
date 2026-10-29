# Quickstart Validation Run — 2026-10-27

Environment: Windows 11 host (Go 1.27.3, Node 24), Docker 29.9 with `testdata/compose.yaml`
(postgres 17.11, mysql 8.4, SQL Server 2022 16.0.4260). The host has no client tools, so engine
scenarios ran against the runtime Docker image (`dumpling:dev`) on the compose network via the API
(script-driven, not clicked through the UI).

## Automated

| Check | Result |
|-------|--------|
| `go vet ./...` | ✅ |
| `go test ./...` (incl. 2 GiB bounded-memory pipeline, restore guards, recover, retention, scheduler, auth, secret-leak) | ✅ |
| `npm test` (Vitest: restoreGate, cronPresets) | ✅ 8 tests |
| `npm run build` (tsc + vite) | ✅ |
| `docker compose ... run --rm it` round-trip postgres / mysql / mssql (write load, new-name restore on other instance, overwrite restore, encrypted) | ✅ 0 failed writes on all engines |

## Manual scenarios

| # | Result | How verified |
|---|--------|--------------|
| 1 | ✅ | API: setup 201, second setup 409, login 204, UI served (`<title>Dumpling`) |
| 2 | ✅ | wrong password → clear `password authentication failed`, no password echoed |
| 3 | ✅ | test returns version 17.11 + `["postgres","shop"]` |
| 4 | ✅ | on-demand backup success, row has size/sha256/server_version; SC-004 covered by integration test |
| 5 | ✅ | restore to pg-b as new db, no confirmation needed |
| 6 | ✅ | second restore → 409 `confirmation_required`; with confirmation → success (drop+recreate verified by integration test stray-table check) |
| 7 | ✅ | `engine_mismatch` |
| 8 | ✅ | unit test `TestRestoreChecksumMismatchLeavesTargetUntouched` (flipped byte → corrupted, engine not called) |
| 9 | ✅ | plan "run now" ×3 with `last=2` → 2 backups kept, 1 prune op; cron firing covered by `scheduler_test.go` (not waited on wall clock) |
| 10 | ✅ | encrypted plan, artifacts end in `.age`, restore without typing passphrase succeeded |
| 11 | ✅ | mssql backup → restore to mssql-b, verify ok, no `.bak` left in share |
| 12 | ✅ | unit test `TestRecover` (running op → interrupted, `.partial` removed); not tested by killing a real process |
| 13 | ✅ | `grep -r` for test password and passphrase in `/data` → no match; also `TestNoSecretLeaks` |
| 14 | ✅ | container start with `0.0.0.0:9898` logs the no-TLS warning; loopback start logs none |

Not verified: visual walkthrough of the React pages in a browser (pages build and type-check; API
flows they call were exercised directly).
