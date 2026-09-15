import { useEffect, useState } from "react";
import { App } from "./App";
import { useHostedAccounts } from "./api";
import "./hosted.css";

type User = { id: string; email: string; name: string };
type Connection = { connected: boolean; email?: string; plan?: string; error?: string; login?: { verificationUrl: string; userCode: string } };

async function accountRequest<T>(path: string, method = "GET"): Promise<T> {
  const response = await fetch(path, { method, credentials: "same-origin", headers: { Accept: "application/json" } });
  if (response.status === 401) window.dispatchEvent(new Event("orchestrator:session-expired"));
  const data = await response.json();
  if (!response.ok) throw new Error(data.error ?? "Request failed. Please try again.");
  return data;
}

export function WebsiteApp() {
  const [mode, setMode] = useState<"loading" | "private" | "hosted" | "error">("loading");
  useEffect(() => {
    const controller = new AbortController();
    fetch("/auth/config", { signal: controller.signal, headers: { Accept: "application/json" } })
      .then(async response => {
        // The private server serves the SPA fallback for this unknown path.
        if (response.status === 404 || response.headers.get("Content-Type")?.includes("text/html")) return setMode("private");
        if (!response.ok) throw new Error("Site configuration unavailable");
        const data = await response.json();
        if (data.hosted !== true) throw new Error("Unknown site configuration");
        useHostedAccounts();
        setMode("hosted");
      })
      .catch(error => { if (error.name !== "AbortError") setMode("error"); });
    return () => controller.abort();
  }, []);
  if (mode === "private") return <App />;
  if (mode === "hosted") return <HostedApp />;
  return <main className="account-shell"><section className="account-card"><h1>Orchestrator</h1><p>{mode === "error" ? "The website is temporarily unavailable." : "Opening your workspace…"}</p>{mode === "error" && <button onClick={() => window.location.reload()}>Try again</button>}</section></main>;
}

function HostedApp() {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [connection, setConnection] = useState<Connection>({ connected: false });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [settings, setSettings] = useState(false);

  useEffect(() => {
    let active = true;
    const load = async () => {
      try {
        const response = await fetch("/auth/me", { credentials: "same-origin" });
        if (response.status === 401) { if (active) setUser(null); return; }
        if (!response.ok) throw new Error("Your account is temporarily unavailable. Please reload to try again.");
        const next = await response.json();
        if (active) setUser(next);
      } catch (err) { if (active) setError((err as Error).message); }
      finally { if (active) setLoading(false); }
    };
    void load();
    const expired = () => { setUser(null); setConnection({ connected: false }); setError("Your session expired. Sign in again to reopen your saved workspace."); };
    window.addEventListener("orchestrator:session-expired", expired);
    return () => { active = false; window.removeEventListener("orchestrator:session-expired", expired); };
  }, []);

  useEffect(() => {
    if (!user) return;
    let active = true;
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      try {
        const next = await accountRequest<Connection>("/auth/chatgpt");
        if (active) setConnection(next);
      } catch (err) { if (active) setError((err as Error).message); }
      if (active) timer = setTimeout(poll, 5000);
    };
    void poll();
    return () => { active = false; clearTimeout(timer); };
  }, [user]);

  async function connect() {
    setBusy(true); setError("");
    try { setConnection(await accountRequest<Connection>("/auth/chatgpt/connect", "POST")); }
    catch (err) { setError((err as Error).message); }
    finally { setBusy(false); }
  }
  async function disconnect() {
    setBusy(true); setError("");
    try { await accountRequest("/auth/chatgpt/disconnect", "POST"); setConnection({ connected: false }); }
    catch (err) { setError((err as Error).message); }
    finally { setBusy(false); }
  }
  async function logout() {
    setBusy(true); setError("");
    try { await accountRequest("/auth/logout", "POST"); setUser(null); setConnection({ connected: false }); setSettings(false); }
    catch (err) { setError((err as Error).message); }
    finally { setBusy(false); }
  }
  if (loading) return <main className="account-shell"><p>Loading your account…</p></main>;
  if (!user) return (
    <main className="account-shell">
      <section className="account-card">
        <span className="account-eyebrow">YOUR JOB SEARCH, IN ONE PLACE</span>
        <h1>A workspace that keeps looking for you.</h1>
        <p>Create an account to save your jobs, track applications, and run recurring monitors.</p>
        <a className="primary-button account-signin" href="/auth/google">Continue with Google</a>
        <p className="account-hint">Your first sign-in creates your account. Connect ChatGPT afterward to turn your requests into job searches and workflows.</p>
        {error && <p role="alert" className="account-error">{error}</p>}
      </section>
    </main>
  );
  return (
    <>
      <header className="account-bar">
        <span><strong>{user.name || "Your workspace"}</strong><small>{user.email}</small></span>
        <div><button type="button" onClick={() => setSettings(!settings)}>{connection.connected ? "ChatGPT connected" : "Connect ChatGPT"}</button><button type="button" disabled={busy} onClick={() => void logout()}>Sign out</button></div>
      </header>
      {(settings || !connection.connected) && (
        <section className="connection-panel" aria-label="ChatGPT connection">
          <div><h2>{connection.connected ? "Your ChatGPT connection" : "Connect ChatGPT to create AI plans"}</h2><p>{connection.connected ? `Connected as ${connection.email || "your ChatGPT account"}${connection.plan ? ` · ${connection.plan}` : ""}.` : "Approve the connection on OpenAI’s website. Your ChatGPT password is never entered here, and your plan’s Codex usage limits apply."}</p><p className="account-hint">Your saved jobs stay in this website account. Scheduled monitors continue when you close your browser. Disconnecting ChatGPT stops new AI plans; it does not stop existing monitors.</p></div>
          {connection.login ? (
            <div className="device-login">
              <p>Enter this code on OpenAI’s page:</p>
              <strong className="device-code">{connection.login.userCode}</strong>
              <a className="primary-button" href={connection.login.verificationUrl} target="_blank" rel="noopener noreferrer">Open OpenAI sign-in</a>
              <p role="status">Waiting for approval… You can keep this page open.</p>
              <button disabled={busy} onClick={() => void disconnect()}>Cancel connection</button>
            </div>
          ) : <button className="primary-button" disabled={busy} onClick={() => void (connection.connected ? disconnect() : connect())}>{busy ? "Please wait…" : connection.connected ? "Disconnect ChatGPT" : "Connect ChatGPT"}</button>}
          {connection.error && <p className="account-error" role="alert">{connection.error}</p>}
        </section>
      )}
      {error && <div className="account-notice" role="alert">{error}<button onClick={() => setError("")}>Dismiss</button></div>}
      <App />
    </>
  );
}
