import { useEffect, useState } from "react";
import { Link } from "react-router";
import { ApiError, bytes, del, get, post, when } from "../api";
import { useEvents } from "../events";
import { dbLabel, type Backup, type Connection, type Destination, type Operation } from "../types";
import Modal, { Confirm } from "../Modal";
import RestoreWizard from "./RestoreWizard";

type Tools = Record<string, { version: string; variant?: string } | null>;
const required: Record<string, string[]> = { postgres: ["psql", "pg_dump", "pg_restore"], mysql: ["mysql", "mysqldump"], mssql: [] };

export default function Backups() {
  const [list, setList] = useState<Backup[]>([]);
  const [conns, setConns] = useState<Connection[]>([]);
  const [dests, setDests] = useState<Destination[]>([]);
  const [tools, setTools] = useState<Tools>({});
  const [filter, setFilter] = useState({ connection_id: "", status: "" });
  const [running, setRunning] = useState<Record<string, Operation>>({});
  const [showNew, setShowNew] = useState(false);
  const [restore, setRestore] = useState<Backup | null>(null);
  const [unlock, setUnlock] = useState<Backup | null>(null);
  const [deleting, setDeleting] = useState<Backup | null>(null);
  const [cascade, setCascade] = useState<{ backup: Backup; typed: string; dependents: number } | null>(null);
  const [error, setError] = useState("");

  const load = () => {
    const q = new URLSearchParams(Object.entries(filter).filter(([, v]) => v)).toString();
    get<Backup[]>("/backups?" + q).then(setList).catch((e) => setError(e.message));
  };
  useEffect(load, [filter]);
  useEffect(() => {
    get<Connection[]>("/connections").then(setConns);
    get<Destination[]>("/destinations").then(setDests);
    get<{ tools: Tools }>("/system").then((s) => setTools(s.tools));
  }, []);

  useEvents({
    operation: (o) => {
      if (o.type !== "backup" && o.type !== "verify" && o.type !== "prune") return;
      setRunning((r) => {
        const n = { ...r };
        if (o.status === "pending" || o.status === "running") n[o.id] = o;
        else delete n[o.id];
        return n;
      });
      if (!["pending", "running"].includes(o.status)) load();
    },
  });

  const missing = conns
    .flatMap((c) => required[c.engine])
    .filter((t, i, a) => a.indexOf(t) === i)
    .filter((t) => !tools[t] || tools[t]?.variant === "mariadb");

  const verify = async (b: Backup) => {
    try { await post(`/backups/${b.id}/verify`); } catch (err: any) { setError(err.message); }
  };
  const remove = async (b: Backup, typed: string, withDependents = false) => {
    try {
      await del(`/backups/${b.id}`, { confirm: typed, cascade: withDependents });
      load();
    } catch (err: any) {
      // incrementals depend on it: ask again, naming how many go with it (FR-013)
      if (err instanceof ApiError && err.code === "has_dependents") setCascade({ backup: b, typed, dependents: err.body?.dependents ?? 0 });
      else setError(err.message);
    }
  };
  const kindLabel = (b: Backup) => (b.kind === "incr" ? `Incremental #${b.seq}` : b.chain_id ? "Full (chain base)" : "Full");
  const base = (b: Backup) => list.find((x) => x.id === b.chain_id);

  return (
    <>
      <h1>Backups <button className="primary" onClick={() => setShowNew(true)} disabled={!conns.length || !dests.length}>Backup now</button></h1>
      {missing.length > 0 && <p className="warn banner">Missing or unsupported client tools on this host: {missing.join(", ")}. Backups for those engines will fail.</p>}
      {(!conns.length || !dests.length) && <p className="muted">Add a <Link to="/connections">connection</Link> and a <Link to="/destinations">destination</Link> first.</p>}
      {error && <p className="error" role="alert">{error}</p>}
      <div aria-live="polite">
        {Object.values(running).map((o) => (
          <p key={o.id} className="card">⏳ {o.type} {o.database} — <span className={`status ${o.status}`}>{o.status}</span> <Link to={`/operations/${o.id}`}>log</Link></p>
        ))}
      </div>
      <div className="filters">
        <select aria-label="Connection" value={filter.connection_id} onChange={(e) => setFilter({ ...filter, connection_id: e.target.value })}>
          <option value="">All connections</option>
          {conns.map((c) => <option key={c.id} value={c.id}>{c.name}</option>)}
        </select>
        <select aria-label="Status" value={filter.status} onChange={(e) => setFilter({ ...filter, status: e.target.value })}>
          <option value="">ok + corrupted</option>
          <option value="ok">ok</option>
          <option value="corrupted">corrupted</option>
          <option value="deleted">deleted</option>
        </select>
      </div>
      <table role="table">
        <thead role="rowgroup"><tr role="row"><th role="columnheader">Time</th><th role="columnheader">Connection / database</th><th role="columnheader">Plan</th><th role="columnheader">Type</th><th role="columnheader">Destination</th><th role="columnheader">Size</th><th role="columnheader">SHA-256</th><th role="columnheader">Status</th><th role="columnheader" aria-label="Actions" /></tr></thead>
        <tbody role="rowgroup">
          {list.map((b) => (
            <tr role="row" key={b.id}>
              <td role="cell" data-label="Time">{when(b.started_at)}</td>
              <td role="cell" data-label="Database">{b.connection_name} / <strong>{dbLabel(b.database)}</strong><div className="muted">{b.engine} {b.server_version}</div></td>
              <td role="cell" data-label="Plan">{b.plan_name || (b.imported ? "–" : "on-demand")}{b.imported && <div><span className="status">imported</span></div>}</td>
              <td role="cell" data-label="Type">
                {kindLabel(b)}
                {b.kind === "incr" && <div className="muted">base {base(b) ? when(base(b)!.started_at) : <code title={b.chain_id}>{b.chain_id.slice(-6)}</code>}</div>}
                {!b.chain_ok && <div><span className="status failed" title="A backup this one depends on is missing or corrupted: it cannot be restored">broken chain</span></div>}
              </td>
              <td role="cell" data-label="Destination"><span className="status">{b.destination_type}</span> <code className="muted" title={b.location}>{b.location}</code></td>
              <td role="cell" data-label="Size">{bytes(b.size_bytes)}{b.encrypted && <span role="img" aria-label="encrypted" title="encrypted"> 🔒</span>}{b.locked && <div><span className="status warning">locked</span></div>}</td>
              <td role="cell" data-label="SHA-256"><code title={b.sha256}>{b.sha256.slice(0, 12)}…</code></td>
              <td role="cell" data-label="Status"><span className={`status ${b.status}`}>{b.status}</span></td>
              <td role="cell" className="actions">
                {b.status === "ok" && b.locked && <button onClick={() => setUnlock(b)}>Unlock</button>}
                {b.status === "ok" && <button onClick={() => setRestore(b)} disabled={!b.chain_ok} title={b.chain_ok ? undefined : "Broken chain: a backup this one depends on is missing or corrupted"}>Restore</button>}
                {b.status !== "deleted" && <button onClick={() => verify(b)}>Verify</button>}
                {b.status !== "deleted" && <button className="subtle-danger" onClick={() => setDeleting(b)}>Delete</button>}
              </td>
            </tr>
          ))}
          {!list.length && <tr role="row"><td role="cell" colSpan={9} className="muted">No backups.</td></tr>}
        </tbody>
      </table>
      {showNew && <NewBackup conns={conns} dests={dests} onClose={() => setShowNew(false)} />}
      {restore && <RestoreWizard backup={restore} onClose={() => setRestore(null)} />}
      {unlock && <UnlockDialog backup={unlock} onClose={() => { setUnlock(null); load(); }} />}
      {deleting && (
        <Confirm title="Delete this backup permanently?" confirmLabel="Delete backup" typeToConfirm={deleting.database} onConfirm={(typed) => remove(deleting, typed)} onClose={() => setDeleting(null)}>
          <p className="muted">{deleting.connection_name} / {dbLabel(deleting.database)} — {when(deleting.started_at)}</p>
        </Confirm>
      )}
      {cascade && (
        <Confirm title="Delete this backup and the incrementals built on it?" confirmLabel={`Delete ${cascade.dependents + 1} backups`}
          onConfirm={() => remove(cascade.backup, cascade.typed, true)} onClose={() => setCascade(null)}>
          <p>{cascade.dependents} incremental backup(s) depend on this one and cannot be restored without it: they are deleted too.</p>
        </Confirm>
      )}
    </>
  );
}

