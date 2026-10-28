import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { ApiError, get, post, when } from "../api";
import Modal from "../Modal";
import { canRestore } from "../restoreGate";
import { dbLabel, type Backup, type Connection, type Preflight } from "../types";

export default function RestoreWizard({ backup, onClose }: { backup: Backup; onClose: () => void }) {
  const navigate = useNavigate();
  // PostgreSQL chain backups are physical: they rebuild a data directory instead of a database
  const physical = backup.engine === "postgres" && !!backup.chain_id;
  const [targets, setTargets] = useState<Connection[]>([]);
  const [targetId, setTargetId] = useState("");
  const [database, setDatabase] = useState(physical ? `${backup.connection_name}-${backup.started_at.slice(0, 10)}` : backup.database);
  const [pf, setPf] = useState<Preflight | null>(null);
  const [typed, setTyped] = useState("");
  const [passphrase, setPassphrase] = useState("");
  const [skipOwnership, setSkipOwnership] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (physical) return;
    get<Connection[]>("/connections").then((cs) => {
      const same = cs.filter((c) => c.engine === backup.engine); // only same-engine targets (FR-017)
      setTargets(same);
      setTargetId(same.find((c) => c.id === backup.connection_id)?.id ?? same[0]?.id ?? "");
    });
  }, [backup]);

  const body = () => physical
    ? { backup_id: backup.id, target_path: database.trim(), passphrase }
    : { backup_id: backup.id, target_connection_id: targetId, target_database: database.trim(), passphrase, skip_ownership: skipOwnership };

  useEffect(() => {
    setPf(null);
    setTyped("");
    setError("");
    if ((!physical && !targetId) || !database.trim()) return;
    const t = setTimeout(() => {
      post<Preflight>("/restores/preflight", body()).then(setPf).catch((e) => setError(e.message));
    }, 300);
    return () => clearTimeout(t);
  }, [targetId, database]);

  const ok = pf !== null && canRestore({
    targetDatabase: database, targetExists: pf.target_exists, typedConfirmation: typed,
    passphraseRequired: pf.passphrase_required, passphrase,
    chainError: pf.chain_error, physical, targetPathOk: pf.target_path_ok,
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

  const chain = pf?.chain ?? [];
  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h2>Restore backup</h2>
        <p className="muted">{backup.connection_name} / {dbLabel(backup.database)} — {new Date(backup.started_at).toLocaleString()} ({backup.engine} {backup.server_version})</p>
        {chain.length > 1 && (
          <div>
            <p>This is an incremental backup. These {chain.length} backups are applied, in order:</p>
            <ol>
              {chain.map((l) => (
                <li key={l.id}>{l.kind === "full" ? "Full" : `Incremental #${l.seq}`} — {when(l.started_at)} <span className={`status ${l.status}`}>{l.status}</span></li>
              ))}
            </ol>
          </div>
        )}
        {pf?.chain_error && <p className="error" role="alert">Cannot restore: {pf.chain_error}. A backup this one depends on is missing or corrupted.</p>}
        {backup.destination_type && backup.destination_type !== "local" && (
          <p className="muted">The {chain.length > 1 ? "artifacts are" : "artifact is"} downloaded once from the destination to temporary files and checksummed before the target is touched.</p>
        )}
        {physical ? (
          <>
            <label>Folder name inside Dumpling's restore folder<input value={database} onChange={(e) => setDatabase(e.target.value)} required /></label>
            <p className="muted">The data directory of the entire server is rebuilt in a new or empty folder. Afterwards, give it to the PostgreSQL server user (mode 0700) and start PostgreSQL {backup.server_version.split(".")[0]} on it.</p>
            {pf && !pf.target_path_ok && <p className="error" role="alert">Choose a new or empty folder inside the restore folder.</p>}
          </>
        ) : (
          <>
            <label>Target connection ({backup.engine} only)
              <select value={targetId} onChange={(e) => setTargetId(e.target.value)}>
                {targets.map((c) => <option key={c.id} value={c.id}>{c.name} — {c.host}:{c.port}</option>)}
              </select>
            </label>
            {!targets.length && <p className="error" role="alert">No {backup.engine} connection available as target.</p>}
            <label>Target database name<input value={database} onChange={(e) => setDatabase(e.target.value)} required /></label>
          </>
        )}
        {pf?.version_warning && <p className="warn">{pf.version_warning}</p>}
        {pf?.passphrase_required && (
          <label>Backup passphrase{backup.locked && " (imported backup — used for this restore only, use Unlock to store it)"}
            {pf.passphrase_link && pf.passphrase_link !== backup.id && " (needed by an earlier backup of the chain)"}
            <input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} required /></label>
        )}
        {!physical && backup.engine !== "mssql" && (
          <label className="inline"><input type="checkbox" checked={skipOwnership} onChange={(e) => setSkipOwnership(e.target.checked)} />
            Skip ownership ({backup.engine === "postgres" ? "--no-owner --no-acl" : "strip DEFINER clauses"}) — use when target lacks the original roles/users
          </label>
        )}
        {!physical && (pf?.target_exists ? (
          <div className="warn">
            <p><strong>Database “{database}” already exists.</strong> Active connections to it will be terminated, and it will be
              <strong> dropped and recreated</strong> from this backup. This cannot be undone.</p>
            <label>Type <code>{database}</code> to confirm<input value={typed} onChange={(e) => setTyped(e.target.value)} autoComplete="off" /></label>
          </div>
        ) : pf && <p className="muted">A new database “{database}” will be created.</p>)}
        {error && <p className="error" role="alert">{error}</p>}
        <div className="actions">
          <button className={pf?.target_exists && !physical ? "danger" : "primary"} disabled={!ok || busy}>
            {pf?.target_exists && !physical ? "Overwrite and restore" : "Restore"}
          </button>
          <button type="button" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </Modal>
  );
}
