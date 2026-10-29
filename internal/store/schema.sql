CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE CHECK (length(username) BETWEEN 1 AND 64),
  password_hash TEXT NOT NULL,
  created_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS connections (
  id                TEXT PRIMARY KEY,
  name              TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
  engine            TEXT NOT NULL CHECK (engine IN ('postgres','mysql','mssql')),
  host              TEXT NOT NULL,
  port              INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
  username          TEXT NOT NULL,
  password          TEXT NOT NULL DEFAULT '',  -- sealed
  tls_mode          TEXT NOT NULL DEFAULT '',
  share_path_server TEXT NOT NULL DEFAULT '',
  share_path_local  TEXT NOT NULL DEFAULT '',
  server_version    TEXT NOT NULL DEFAULT '',
  tested_at         TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS destinations (
  id   TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
  path TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS plans (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE CHECK (length(name) BETWEEN 1 AND 64),
  connection_id  TEXT NOT NULL REFERENCES connections(id),
  databases      TEXT NOT NULL CHECK (json_array_length(databases) >= 1),
  destination_id TEXT NOT NULL REFERENCES destinations(id),
  schedule       TEXT NOT NULL,
  retention      TEXT NOT NULL,
  passphrase     TEXT NOT NULL DEFAULT '',  -- sealed; '' = not encrypted
  paused         INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS operations (
  id                   TEXT PRIMARY KEY,
  type                 TEXT NOT NULL CHECK (type IN ('backup','restore','prune','verify')),
  status               TEXT NOT NULL CHECK (status IN ('pending','running','success','warning','failed','interrupted','skipped')),
  plan_id              TEXT REFERENCES plans(id) ON DELETE SET NULL,
  connection_id        TEXT REFERENCES connections(id) ON DELETE SET NULL,
  backup_id            TEXT REFERENCES backups(id) ON DELETE SET NULL,
  target_connection_id TEXT REFERENCES connections(id) ON DELETE SET NULL,
  target_database      TEXT NOT NULL DEFAULT '',
  database             TEXT NOT NULL DEFAULT '',
  message              TEXT NOT NULL DEFAULT '',
  created_at           TEXT NOT NULL,
  started_at           TEXT NOT NULL DEFAULT '',
  finished_at          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS operations_status ON operations(status);
CREATE INDEX IF NOT EXISTS operations_type ON operations(type);
CREATE INDEX IF NOT EXISTS operations_plan ON operations(plan_id);
CREATE INDEX IF NOT EXISTS operations_connection ON operations(connection_id);

CREATE TABLE IF NOT EXISTS backups (
  id              TEXT PRIMARY KEY,
  operation_id    TEXT REFERENCES operations(id) ON DELETE SET NULL,
  plan_id         TEXT REFERENCES plans(id) ON DELETE SET NULL,
  plan_name       TEXT NOT NULL DEFAULT '',
  connection_id   TEXT REFERENCES connections(id) ON DELETE SET NULL,
  connection_name TEXT NOT NULL,
  engine          TEXT NOT NULL CHECK (engine IN ('postgres','mysql','mssql')),
  server_version  TEXT NOT NULL DEFAULT '',
  database        TEXT NOT NULL,
  destination_id  TEXT NOT NULL REFERENCES destinations(id),
  path            TEXT NOT NULL,
  size_bytes      INTEGER NOT NULL,
  sha256          TEXT NOT NULL,
  encrypted       INTEGER NOT NULL,
  status          TEXT NOT NULL CHECK (status IN ('ok','corrupted','deleted')),
  started_at      TEXT NOT NULL,
  finished_at     TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS backups_plan ON backups(plan_id);