// UnlockDialog stores the passphrase of an imported encrypted backup (validated server-side by
// decrypting the artifact header), optionally for every locked backup of the same origin.
function UnlockDialog({ backup, onClose }: { backup: Backup; onClose: () => void }) {
  const [passphrase, setPassphrase] = useState("");
  const [all, setAll] = useState(true);
  const [error, setError] = useState("");
  const [done, setDone] = useState<number | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(""); setBusy(true);
    try {
      const r = await post<{ unlocked: string[] }>(`/backups/${backup.id}/unlock`, { passphrase, apply_to_origin: all });
      setDone(r.unlocked.length);
    } catch (err: any) {
      setError(err.code === "wrong_passphrase" ? "Wrong passphrase." : err.message);
    } finally { setBusy(false); }
  };

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h2>Unlock imported backup</h2>
        <p className="muted">{backup.connection_name || "?"} / {backup.database} — {new Date(backup.started_at).toLocaleString()}{backup.plan_name && ` (plan ${backup.plan_name})`}</p>
        {done === null ? (
          <>
            <label>Passphrase<input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} required autoComplete="off" /></label>
            <label className="inline"><input type="checkbox" checked={all} onChange={(e) => setAll(e.target.checked)} /> Apply to all locked backups of the same origin</label>
            {error && <p className="error" role="alert">{error}</p>}
            <div className="actions">
              <button className="primary" disabled={busy}>Unlock</button>
              <button type="button" onClick={onClose}>Cancel</button>
            </div>
          </>
        ) : (
          <>
            <p>{done} backup(s) unlocked. The passphrase is stored encrypted.</p>
            <div className="actions"><button type="button" className="primary" onClick={onClose}>Close</button></div>
          </>
        )}
      </form>
    </Modal>
  );
}

