import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { ApiError, get, post } from "../api";
import { canRestore } from "../restoreGate";
import type { Backup, Connection } from "../types";

type Preflight = { target_exists: boolean; version_warning?: string; passphrase_required: boolean };

export default function RestoreWizard({ backup, onClose }: { backup: Backup; onClose: () => void }) {
  const navigate = useNavigate();
  const [targets, setTargets] = useState<Connection[]>([]);
  const [targetId, setTargetId] = useState("");
  const [database, setDatabase] = useState(backup.database);
  const [pf, setPf] = useState<Preflight | null>(null);
  const [typed, setTyped] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [skipOwnership, setSkipOwnership] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    get<Connection[]>("/connections").then((cs) => {
      const same = cs.filter((c) => c.engine === backup.engine); // only same-engine targets (FR-017)
      setTargets(same);
      setTargetId(same.find((c) => c.id === backup.connection_id)?.id ?? same[0]?.id ?? "");
    });
  }, [backup]);

  const body = () => ({ backup_id: backup.id, target_connection_id: targetId, target_database: database.trim(), passphrase, skip_ownership: skipOwnership });

  useEffect(() => {
    setPf(null);
    setTyped("");
    setError("");
    if (!targetId || !database.trim()) return;
    const t = setTimeout(() => {
      post<Preflight>("/restores/preflight", body()).then(setPf).catch((e) => setError(e.message));
    }, 300);
    return () => clearTimeout(t);
  }, [targetId, database]);

  const ok = pf !== null && canRestore({
    targetDatabase: database, targetExists: pf.target_exists, typedConfirmation: typed,
    passphraseRequired: pf.passphrase_required, passphrase,
  });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    try {
      const r = await post<{ operation_id: string }>("/restores", { ...body(), confirm_database: typed });
      navigate(`/operations/${r.operation_id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <dialog open>
      <form onSubmit={submit}>
        <h2>Restore backup</h2>
        <p className="muted">{backup.connection_name} / {backup.database} — {new Date(backup.started_at).toLocaleString()} ({backup.engine} {backup.server_version})</p>
        <label>Target connection ({backup.engine} only)
          <select value={targetId} onChange={(e) => setTargetId(e.target.value)}>
            {targets.map((c) => <option key={c.id} value={c.id}>{c.name} — {c.host}:{c.port}</option>)}
          </select>
        </label>
        {!targets.length && <p className="error">No {backup.engine} connection available as target.</p>}
        <label>Target database name<input value={database} onChange={(e) => setDatabase(e.target.value)} required /></label>
        {pf?.version_warning && <p className="warn">{pf.version_warning}</p>}
        {pf?.passphrase_required && (
          <label>Backup passphrase<input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} required /></label>
        )}
        {backup.engine !== "mssql" && (
          <label className="inline"><input type="checkbox" checked={skipOwnership} onChange={(e) => setSkipOwnership(e.target.checked)} />
            Skip ownership ({backup.engine === "postgres" ? "--no-owner --no-acl" : "strip DEFINER clauses"}) — use when target lacks the original roles/users
          </label>
        )}
        {pf?.target_exists ? (
          <div className="warn">
            <p><strong>Database “{database}” already exists.</strong> Active connections to it will be terminated, and it will be
              <strong> dropped and recreated</strong> from this backup. This cannot be undone.</p>
            <label>Type <code>{database}</code> to confirm<input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" /></label>
          </div>
        ) : pf && <p className="muted">A new database “{database}” will be created.</p>}
        {error && <p className="error">{error}</p>}
        <div className="actions">
          <button className={pf?.target_exists ? "danger" : "primary"} disabled={!ok || busy}>
            {pf?.target_exists ? "Overwrite and restore" : "Restore"}
          </button>
          <button type="button" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </dialog>
  );
}
