import { useEffect, useState } from "react";
import { Link } from "react-router";
import { del, get, post, put, when } from "../api";
import { fromCron, toCron, type Preset } from "../cronPresets";
import { CLUSTER_DB, dbLabel, type Connection, type Destination, type Engine, type Plan } from "../types";
import { Confirm } from "../Modal";

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

const empty = (): Plan => ({
  id: "", name: "", connection_id: "", databases: [], destination_id: "", schedule: "0 2 * * *",
  retention: { last: 7, daily: 0, weekly: 4, monthly: 6 }, encrypted: false, paused: false,
  incr_schedule: "", max_chain: 30,
});

// What incremental mode costs, per engine; SQL Server and MySQL need an explicit acknowledgment.
const incrRisk: Partial<Record<Engine, string>> = {
  mssql: "Full backups of this plan are taken without COPY_ONLY: each one becomes the differential base for every backup tool on these databases, so differentials taken by other tools will build on Dumpling's fulls.",
  mysql: "Each full backup briefly holds a global read lock at its start to record the binary log position. Dumpling skips the full (warning, retried next run) while writes have been running for over 10 seconds, and stops it if the lock is not acquired within 5 seconds. Requires binary logging; with statement-based logging, statements touching other databases may be replayed incorrectly.",
};
const incrNote: Partial<Record<Engine, string>> = {
  postgres: "Backs up the entire server (all databases) as physical base backups; requires PostgreSQL 17+, summarize_wal = on and a role with REPLICATION. Restores rebuild a data directory inside Dumpling's restore folder, on which you start a PostgreSQL server of the same major version.",
};

