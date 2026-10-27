import { useEffect, useState } from "react";
import { Link } from "react-router";
import { get, when } from "../api";
import { useEvents } from "../events";
import type { Connection, Operation, Plan } from "../types";

const types = ["backup", "restore", "prune", "verify"];
const statuses = ["pending", "running", "success", "warning", "failed", "interrupted", "skipped"];

export default function Operations() {
  const [ops, setOps] = useState<Operation[]>([]);
  const [more, setMore] = useState(true);
  const [filter, setFilter] = useState({ type: "", status: "", plan_id: "", connection_id: "" });
  const [plans, setPlans] = useState<Plan[]>([]);
  const [conns, setConns] = useState<Connection[]>([]);
  const [error, setError] = useState("");

  const query = (before?: string) => {
    const q = new URLSearchParams(Object.entries(filter).filter(([, v]) => v));
    q.set("limit", "50");
    if (before) q.set("before", before);
    return get<Operation[]>("/operations?" + q.toString());
  };

  useEffect(() => {
    query().then((o) => { setOps(o); setMore(o.length === 50); }).catch((e) => setError(e.message));
  }, [filter]);
  useEffect(() => {
    get<Plan[]>("/plans").then(setPlans);
    get<Connection[]>("/connections").then(setConns);
  }, []);

  const matches = (o: Operation) =>
    (!filter.type || o.type === filter.type) && (!filter.status || o.status === filter.status) &&
    (!filter.plan_id || o.plan_id === filter.plan_id) &&
    (!filter.connection_id || o.connection_id === filter.connection_id || o.target_connection_id === filter.connection_id);

  useEvents({
    operation: (o) => setOps((list) => {
      const i = list.findIndex((x) => x.id === o.id);
      if (i >= 0) return matches(o) ? list.map((x) => (x.id === o.id ? o : x)) : list.filter((x) => x.id !== o.id);
      return matches(o) ? [o, ...list] : list;
    }),
  });

  const loadMore = () => query(ops[ops.length - 1]?.id).then((o) => { setOps([...ops, ...o]); setMore(o.length === 50); });
  const name = (id: string) => conns.find((c) => c.id === id)?.name ?? (id ? "(deleted)" : "");
  const sel = (k: keyof typeof filter, name: string, label: string, opts: [string, string][]) => (
    <select aria-label={name} value={filter[k]} onChange={(e) => setFilter({ ...filter, [k]: e.target.value })}>
      <option value="">{label}</option>
      {opts.map(([v, l]) => <option key={v} value={v}>{l}</option>)}
    </select>
  );

  return (
    <>
      <h1>Operations</h1>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="filters">
        {sel("type", "Type", "All types", types.map((t) => [t, t]))}
        {sel("status", "Status", "All statuses", statuses.map((s) => [s, s]))}
        {sel("plan_id", "Plan", "All plans", plans.map((p) => [p.id, p.name]))}
        {sel("connection_id", "Connection", "All connections", conns.map((c) => [c.id, c.name]))}
      </div>
      <table>
        <thead><tr><th>Created</th><th>Type</th><th>Status</th><th>Subject</th><th>Message</th></tr></thead>
        <tbody>
          {ops.map((o) => (
            <tr key={o.id}>
              <td data-label="Created"><Link to={`/operations/${o.id}`}>{when(o.created_at)}</Link></td>
              <td data-label="Type">{o.type}</td>
              <td data-label="Status"><span className={`status ${o.status}`}>{o.status}</span></td>
              <td data-label="Subject">{name(o.connection_id)}{o.database && ` / ${o.database}`}{o.type === "restore" && ` → ${name(o.target_connection_id)} / ${o.target_database}`}</td>
              <td data-label="Message" className="muted">{o.message}</td>
            </tr>
          ))}
          {!ops.length && <tr><td colSpan={5} className="muted">No operations.</td></tr>}
        </tbody>
      </table>
      {more && ops.length > 0 && <p><button onClick={loadMore}>Load more</button></p>}
    </>
  );
}