function NewBackup({ conns, dests, onClose }: { conns: Connection[]; dests: Destination[]; onClose: () => void }) {
  const [connId, setConnId] = useState(conns[0]?.id ?? "");
  const [destId, setDestId] = useState(dests[0]?.id ?? "");
  const [dbs, setDbs] = useState<string[] | null>(null);
  const [picked, setPicked] = useState<string[]>([]);
  const [encrypt, setEncrypt] = useState(false);
  const [passphrase, setPassphrase] = useState("");
  const [repeat, setRepeat] = useState("");
  const [err, setErr] = useState("");
  const [submitErr, setSubmitErr] = useState("");
  const [busy, setBusy] = useState(false);
  const mismatch = encrypt && repeat !== passphrase;

  useEffect(() => {
    setDbs(null);
    setPicked([]);
    setErr("");
    if (connId) get<string[]>(`/connections/${connId}/databases`).then(setDbs).catch((e) => { setErr(e.message); setDbs([]); });
  }, [connId]);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setSubmitErr(""); setBusy(true);
    try {
      await post("/backups", { connection_id: connId, databases: picked, destination_id: destId, passphrase: encrypt ? passphrase : "" });
      onClose();
    } catch (er: any) {
      setSubmitErr(er.message);
      setBusy(false);
    }
  };

  return (
    <Modal onClose={onClose}>
      <form onSubmit={submit}>
        <h2>Backup now</h2>
        <label>Connection<select value={connId} onChange={(e) => setConnId(e.target.value)}>{conns.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.engine})</option>)}</select></label>
        <fieldset>
          <legend>Databases</legend>
          {dbs === null && <span className="muted">Loading…</span>}
          {err && <span className="error" role="alert">{err}</span>}
          {dbs?.map((d) => (
            <label key={d} className="inline">
              <input type="checkbox" checked={picked.includes(d)} onChange={(e) => setPicked(e.target.checked ? [...picked, d] : picked.filter((x) => x !== d))} />{d}
            </label>
          ))}
        </fieldset>
        <label>Destination<select value={destId} onChange={(e) => setDestId(e.target.value)}>{dests.map((d) => <option key={d.id} value={d.id}>{d.name} ({d.type})</option>)}</select></label>
        <label className="inline"><input type="checkbox" checked={encrypt} onChange={(e) => setEncrypt(e.target.checked)} /> Encrypt with passphrase</label>
        {encrypt && (
          <>
            <div className="row">
              <label>Passphrase<input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} required autoComplete="new-password" /></label>
              <label>Repeat passphrase<input type="password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required autoComplete="new-password" /></label>
            </div>
            {mismatch && repeat && <p className="error" role="alert">Passphrases don't match.</p>}
            <p className="warn">The passphrase is not stored for on-demand backups. Losing it makes the backup unrecoverable.</p>
          </>
        )}
        {submitErr && <p className="error" role="alert">{submitErr}</p>}
        <div className="actions">
          <button className="primary" disabled={!picked.length || mismatch || busy}>{busy ? "Starting…" : "Start backup"}</button>
          <button type="button" onClick={onClose}>Cancel</button>
        </div>
      </form>
    </Modal>
  );
}
