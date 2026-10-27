import { useState } from "react";
import { post } from "../api";

export default function Login({ onDone }: { onDone: () => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await post("/login", { username, password });
      onDone();
    } catch (err: any) {
      setError(err.message);
    }
  };

  return (
    <main className="center">
      <form onSubmit={submit} style={{ width: "min(340px, 100%)" }}>
        <h1>🥟 Dumpling</h1>
        <label>Username<input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus required /></label>
        <label>Password<input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required /></label>
        {error && <p className="error" role="alert">{error}</p>}
        <button className="primary">Log in</button>
      </form>
    </main>
  );
}