export default function Plans() {
  const [list, setList] = useState<Plan[]>([]);
  const [conns, setConns] = useState<Connection[]>([]);
  const [dests, setDests] = useState<Destination[]>([]);
  const [edit, setEdit] = useState<Plan | null>(null);
  const [error, setError] = useState("");

  const load = () => get<Plan[]>("/plans").then(setList).catch((e) => setError(e.message));
  useEffect(() => {
    load();
    get<Connection[]>("/connections").then(setConns);
    get<Destination[]>("/destinations").then(setDests);
  }, []);

  const act = async (fn: () => Promise<unknown>) => {
    setError("");
    try { await fn(); load(); } catch (err: any) { setError(err.message); }
  };

  const [deleting, setDeleting] = useState<Plan | null>(null);

  return (
    <>
      <h1>Plans <button className="primary" onClick={() => setEdit({ ...empty(), connection_id: conns[0]?.id ?? "", destination_id: dests[0]?.id ?? "" })} disabled={!conns.length || !dests.length}>Add plan</button></h1>
      {deleting && (
        <Confirm title={`Delete plan ${deleting.name}?`} confirmLabel="Delete plan" onConfirm={() => act(() => del(`/plans/${deleting.id}`))} onClose={() => setDeleting(null)}>
          Existing backups are kept.{deleting.encrypted && " Its stored passphrase is deleted too: restoring its encrypted backups will then require typing the passphrase."}
        </Confirm>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      {edit && <PlanForm plan={edit} conns={conns} dests={dests} onDone={() => { setEdit(null); load(); }} onCancel={() => setEdit(null)} />}
      <table role="table">
        <thead role="rowgroup"><tr role="row"><th role="columnheader">Name</th><th role="columnheader">Source</th><th role="columnheader">Schedule</th><th role="columnheader">Next run</th><th role="columnheader">Last run</th><th role="columnheader" aria-label="Actions" /></tr></thead>
        <tbody role="rowgroup">
          {list.map((p) => (
            <tr role="row" key={p.id}>
              <td role="cell" data-label="Name">{p.name}{p.encrypted && <span role="img" aria-label="encrypted" title="encrypted"> 🔒</span>}{p.paused && <span className="status skipped"> paused</span>}</td>
              <td role="cell" data-label="Source">{conns.find((c) => c.id === p.connection_id)?.name} / {p.databases.map(dbLabel).join(", ")}</td>
              <td role="cell" data-label="Schedule">
                <code>{p.schedule}</code>
                {p.incr_schedule && <> <span className="muted">full</span><br /><code>{p.incr_schedule}</code> <span className="muted">incremental</span></>}
              </td>
              <td role="cell" data-label="Next run">
                {p.paused ? "–" : <>{when(p.next_run)}{p.next_incr_run && <><br />{when(p.next_incr_run)} <span className="muted">incremental</span></>}</>}
              </td>
              <td role="cell" data-label="Last run">{p.last_operation ? <Link to={`/operations/${p.last_operation.id}`}><span className={`status ${p.last_operation.status}`}>{p.last_operation.status}</span></Link> : "–"}</td>
              <td role="cell" className="actions">
                <button onClick={() => act(() => post(`/plans/${p.id}/run`))}>{p.incr_schedule ? "Run full now" : "Run now"}</button>
                {p.incr_schedule && <button onClick={() => act(() => post(`/plans/${p.id}/run?kind=incremental`))}>Run incremental now</button>}
                <button onClick={() => act(() => post(`/plans/${p.id}/${p.paused ? "resume" : "pause"}`))}>{p.paused ? "Resume" : "Pause"}</button>
                <button onClick={() => setEdit(p)}>Edit</button>
                <button className="subtle-danger" onClick={() => setDeleting(p)}>Delete</button>
              </td>
            </tr>
          ))}
          {!list.length && <tr role="row"><td role="cell" colSpan={6} className="muted">No plans yet.</td></tr>}
        </tbody>
      </table>
    </>
  );
}

function PlanForm({ plan, conns, dests, onDone, onCancel }: { plan: Plan; conns: Connection[]; dests: Destination[]; onDone: () => void; onCancel: () => void }) {
  const [p, setP] = useState(plan);
  const [preset, setPreset] = useState<Preset>(fromCron(plan.schedule));
  const [incr, setIncr] = useState(plan.incr_schedule !== "");
  const [incrPreset, setIncrPreset] = useState<Preset>(fromCron(plan.incr_schedule || "0 * * * *"));
  const [ack, setAck] = useState(false);
  const [dbs, setDbs] = useState<string[]>(plan.databases.filter((d) => d !== CLUSTER_DB));
  const [encMode, setEncMode] = useState<"keep" | "set" | "none">(plan.encrypted ? "keep" : "none");
  const [passphrase, setPassphrase] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const mismatch = encMode === "set" && repeat !== passphrase;

  useEffect(() => {
    if (!p.connection_id) return;
    get<string[]>(`/connections/${p.connection_id}/databases`)
      .then((list) => setDbs([...new Set([...list, ...p.databases.filter((d) => d !== CLUSTER_DB)])]))
      .catch((e) => setError(`Cannot list databases: ${e.message}`));
  }, [p.connection_id]);

  const setRet = (k: keyof Plan["retention"], v: number) => setP({ ...p, retention: { ...p.retention, [k]: v } });
  const eng = conns.find((c) => c.id === p.connection_id)?.engine;
  const cluster = incr && eng === "postgres"; // physical backups: the whole server, no database choice
  const databases = cluster ? [CLUSTER_DB] : p.databases.filter((d) => d !== CLUSTER_DB);
  // asked again whenever incremental mode is (re)enabled, or the connection changes
  const needsAck = incr && !!eng && !!incrRisk[eng] && !(plan.incr_schedule && plan.connection_id === p.connection_id);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {
      name: p.name, connection_id: p.connection_id, databases, destination_id: p.destination_id,
      schedule: toCron(preset), retention: p.retention, paused: p.paused,
      incr_schedule: incr ? toCron(incrPreset) : "", max_chain: p.max_chain || 30,
    };
    if (needsAck) body.ack_incremental_risk = ack;
    if (encMode === "set") body.passphrase = passphrase;
    if (encMode === "none" && plan.encrypted) body.passphrase = "";
    setBusy(true);
    try {
      if (p.id) await put(`/plans/${p.id}`, body);
      else await post("/plans", body);
      onDone();
    } catch (err: any) {
      setError(err.message);
      setBusy(false);
    }
  };

  const num = (v: string) => Math.max(0, Number(v) || 0);

  return (
    <form onSubmit={submit} style={{ marginBottom: "1.5rem" }}>
      <div className="row">
        <label>Name<input value={p.name} onChange={(e) => setP({ ...p, name: e.target.value })} required maxLength={64} /></label>
        <label>Connection<select value={p.connection_id} onChange={(e) => setP({ ...p, connection_id: e.target.value, databases: [] })}>{conns.map((c) => <option key={c.id} value={c.id}>{c.name} ({c.engine})</option>)}</select></label>
        <label>Destination<select value={p.destination_id} onChange={(e) => setP({ ...p, destination_id: e.target.value })}>{dests.map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}</select></label>
      </div>
      {cluster ? (
        <fieldset>
          <legend>Databases</legend>
          <p>Entire server: incremental PostgreSQL backups are physical and always cover every database.</p>
        </fieldset>
      ) : (
        <fieldset>
          <legend>Databases (one backup each)</legend>
          {dbs.map((d) => (
            <label key={d} className="inline">
              <input type="checkbox" checked={databases.includes(d)} onChange={(e) => setP({ ...p, databases: e.target.checked ? [...databases, d] : databases.filter((x) => x !== d) })} />{d}
            </label>
          ))}
        </fieldset>
      )}
      <ScheduleFields legend={incr ? "Full backup schedule (server time unless CRON_TZ= prefix)" : "Schedule (server time unless CRON_TZ= prefix)"} preset={preset} setPreset={setPreset} />
      <fieldset>
        <legend>Incremental backups</legend>
        <label className="inline"><input type="checkbox" checked={incr} onChange={(e) => { setIncr(e.target.checked); setAck(false); }} /> Take incremental backups between full backups</label>
        {incr && <>
          {eng && incrNote[eng] && <p className="muted">{incrNote[eng]}</p>}
          <ScheduleFields legend="Incremental schedule" preset={incrPreset} setPreset={setIncrPreset} />
          <div className="row">
            <label>Max incrementals per chain<input type="number" min={1} max={1000} value={p.max_chain} onChange={(e) => setP({ ...p, max_chain: Math.min(1000, Math.max(1, Number(e.target.value) || 1)) })} /></label>
          </div>
          <p className="muted">A full backup is forced after this many incrementals. Restoring an incremental also needs the earlier links of its chain; retention never removes a link that a kept backup depends on.</p>
          {needsAck && eng && <>
            <p className="warn">{incrRisk[eng]}</p>
            <label className="inline"><input type="checkbox" checked={ack} onChange={(e) => setAck(e.target.checked)} required /> I understand and want incremental backups for this plan</label>
          </>}
        </>}
      </fieldset>
      <fieldset className="row">
        <legend>Retention (union of rules; newest backup always kept)</legend>
        <label>Keep last<input type="number" min={0} value={p.retention.last} onChange={(e) => setRet("last", num(e.target.value))} /></label>
        <label>Daily<input type="number" min={0} value={p.retention.daily} onChange={(e) => setRet("daily", num(e.target.value))} /></label>
        <label>Weekly<input type="number" min={0} value={p.retention.weekly} onChange={(e) => setRet("weekly", num(e.target.value))} /></label>
        <label>Monthly<input type="number" min={0} value={p.retention.monthly} onChange={(e) => setRet("monthly", num(e.target.value))} /></label>
      </fieldset>
      <fieldset>
        <legend>Encryption</legend>
        {plan.encrypted && <label className="inline"><input type="radio" checked={encMode === "keep"} onChange={() => setEncMode("keep")} /> Keep current passphrase</label>}
        <label className="inline"><input type="radio" checked={encMode === "set"} onChange={() => setEncMode("set")} /> {plan.encrypted ? "Change passphrase (future backups)" : "Encrypt with passphrase"}</label>
        <label className="inline"><input type="radio" checked={encMode === "none"} onChange={() => setEncMode("none")} /> No encryption{plan.encrypted && " (future backups)"}</label>
        {encMode === "set" && <>
          <div className="row">
            <label>Passphrase<input type="password" value={passphrase} onChange={(e) => setPassphrase(e.target.value)} required autoComplete="new-password" /></label>
            <label>Repeat passphrase<input type="password" value={repeat} onChange={(e) => setRepeat(e.target.value)} required autoComplete="new-password" /></label>
          </div>
          {mismatch && repeat && <p className="error" role="alert">Passphrases don't match.</p>}
          <p className="warn">Stored encrypted so scheduled runs work unattended. Keep a copy elsewhere: losing it (e.g. by deleting the plan) makes its backups unrecoverable.</p>
        </>}
      </fieldset>
      <label className="inline"><input type="checkbox" checked={p.paused} onChange={(e) => setP({ ...p, paused: e.target.checked })} /> Paused</label>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="actions">
        <button className="primary" disabled={!databases.length || mismatch || busy || (needsAck && !ack)}>{busy ? "Saving…" : "Save plan"}</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

function ScheduleFields({ legend, preset, setPreset }: { legend: string; preset: Preset; setPreset: (p: Preset) => void }) {
  return (
    <fieldset className="row">
      <legend>{legend}</legend>
      <label>Frequency
        <select value={preset.kind} onChange={(e) => {
          const k = e.target.value;
          setPreset(k === "hourly" ? { kind: "hourly", minute: 0 } : k === "daily" ? { kind: "daily", hour: 2, minute: 0 }
            : k === "weekly" ? { kind: "weekly", weekday: 0, hour: 2, minute: 0 } : { kind: "cron", expr: toCron(preset) });
        }}>
          <option value="hourly">Hourly</option><option value="daily">Daily</option><option value="weekly">Weekly</option><option value="cron">Custom cron</option>
        </select>
      </label>
      {preset.kind === "weekly" && <label>Day<select value={preset.weekday} onChange={(e) => setPreset({ ...preset, weekday: Number(e.target.value) })}>{days.map((d, i) => <option key={d} value={i}>{d}</option>)}</select></label>}
      {(preset.kind === "daily" || preset.kind === "weekly") && <label>Hour<input type="number" min={0} max={23} value={preset.hour} onChange={(e) => setPreset({ ...preset, hour: Number(e.target.value) })} /></label>}
      {preset.kind !== "cron" && <label>Minute<input type="number" min={0} max={59} value={preset.minute} onChange={(e) => setPreset({ ...preset, minute: Number(e.target.value) })} /></label>}
      {preset.kind === "cron" && <label>Cron expression (5 fields)<input value={preset.expr} onChange={(e) => setPreset({ kind: "cron", expr: e.target.value })} required /></label>}
    </fieldset>
  );
}
