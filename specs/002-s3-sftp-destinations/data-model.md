# Data Model: S3 and SFTP Storage Destinations

Additive changes only (migration via `PRAGMA user_version`, see research R9).

## destinations (extended)

| Column | Type | Notes |
|--------|------|-------|
| id, name, path | existing | `path` used only by `local` |
| type | TEXT NOT NULL DEFAULT 'local' | `local` \| `s3` \| `sftp`; existing rows become `local` |
| config | TEXT NOT NULL DEFAULT '{}' | JSON, non-secret settings (below) |
| secret | TEXT NOT NULL DEFAULT '' | sealed JSON of secret settings (below) |

**S3 config**: `endpoint` (URL `https://host:port` or host[:port], empty = AWS; an `http`/`https` scheme sets `use_tls`), `use_tls` (default true), `region`, `bucket`,
`prefix` (no leading/trailing `/`), `access_key_id`, `path_style` (bool).
**S3 secret**: `secret_access_key`.

**SFTP config**: `host`, `port` (default 22), `username`, `auth` (`password` \| `key`), `remote_dir`
(absolute), `host_key_fingerprint` (`SHA256:...`, pinned).
**SFTP secret**: `password` | `private_key` (PEM) + optional `key_passphrase`.

Validation:
- name 1–64 chars, unique (existing).
- s3: `bucket`, `region`, `access_key_id`, `secret_access_key` required.
- sftp: `host`, `username`, `remote_dir`, one of password/private_key, `host_key_fingerprint`
  required and must match the server at save time.
- **Location fields** — local `path`; s3 `endpoint`, `bucket`, `prefix`; sftp `host`, `port`,
  `remote_dir`; plus `type` — immutable while the destination has non-deleted backups
  (409 `location_locked`). Credentials, name, `path_style`, `use_tls`, `region` editable.
- Delete refused while referenced: 409 `in_use` with `{"plans": ["<plan name>", …], "backup_count": n}`.

API never returns `secret`; it returns `has_secret: {field: bool}`. Blank secret field on update =
keep stored value.

## backups (extended)

| Column | Type | Notes |
|--------|------|-------|
| imported | INTEGER NOT NULL DEFAULT 0 | 1 = created by rescan; `plan_id`, `operation_id`, `connection_id` NULL |
| origin_plan_id | TEXT NOT NULL DEFAULT '' | plan id from sidecar; not a FK |
| passphrase | TEXT NOT NULL DEFAULT '' | sealed; set by unlock |

Derived (API only): `locked = imported && encrypted && passphrase == ''`;
`location` display string (`/abs/path/rel`, `s3://bucket/prefix/rel`, `sftp://host:port/dir/rel`).

Uniqueness for import: skip when `(destination_id, path)` exists with status ≠ deleted, or when the
sidecar `operation_id` exists in `operations`.

Retention: unchanged query (`plan_id = ?`) — imported backups have NULL `plan_id`, so never pruned (FR-017).

## Sidecar JSON (extended)

Adds `plan_id` to existing fields (`engine, server_version, database, connection, plan, encrypted,
gzip, started_at, finished_at, operation_id, size_bytes, sha256`). Older sidecars without
`plan_id` → origin = engine + database.

## State

Backup status unchanged (`ok`, `corrupted`, `deleted`). Lock state:

```
imported+encrypted, no passphrase ──unlock(valid)──▶ unlocked (restorable)
                                  ──unlock(wrong)──▶ stays locked, nothing stored
```

Verify transitions (R10): missing → `corrupted`; mismatch → `corrupted`; unreachable → unchanged;
ok after corrupted → `ok`.
