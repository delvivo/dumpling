import { useEffect, useState } from "react";
import { ApiError, bytes, del, get, post, put } from "../api";
import type { Destination, DestinationType, RescanResult, S3Config, SFTPConfig } from "../types";
import { Confirm } from "../Modal";

const blankS3: S3Config = { endpoint: "", use_tls: true, region: "us-east-1", bucket: "", prefix: "", access_key_id: "", path_style: false };
const blankSFTP: SFTPConfig = { host: "", port: 22, username: "", auth: "password", remote_dir: "", host_key_fingerprint: "" };

function blank(type: DestinationType = "local"): Destination {
  return { id: "", name: "", type, path: "", s3: type === "s3" ? { ...blankS3 } : null, sftp: type === "sftp" ? { ...blankSFTP } : null };
}

export function location(d: Destination): string {
  if (d.type === "s3" && d.s3) return `s3://${[d.s3.bucket, d.s3.prefix].filter(Boolean).join("/")}${d.s3.endpoint ? ` @ ${d.s3.endpoint}` : ""}`;
  if (d.type === "sftp" && d.sftp) return `sftp://${d.sftp.username}@${d.sftp.host}:${d.sftp.port}${d.sftp.remote_dir}`;
  return d.path;
}

function summary(r: RescanResult): string {
  const by: Record<string, number> = {};
  r.skipped.forEach((s) => { by[s.reason] = (by[s.reason] ?? 0) + 1; });
  const skipped = Object.entries(by).map(([k, n]) => `${n} ${k.replace("_", " ")}`).join(", ");
  return `Rescan: ${r.imported} backup(s) imported${skipped ? `; skipped ${skipped}` : ""}.`;
}

export default function Destinations() {
  const [list, setList] = useState<Destination[]>([]);
  const [edit, setEdit] = useState<Destination | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");

  const load = () => get<Destination[]>("/destinations").then(setList).catch((e) => setError(e.message));
  useEffect(() => { load(); }, []);

  const rescan = async (d: Destination) => {
    setError(""); setNotice("");
    try { setNotice(`${d.name} — ${summary(await post<RescanResult>(`/destinations/${d.id}/rescan`))}`); load(); }
    catch (err: any) { setError(err.message); }
  };

  const [deleting, setDeleting] = useState<Destination | null>(null);
  const remove = async (d: Destination) => {
    setError(""); setNotice("");
    try { await del(`/destinations/${d.id}`); load(); }
    catch (err) {
      if (err instanceof ApiError && err.code === "in_use") {
        const { plans = [], backup_count = 0 } = err.body?.detail ?? {};
        setError(`Cannot delete ${d.name}: ${plans.length ? `used by plans ${plans.join(", ")}` : "no plans"} — ${backup_count} backup(s).`);
      } else setError(String((err as Error).message));
    }
  };

  return (
    <>
      <h1>Destinations <button className="primary" onClick={() => { setEdit(blank()); setNotice(""); }}>Add destination</button></h1>
      {deleting && <Confirm title={`Delete destination ${deleting.name}?`} confirmLabel="Delete destination" onConfirm={() => remove(deleting)} onClose={() => setDeleting(null)}>Stored backup files are not touched.</Confirm>}
      {error && <p className="error" role="alert">{error}</p>}
      <p className="sr-only" role="status">{notice}</p>
      {notice && <p className="card">{notice}</p>}
      {edit && <DestinationForm key={edit.id || "new"} initial={edit} onDone={(n) => { setEdit(null); if (n) setNotice(n); load(); }} />}
      <table role="table">
        <thead role="rowgroup"><tr role="row"><th role="columnheader">Name</th><th role="columnheader">Type</th><th role="columnheader">Location</th><th role="columnheader">Backups</th><th role="columnheader">Free space</th><th role="columnheader" aria-label="Actions" /></tr></thead>
        <tbody role="rowgroup">
          {list.map((d) => (
            <tr role="row" key={d.id}>
              <td role="cell" data-label="Name">{d.name}</td><td role="cell" data-label="Type">{d.type}</td><td role="cell" data-label="Location"><code>{location(d)}</code></td><td role="cell" data-label="Backups">{d.backup_count ?? 0}</td>
              <td role="cell" data-label="Free space">{d.type === "local" ? bytes(d.free_bytes) : "–"}</td>
              <td role="cell" className="actions">
                <button onClick={() => { setEdit(d); setNotice(""); }}>Edit</button>
                <button onClick={() => rescan(d)} title="Import backups found on this destination">Rescan</button>
                <button className="subtle-danger" onClick={() => setDeleting(d)}>Delete</button>
              </td>
            </tr>
          ))}
          {!list.length && <tr role="row"><td role="cell" colSpan={6} className="muted">No destinations yet.</td></tr>}
        </tbody>
      </table>
    </>
  );
}

