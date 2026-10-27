import { useEffect, useState } from "react";
import { bytes, del, get, post, put } from "../api";
import type { Destination } from "../types";

export default function Destinations() {
  const [list, setList] = useState<Destination[]>([]);
  const [edit, setEdit] = useState<Destination | null>(null);
  const [error, setError] = useState("");

  const load = () => get<Destination[]>("/destinations").then(setList).catch((e) => setError(e.message));
  useEffect(() => { load(); }, []);

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setError("");
    try {
      const body = { name: edit!.name, path: edit!.path };
      if (edit!.id) await put(`/destinations/${edit!.id}`, body);
      else await post("/destinations", body);
      setEdit(null);
      load();
    } catch (err: any) {
      setError(err.message);
    }
  };

  const remove = async (d: Destination) => {
    if (!confirm(`Delete destination ${d.name}? Files on disk are not touched.`)) return;
    try { await del(`/destinations/${d.id}`); load(); } catch (err: any) { setError(err.message); }
  };

  return (
    <>
      <h1>Destinations <button className="primary" onClick={() => setEdit({ id: "", name: "", path: "" })}>Add destination</button></h1>
      {error && <p className="error">{error}</p>}
      {edit && (
        <form onSubmit={save} style={{ marginBottom: "1.5rem" }}>
          <div className="row">
            <label>Name<input value={edit.name} onChange={(e) => setEdit({ ...edit, name: e.target.value })} required maxLength={64} /></label>
            <label>Absolute directory path (local disk or mount)<input value={edit.path} onChange={(e) => setEdit({ ...edit, path: e.target.value })} required placeholder="/srv/backups" /></label>
          </div>
          <div className="actions">
            <button className="primary">Save</button>
            <button type="button" onClick={() => setEdit(null)}>Cancel</button>
          </div>
        </form>
      )}
      <table>
        <thead><tr><th>Name</th><th>Path</th><th>Free space</th><th /></tr></thead>
        <tbody>
          {list.map((d) => (
            <tr key={d.id}>
              <td>{d.name}</td><td><code>{d.path}</code></td><td>{bytes(d.free_bytes)}</td>
              <td className="actions">
                <button onClick={() => setEdit(d)}>Edit</button>
                <button onClick={() => remove(d)}>Delete</button>
              </td>
            </tr>
          ))}
          {!list.length && <tr><td colSpan={4} className="muted">No destinations yet.</td></tr>}
        </tbody>
      </table>
    </>
  );
}
