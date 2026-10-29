import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router";
import { get, when } from "../api";
import { useEvents } from "../events";
import type { Operation } from "../types";

export default function OperationDetail() {
  const { id } = useParams();
  const [op, setOp] = useState<Operation | null>(null);
  const [log, setLog] = useState("");
  const [error, setError] = useState("");
  const pre = useRef<HTMLPreElement>(null);

  const load = () => {
    get<Operation>(`/operations/${id}`).then(setOp).catch((e) => setError(e.message));
    get<string>(`/operations/${id}/log`).then(setLog);
  };
  useEffect(load, [id]);

  useEvents({
    operation: (o) => {
      if (o.id !== id) return;
      setOp(o);
      if (!["pending", "running"].includes(o.status)) get<string>(`/operations/${id}/log`).then(setLog); // final, complete log
    },
    log: (e) => { if (e.id === id) setLog((l) => l + e.line + "\n"); },
  });

  useEffect(() => { pre.current?.scrollTo(0, pre.current.scrollHeight); }, [log]);

  if (error) return <p className="error" role="alert">{error}</p>;
  if (!op) return <p>Loading…</p>;
  return (
    <>
      <h1>{op.type} <span className={`status ${op.status}`} aria-live="polite">{op.status}</span></h1>
      <div className="card">
        <div className="row">
          <div><div className="muted">Created</div>{when(op.created_at)}</div>
          <div><div className="muted">Started</div>{when(op.started_at)}</div>
          <div><div className="muted">Finished</div>{when(op.finished_at)}</div>
          <div><div className="muted">Database</div>{op.database || "–"}{op.target_database && ` → ${op.target_database}`}</div>
        </div>
        {op.message && <p className={op.status === "failed" ? "error" : op.status === "warning" ? "warn" : ""}>{op.message}</p>}
        {op.backup_id && <p><Link to="/backups">Backups</Link></p>}
      </div>
      <h2>Log</h2>
      <pre className="log" ref={pre}>{log || "(empty)"}</pre>
    </>
  );
}
