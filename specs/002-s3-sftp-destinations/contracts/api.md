# API Contract Delta: S3 and SFTP Destinations

Extends `specs/001-db-backup-webui/contracts/api.md`. Same auth, CSRF header, error shape
(`{"error": "<message>", "code": "<code>"}`, plus `detail` where noted). List fields are always JSON arrays.

## Destination object (response)

```json
{
  "id": "…", "name": "offsite", "type": "s3",
  "path": "",
  "s3":   {"endpoint": "minio:9000", "use_tls": false, "region": "us-east-1", "bucket": "dumps",
           "prefix": "prod", "access_key_id": "AK…", "path_style": true},
  "sftp": null,
  "has_secret": {"secret_access_key": true},
  "free_bytes": null,
  "backup_count": 12
}
```

`sftp` shape: `{"host", "port", "username", "auth": "password"|"key", "remote_dir",
"host_key_fingerprint"}`. `free_bytes` only for `local`.

## Destinations

| Method | Path | Body / Notes |
|--------|------|--------------|
| GET | `/api/destinations` | list of destination objects |
| POST | `/api/destinations/test` | create/update body (+ optional `id` to reuse stored secrets) → `{"ok": true, "host_key_fingerprint"?: "SHA256:…"}`; failure → 400 `test_failed` with secret-free message, reason in `detail` (`auth_failed`, `not_found`, `unreachable`, `permission_denied`) and `host_key_fingerprint` when known; SFTP key differing from the sent one → 400 `host_key_mismatch` |
| POST | `/api/destinations` | `{name, type, path?, s3?: {…, secret_access_key}, sftp?: {…, password?, private_key?, key_passphrase?, host_key_fingerprint}}`; runs test, then automatic rescan → `201` destination object plus `rescan: {imported, skipped: [{path, reason}]}`; 400 `test_failed`, 400 `host_key_mismatch`, 409 `duplicate` |
| PUT | `/api/destinations/{id}` | same body; blank secret = keep; 409 `location_locked` when location fields change while backups exist |
| DELETE | `/api/destinations/{id}` | 409 `in_use` with `{plans: [names], backup_count}` in error `detail` |
| POST | `/api/destinations/{id}/rescan` | → `{imported: n, skipped: [{path, reason}]}`; 502 `unreachable`; 409 `rescan_in_progress` |

Skip reasons: `known`, `no_artifact`, `size_mismatch`, `bad_metadata`.

## Backups (changes)

Backup object gains `imported` (bool), `locked` (bool), `destination_type`, `location` (display string).

| Method | Path | Body / Notes |
|--------|------|--------------|
| POST | `/api/backups/{id}/unlock` | `{passphrase, apply_to_origin: bool}` → `{"unlocked": ["id", …]}`; 400 `wrong_passphrase`; 400 `not_locked` |
| POST | `/api/backups/{id}/verify` | unchanged; operation message distinguishes `artifact missing` / `destination unreachable` / `checksum mismatch` |

## Restore (changes)

`POST /api/restores` on a locked backup without `passphrase` → 400 `backup_locked` (preflight reports
`passphrase_required: true`). Passphrase resolution: body → backup stored passphrase → plan passphrase.
