import { useState } from "react";
import { post } from "../api";

export default function Setup({ onDone }: { onDone: () => void }) {
  const [username, setUsername] = useState("admin");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return setError("Passwords do not match");
    try {
      await post("/setup", { username, password });
      onDone();
    } catch (err: any) {
      setError(err.message);
    }
  };

  return (
    <main className="center">
      <form onSubmit={submit} style={{ width: 360 }}>
        <h1>🥟 Welcome to Dumpling</h1>
        <p className="muted">Create the admin account.</p>
        <label>Username<input value={username} onChange={(e) => setUsername(e.target.value)} required maxLength={64} /></label>
        <label>Password (8–72 chars)<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required minLength={8} maxLength={72} /></label>
        <label>Confirm password<input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} required /></label>
        {error && <p className="error">{error}</p>}
        <button className="primary">Create account</button>
      </form>
    </main>
  );
}
