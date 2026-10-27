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
      <form onSubmit={submit} style={{ width: "min(360px, 100%)" }}>
        <h1>🥟 Welcome to Dumpling</h1>
        <p className="muted">Create the admin account.</p>
        <label>Username<input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required maxLength={64} /></label>
        <label>Password (8–72 chars)<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required minLength={8} maxLength={72} /></label>
        <label>Confirm password<input type="password" value={confirm} onChange={(e) => setConfirm(e.target.value)} autoComplete="new-password" required /></label>
        {error && <p className="error" role="alert">{error}</p>}
        <button className="primary">Create account</button>
      </form>
    </main>
  );
}
