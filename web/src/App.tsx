import { useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router";
import ErrorBoundary from "./ErrorBoundary";
import { get, post } from "./api";
import Setup from "./pages/Setup";
import Login from "./pages/Login";
import Connections from "./pages/Connections";
import Destinations from "./pages/Destinations";
import Backups from "./pages/Backups";
import Plans from "./pages/Plans";
import Operations from "./pages/Operations";
import OperationDetail from "./pages/OperationDetail";

type State = "loading" | "setup" | "login" | "ready";

export default function App() {
  const [state, setState] = useState<State>("loading");
  const [user, setUser] = useState("");
  const location = useLocation();

  const check = async () => {
    try {
      const s = await get<{ needed: boolean }>("/setup");
      if (s.needed) return setState("setup");
      const me = await get<{ username: string }>("/me");
      setUser(me.username);
      setState("ready");
    } catch {
      setState("login");
    }
  };

  useEffect(() => {
    check();
    const onUnauthorized = () => setState("login");
    window.addEventListener("dumpling:unauthorized", onUnauthorized);
    return () => window.removeEventListener("dumpling:unauthorized", onUnauthorized);
  }, []);

  if (state === "loading") return <main className="center">Loading…</main>;
  if (state === "setup") return <Setup onDone={() => setState("login")} />;
  if (state === "login") return <Login onDone={check} />;

  return (
    <div className="layout">
      <nav>
        <strong className="brand">🥟 Dumpling</strong>
        <NavLink to="/backups">Backups</NavLink>
        <NavLink to="/plans">Plans</NavLink>
        <NavLink to="/operations">Operations</NavLink>
        <NavLink to="/connections">Connections</NavLink>
        <NavLink to="/destinations">Destinations</NavLink>
        <span className="spacer" />
        <span className="muted">{user}</span>
        <button className="link" onClick={async () => { await post("/logout"); setState("login"); }}>Log out</button>
      </nav>
      <main>
        <ErrorBoundary key={location.pathname}>
          <Routes>
            <Route path="/" element={<Navigate to="/backups" replace />} />
            <Route path="/backups" element={<Backups />} />
            <Route path="/plans" element={<Plans />} />
            <Route path="/operations" element={<Operations />} />
            <Route path="/operations/:id" element={<OperationDetail />} />
            <Route path="/connections" element={<Connections />} />
            <Route path="/destinations" element={<Destinations />} />
            <Route path="*" element={<Navigate to="/backups" replace />} />
          </Routes>
        </ErrorBoundary>
      </main>
    </div>
  );
}
