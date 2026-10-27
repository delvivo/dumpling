export type Engine = "postgres" | "mysql" | "mssql";
export interface Connection {
  id: string; name: string; engine: Engine; host: string; port: number; username: string;
  password?: string; password_set?: boolean; tls_mode: string;
  share_path_server: string; share_path_local: string; server_version: string; tested_at: string;
}
export interface Destination { id: string; name: string; path: string; free_bytes?: number }
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
}
export interface Backup {
  id: string; operation_id: string; plan_id: string; plan_name: string; connection_id: string; connection_name: string;
  engine: Engine; server_version: string; database: string; destination_id: string; path: string;
  size_bytes: number; sha256: string; encrypted: boolean; status: string; started_at: string; finished_at: string;
}
