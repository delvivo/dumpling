import { useEffect, useState } from "react";
import { Link } from "react-router";
import { del, get, post, put, when } from "../api";
import { fromCron, toCron, type Preset } from "../cronPresets";
import type { Connection, Destination, Plan } from "../types";

const days = ["Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"];

const empty = (): Plan => ({
  id: "", name: "", connection_id: "", databases: [], destination_id: "", schedule: "0 2 * * *",
  retention: { last: 7, daily: 0, weekly: 4, monthly: 6 }, encrypted: false, paused: false,
});

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

  const remove = (p: Plan) => {
    const extra = p.encrypted ? " Its stored passphrase is deleted too: restoring its encrypted backups will then require typing the passphrase." : "";
    if (confirm(`Delete plan ${p.name}? Existing backups are kept.${extra}`)) act(() => del(`/plans/${p.id}`));
  };

  return (
    <>
      <h1>Plans <button className="primary" onClick={() => setEdit({ ...empty(), connection_id: conns[0]?.id ?? "", destination_id: dests[0]?.id ?? "" })} disabled={!conns.length || !dests.length}>Add plan</button></h1>
      {error && <p className="error" role="alert">{error}</p>}
      {edit && <PlanForm plan={edit} conns={conns} dests={dests} onDone={() => { setEdit(null); load(); }} onCancel={() => setEdit(null)} />}
      <table>
        <thead><tr><th>Name</th><th>Source</th><th>Schedule</th><th>Next run</th><th>Last run</th><th /></tr></thead>
        <tbody>
          {list.map((p) => (
            <tr key={p.id}>
              <td data-label="Name">{p.name}{p.encrypted && " 🔒"}{p.paused && <span className="status skipped"> paused</span>}</td>
              <td data-label="Source">{conns.find((c) => c.id === p.connection_id)?.name} / {p.databases.join(", ")}</td>
              <td data-label="Schedule"><code>{p.schedule}</code></td>
              <td data-label="Next run">{p.paused ? "–" : when(p.next_run)}</td>
              <td data-label="Last run">{p.last_operation ? <Link to={`/operations/${p.last_operation.id}`}><span className={`status ${p.last_operation.status}`}>{p.last_operation.status}</span></Link> : "–"}</td>
              <td className="actions">
                <button onClick={() => act(() => post(`/plans/${p.id}/run`))}>Run now</button>
                <button onClick={() => act(() => post(`/plans/${p.id}/${p.paused ? "resume" : "pause"}`))}>{p.paused ? "Resume" : "Pause"}</button>
                <button onClick={() => setEdit(p)}>Edit</button>
                <button className="subtle-danger" onClick={() => remove(p)}>Delete</button>
              </td>
            </tr>
          ))}
          {!list.length && <tr><td colSpan={6} className="muted">No plans yet.</td></tr>}
        </tbody>
      </table>
    </>
  );
}

function PlanForm({ plan, conns, dests, onDone, onCancel }: { plan: Plan; conns: Connection[]; dests: Destination[]; onDone: () => void; onCancel: () => void }) {
  const [p, setP] = useState(plan);
  const [preset, setPreset] = useState<Preset>(fromCron(plan.schedule));
  const [dbs, setDbs] = useState<string[]>(plan.databases);
  const [encMode, setEncMode] = useState<"keep" | "set" | "none">(plan.encrypted ? "keep" : "none");
  const [passphrase, setPassphrase] = useState("");
  const [repeat, setRepeat] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const mismatch = encMode === "set" && repeat !== passphrase;

  useEffect(() => {
    if (!p.connection_id) return;
    get<string[]>(`/connections/${p.connection_id}/databases`)
      .then((list) => setDbs([...new Set([...list, ...p.databases])]))
      .catch((e) => setError(`Cannot list databases: ${e.message}`));
  }, [p.connection_id]);

  const setRet = (k: keyof Plan["retention"], v: number) => setP({ ...p, retention: { ...p.retention, [k]: v } });

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const body: Record<string, unknown> = {
      name: p.name, connection_id: p.connection_id, databases: p.databases, destination_id: p.destination_id,
      schedule: toCron(preset), retention: p.retention, paused: p.paused,
    };
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
      <fieldset>
        <legend>Databases (one backup each)</legend>
        {dbs.map((d) => (
          <label key={d} className="inline">
            <input type="checkbox" checked={p.databases.includes(d)} onChange={(e) => setP({ ...p, databases: e.target.checked ? [...p.databases, d] : p.databases.filter((x) => x !== d) })} />{d}
          </label>
        ))}
      </fieldset>
      <fieldset className="row">
        <legend>Schedule (server time unless CRON_TZ= prefix)</legend>
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
        <button className="primary" disabled={!p.databases.length || mismatch || busy}>{busy ? "Saving…" : "Save plan"}</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}