function DestinationForm({ initial, onDone }: { initial: Destination; onDone: (notice?: string) => void }) {
  const [d, setD] = useState<Destination>(initial);
  const [error, setError] = useState("");
  const [tested, setTested] = useState<{ ok: boolean; msg: string; fingerprint?: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const locked = !!d.id && (d.backup_count ?? 0) > 0; // location fields immutable (FR-013)
  const secret = (k: string) => (d.has_secret?.[k] ? "stored — leave blank to keep" : "");
  const s3 = (p: Partial<S3Config>) => setD({ ...d, s3: { ...d.s3!, ...p } });
  const sftp = (p: Partial<SFTPConfig>) => setD({ ...d, sftp: { ...d.sftp!, ...p } });
  const setType = (t: DestinationType) => setD({ ...blank(t), id: d.id, name: d.name });

  // A changed SFTP host or port invalidates the confirmed host key.
  const sftpAddr = (p: Partial<SFTPConfig>) => { sftp({ ...p, host_key_fingerprint: "" }); setTested(null); };

  const test = async () => {
    setError(""); setTested(null); setBusy(true);
    try {
      const r = await post<{ ok: boolean; host_key_fingerprint?: string }>("/destinations/test", d);
      setTested({ ok: true, msg: "Connection ok: write, read and delete succeeded.", fingerprint: r.host_key_fingerprint });
    } catch (err) {
      const e = err as ApiError;
      setTested({ ok: false, msg: e.message, fingerprint: e.body?.host_key_fingerprint });
    } finally { setBusy(false); }
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError(""); setBusy(true);
    try {
      const saved = d.id ? await put<Destination>(`/destinations/${d.id}`, d) : await post<Destination>("/destinations", d);
      onDone(saved.rescan && saved.rescan.imported + saved.rescan.skipped.length > 0 ? `${saved.name} — ${summary(saved.rescan)}` : undefined);
    } catch (err: any) {
      setError(err.code === "location_locked" ? "Type and location cannot change while this destination holds backups." : err.message);
    } finally { setBusy(false); }
  };

  const needsPin = d.type === "sftp" && !d.sftp?.host_key_fingerprint;

  return (
    <form onSubmit={save} style={{ marginBottom: "1.5rem" }}>
      <div className="row">
        <label>Name<input value={d.name} onChange={(e) => setD({ ...d, name: e.target.value })} required maxLength={64} /></label>
        <label>Type
          <select value={d.type} onChange={(e) => setType(e.target.value as DestinationType)} disabled={locked}>
            <option value="local">Local directory / mount</option>
            <option value="s3">S3-compatible bucket</option>
            <option value="sftp">SFTP server</option>
          </select>
        </label>
      </div>

      {d.type === "local" && (
        <label>Absolute directory path (local disk or mount)
          <input value={d.path} onChange={(e) => setD({ ...d, path: e.target.value })} required placeholder="/srv/backups" readOnly={locked} />
        </label>
      )}

      {d.type === "s3" && d.s3 && (
        <>
          <div className="row">
            <label>Endpoint (empty = AWS)<input value={d.s3.endpoint} onChange={(e) => s3({ endpoint: e.target.value })} placeholder="https://s3.example.com" readOnly={locked} /></label>
            <label>Region<input value={d.s3.region} onChange={(e) => s3({ region: e.target.value })} required /></label>
          </div>
          <div className="row">
            <label>Bucket<input value={d.s3.bucket} onChange={(e) => s3({ bucket: e.target.value })} required readOnly={locked} /></label>
            <label>Key prefix (optional)<input value={d.s3.prefix} onChange={(e) => s3({ prefix: e.target.value })} placeholder="dumpling/prod" readOnly={locked} /></label>
          </div>
          <div className="row">
            <label>Access key ID<input value={d.s3.access_key_id} onChange={(e) => s3({ access_key_id: e.target.value })} required autoComplete="off" /></label>
            <label>Secret access key
              <input type="password" value={d.s3.secret_access_key ?? ""} onChange={(e) => s3({ secret_access_key: e.target.value })}
                required={!d.has_secret?.secret_access_key} placeholder={secret("secret_access_key")} autoComplete="new-password" />
            </label>
          </div>
          <div className="row">
            <label className="inline"><input type="checkbox" checked={d.s3.use_tls} onChange={(e) => s3({ use_tls: e.target.checked })} /> Use HTTPS</label>
            <label className="inline"><input type="checkbox" checked={d.s3.path_style} onChange={(e) => s3({ path_style: e.target.checked })} /> Path-style addressing (MinIO and similar)</label>
          </div>
        </>
      )}

      {d.type === "sftp" && d.sftp && (
        <>
          <div className="row">
            <label>Host<input value={d.sftp.host} onChange={(e) => sftpAddr({ host: e.target.value })} required readOnly={locked} /></label>
            <label>Port<input type="number" min={1} max={65535} value={d.sftp.port} onChange={(e) => sftpAddr({ port: Number(e.target.value) })} required readOnly={locked} /></label>
            <label>Username<input value={d.sftp.username} onChange={(e) => sftp({ username: e.target.value })} required autoComplete="off" /></label>
          </div>
          <label>Remote directory (absolute)<input value={d.sftp.remote_dir} onChange={(e) => sftp({ remote_dir: e.target.value })} required placeholder="/backups" readOnly={locked} /></label>
          <div className="row">
            <label className="inline"><input type="radio" checked={d.sftp.auth === "password"} onChange={() => sftp({ auth: "password" })} /> Password</label>
            <label className="inline"><input type="radio" checked={d.sftp.auth === "key"} onChange={() => sftp({ auth: "key" })} /> Private key</label>
          </div>
          {d.sftp.auth === "password" ? (
            <label>Password
              <input type="password" value={d.sftp.password ?? ""} onChange={(e) => sftp({ password: e.target.value })}
                required={!d.has_secret?.password} placeholder={secret("password")} autoComplete="new-password" />
            </label>
          ) : (
            <>
              <label>Private key (PEM / OpenSSH)
                <textarea rows={4} value={d.sftp.private_key ?? ""} onChange={(e) => sftp({ private_key: e.target.value })}
                  required={!d.has_secret?.private_key} placeholder={secret("private_key") || "-----BEGIN OPENSSH PRIVATE KEY-----"} />
              </label>
              <label>Key passphrase (optional)
                <input type="password" value={d.sftp.key_passphrase ?? ""} onChange={(e) => sftp({ key_passphrase: e.target.value })}
                  placeholder={secret("key_passphrase")} autoComplete="new-password" />
              </label>
            </>
          )}
          {d.sftp.host_key_fingerprint
            ? <p className="muted">Pinned host key: <code>{d.sftp.host_key_fingerprint}</code></p>
            : <p className="muted">Run Test to see the server host key, then confirm it.</p>}
        </>
      )}

      <p className="sr-only" role="status">{tested ? `Test ${tested.ok ? "succeeded" : "failed"}: ${tested.msg}` : ""}</p>
      {tested && (
        <div className={tested.ok ? "card" : "warn"}>
          <span className={tested.ok ? "" : "error"}>{tested.msg}</span>
          {d.type === "sftp" && tested.fingerprint && tested.fingerprint !== d.sftp?.host_key_fingerprint && (
            <p>Server host key: <code>{tested.fingerprint}</code>{" "}
              <button type="button" onClick={() => sftp({ host_key_fingerprint: tested.fingerprint! })}>Trust this host key</button>
            </p>
          )}
        </div>
      )}
      {error && <p className="error" role="alert">{error}</p>}
      <div className="actions">
        <button className="primary" disabled={busy || needsPin} title={needsPin ? "Test and trust the server host key first" : undefined}>Save</button>
        {d.type !== "local" && <button type="button" onClick={test} disabled={busy}>Test</button>}
        <button type="button" onClick={() => onDone()}>Cancel</button>
      </div>
    </form>
  );
}
