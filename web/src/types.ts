export type Engine = "postgres" | "mysql" | "mssql";
export interface Connection {
  id: string; name: string; engine: Engine; host: string; port: number; username: string;
  password?: string; password_set?: boolean; tls_mode: string;
  share_path_server: string; share_path_local: string; server_version: string; tested_at: string;
}
export type DestinationType = "local" | "s3" | "sftp";
export interface S3Config {
  endpoint: string; use_tls: boolean; region: string; bucket: string; prefix: string; access_key_id: string;
  path_style: boolean; secret_access_key?: string;
}
export interface SFTPConfig {
  host: string; port: number; username: string; auth: "password" | "key"; remote_dir: string;
  host_key_fingerprint: string; password?: string; private_key?: string; key_passphrase?: string;
}
export interface RescanResult { imported: number; skipped: { path: string; reason: string }[] }
export interface Destination {
  id: string; name: string; type: DestinationType; path: string; s3: S3Config | null; sftp: SFTPConfig | null;
  has_secret?: Record<string, boolean>; free_bytes?: number; backup_count?: number; rescan?: RescanResult;
}
export interface Retention { last: number; daily: number; weekly: number; monthly: number }
export interface Operation {
  id: string; type: string; status: string; plan_id: string; connection_id: string; backup_id: string;
  target_connection_id: string; target_database: string; database: string; message: string;
  created_at: string; started_at: string; finished_at: string;
}
export interface Plan {
  id: string; name: string; connection_id: string; databases: string[]; destination_id: string;
  schedule: string; retention: Retention; encrypted: boolean; paused: boolean; passphrase?: string;
  next_run?: string; last_operation?: Operation | null;
  incr_schedule: string; max_chain: number; incr_ack_at?: string; next_incr_run?: string;
  ack_incremental_risk?: boolean;
}
export interface Backup {
  id: string; operation_id: string; plan_id: string; plan_name: string; connection_id: string; connection_name: string;
  engine: Engine; server_version: string; database: string; destination_id: string; path: string;
  size_bytes: number; sha256: string; encrypted: boolean; status: string; started_at: string; finished_at: string;
  imported: boolean; locked: boolean; destination_type: DestinationType; location: string;
  kind: "full" | "incr"; chain_id: string; parent_id: string; seq: number; marker: string; chain_ok: boolean;
}
/** Database value of a PostgreSQL server-wide (physical) plan or backup. */
export const CLUSTER_DB = "*";
export const dbLabel = (db: string) => (db === CLUSTER_DB ? "Entire server" : db);
export interface ChainLink { id: string; kind: "full" | "incr"; seq: number; status: string; started_at: string }
export interface Preflight {
  target_exists: boolean; version_warning?: string; passphrase_required: boolean; passphrase_link?: string;
  chain?: ChainLink[]; chain_error?: string; target_path_ok?: boolean; physical?: boolean;
}
