import { useEffect, useState } from "react";
import { del, get, post, put, when } from "../api";
import type { Connection, Engine } from "../types";

const defaults: Record<Engine, { port: number; username: string }> = {
  postgres: { port: 5432, username: "postgres" },
  mysql: { port: 3306, username: "root" },
  mssql: { port: 1433, username: "sa" },
};

const tlsModes: Record<Engine, [string, string][]> = {
  postgres: [["", "prefer (default)"], ["disable", "disable"], ["require", "require"], ["verify-full", "verify-full"]],
  mysql: [["", "preferred (default)"], ["DISABLED", "disabled"], ["REQUIRED", "required"], ["VERIFY_IDENTITY", "verify identity"]],
  mssql: [["", "encrypt (default)"], ["trust", "encrypt, trust server certificate"], ["disable", "disable"]],
};

const empty = (): Connection => ({
  id: "", name: "", engine: "postgres", host: "", port: 5432, username: "postgres", password: "",
  tls_mode: "", share_path_server: "", share_path_local: "", server_version: "", tested_at: "",
});

type TestResult = { ok: boolean; server_version?: string; databases: string[]; warnings: string[]; error?: string };

export default function Connections() {
  const [list, setList] = useState<Connection[]>([]);
  const [edit, setEdit] = useState<Connection | null>(null);
  const [error, setError] = useState("");
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);

  const load = () => get<Connection[]>("/connections").then(setList).catch((e) => setError(e.message));
  useEffect(() => { load(); }, []);

  const set = (patch: Partial<Connection>) => setEdit((c) => ({ ...c!, ...patch }));
  const changeEngine = (engine: Engine) => set({ engine, ...defaults[engine], tls_mode: "" });

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    try {
      if (edit!.id) await put(`/connections/${edit!.id}`, edit);
      else await post("/connections", edit);
      setEdit(null);
      setTest(null);
      load();
    } catch (err: any) {
      setError(err.message);
    }
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await post<TestResult>("/connections/test", edit));
      load();
    } catch (err: any) {
      setTest({ ok: false, error: err.message, databases: [], warnings: [] });
    } finally {
      setTesting(false);
    }
  };

  const remove = async (c: Connection) => {
    if (!confirm(`Delete connection ${c.name}? Existing backups stay restorable to other targets.`)) return;
    try { await del(`/connections/${c.id}`); load(); } catch (err: any) { setError(err.message); }
  };

  return (
    <>
      <h1>Connections <button className="primary" onClick={() => { setEdit(empty()); setTest(null); }}>Add connection</button></h1>
      {error && <p className="error">{error}</p>}
      {edit && (
        <form onSubmit={save} style={{ marginBottom: "1.5rem" }}>
          <div className="row">
            <label>Name<input value={edit.name} onChange={(e) => set({ name: e.target.value })} required maxLength={64} /></label>
            <label>Engine
              <select value={edit.engine} onChange={(e) => changeEngine(e.target.value as Engine)}>
                <option value="postgres">PostgreSQL</option>
                <option value="mysql">MySQL</option>
                <option value="mssql">SQL Server</option>
              </select>
            </label>
            <label>Host<input value={edit.host} onChange={(e) => set({ host: e.target.value })} required /></label>
            <label>Port<input type="number" min={1} max={65535} value={edit.port} onChange={(e) => set({ port: Number(e.target.value) })} required /></label>
          </div>
          <div className="row">
            <label>Username<input value={edit.username} onChange={(e) => set({ username: e.target.value })} required autoComplete="off" /></label>
            <label>Password
              <input type="password" value={edit.password ?? ""} onChange={(e) => set({ password: e.target.value })} autoComplete="new-password"
                placeholder={edit.password_set ? "leave empty to keep" : ""} />
            </label>
            <label>TLS
              <select value={edit.tls_mode} onChange={(e) => set({ tls_mode: e.target.value })}>
                {tlsModes[edit.engine].map(([v, l]) => <option key={v} value={v}>{l}</option>)}
              </select>
            </label>
          </div>
          {edit.engine === "mssql" && (
            <div className="row">
              <label>Share path as seen by SQL Server<input value={edit.share_path_server} onChange={(e) => set({ share_path_server: e.target.value })} placeholder="/var/opt/mssql/share or D:\share" required /></label>
              <label>Same share as seen by Dumpling<input value={edit.share_path_local} onChange={(e) => set({ share_path_local: e.target.value })} placeholder="/mnt/mssql-share" required /></label>
            </div>
          )}
          {test && (
            <div className={test.ok ? "card" : "card error"}>
              {test.ok ? <>✅ Connected — server {test.server_version}. Databases: {(test.databases ?? []).join(", ") || "none"}</> : <>❌ {test.error}</>}
              {(test.warnings ?? []).map((w) => <div key={w} className="warn">{w}</div>)}
            </div>
          )}
          <div className="actions">
            <button className="primary">Save</button>
            <button type="button" onClick={runTest} disabled={testing}>{testing ? "Testing…" : "Test connection"}</button>
            <button type="button" onClick={() => setEdit(null)}>Cancel</button>
          </div>
        </form>
      )}
      <table>
        <thead><tr><th>Name</th><th>Engine</th><th>Host</th><th>Version</th><th>Last test</th><th /></tr></thead>
        <tbody>
          {list.map((c) => (
            <tr key={c.id}>
              <td>{c.name}</td><td>{c.engine}</td><td>{c.host}:{c.port}</td><td>{c.server_version || "–"}</td><td>{when(c.tested_at)}</td>
              <td className="actions">
                <button onClick={() => { setEdit({ ...c, password: "" }); setTest(null); }}>Edit</button>
                <button onClick={() => remove(c)}>Delete</button>
              </td>
            </tr>
          ))}
          {!list.length && <tr><td colSpan={6} className="muted">No connections yet.</td></tr>}
        </tbody>
      </table>
    </>
  );
}
