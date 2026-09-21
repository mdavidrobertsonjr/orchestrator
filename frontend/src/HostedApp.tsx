import { FormEvent, useEffect, useState } from "react";
import { App } from "./App";
import { useHostedAccounts } from "./api";
import "./hosted.css";

type User = { id: string; email: string; name: string };
type Connection = { connected: boolean; email?: string; plan?: string; error?: string; login?: { verificationUrl: string; userCode: string } };

async function accountRequest<T>(path: string, method = "GET", body?: object): Promise<T> {
  const response = await fetch(path, { method, credentials: "same-origin", headers: { Accept: "application/json", ...(body ? { "Content-Type": "application/json" } : {}) }, body: body ? JSON.stringify(body) : undefined });
  if (response.status === 401 && !path.startsWith("/auth/email/")) window.dispatchEvent(new Event("orchestrator:session-expired"));
  const data = await response.json();
  if (!response.ok) throw new Error(data.error ?? "Request failed. Please try again.");
  return data;
}

export function WebsiteApp() {
  const [mode, setMode] = useState<"loading" | "private" | "hosted" | "error">("loading");
  const [providers, setProviders] = useState({ google: false, email_signup: false });
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
        setProviders({ google: data.google === true, email_signup: data.email_signup === true });
        setMode("hosted");
      })
      .catch(error => { if (error.name !== "AbortError") setMode("error"); });
    return () => controller.abort();
  }, []);
  if (mode === "private") return <App />;
  if (mode === "hosted") return <HostedApp providers={providers} />;
  return <main className="account-shell"><section className="account-card"><h1>Orchestrator</h1><p>{mode === "error" ? "The website is temporarily unavailable." : "Opening your workspace…"}</p>{mode === "error" && <button onClick={() => window.location.reload()}>Try again</button>}</section></main>;
}

function HostedApp({ providers }: { providers: { google: boolean; email_signup: boolean } }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);
  const [connection, setConnection] = useState<Connection>({ connected: false });
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [settings, setSettings] = useState(false);
  const [emailToken, setEmailToken] = useState("");

  useEffect(() => {
    const readLink = () => {
      const token = new URLSearchParams(window.location.hash.slice(1)).get("email_token");
      if (token) {
        setEmailToken(token);
        window.history.replaceState(null, "", window.location.pathname + window.location.search);
      }
    };
    readLink();
    window.addEventListener("hashchange", readLink);
    return () => window.removeEventListener("hashchange", readLink);
  }, []);

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
  if (emailToken) return <EmailAccess providers={providers} token={emailToken} onCancel={() => setEmailToken("")} onLogin={next => { setUser(next); setEmailToken(""); setConnection({ connected: false }); setError(""); setSettings(false); }} />;
  if (!user) return (
    <EmailAccess providers={providers} notice={error} onLogin={next => { setUser(next); setError(""); }} />
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
      <App key={user.id} aiConnected={connection.connected} />
    </>
  );
}

function EmailAccess({ providers, token, notice, onLogin, onCancel }: {
  providers: { google: boolean; email_signup: boolean }; token?: string; notice?: string;
  onLogin: (user: User) => void; onCancel?: () => void;
}) {
  const [action, setAction] = useState<"login" | "signup" | "reset">("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const title = token ? "Choose your password" : action === "signup" ? "Create your account" : action === "reset" ? "Reset your password" : "Welcome to your workspace";
  function switchAction(next: typeof action) { setAction(next); setError(""); setMessage(""); setPassword(""); setConfirmation(""); }
  async function submit(event: FormEvent) {
    event.preventDefault(); setError(""); setMessage("");
    if (token && password !== confirmation) { setError("The passwords do not match."); return; }
    setBusy(true);
    try {
      if (token || action === "login") {
        const user = await accountRequest<User>(token ? "/auth/email/complete" : "/auth/email/login", "POST", token ? { token, password, name } : { email, password });
        setPassword(""); setConfirmation(""); onLogin(user);
      } else {
        const result = await accountRequest<{ message: string }>(`/auth/email/${action}`, "POST", { email });
        setMessage(result.message);
      }
    } catch (err) { setError((err as Error).message); }
    finally { setBusy(false); }
  }
  return <main className="account-shell"><section className="account-card">
    <span className="account-eyebrow">YOUR JOB SEARCH, IN ONE PLACE</span>
    <h1>{title}</h1>
    <p>{token ? "Set a password of at least 15 characters. A password reset signs out your other sessions." : action === "login" ? "Save jobs, track applications, and keep your monitors running." : "We’ll email a link to verify your address and choose a password. The link expires in 30 minutes."}</p>
    {!token && providers.google && <a className="primary-button account-signin" href="/auth/google">Continue with Google</a>}
    <form className="email-access" onSubmit={event => void submit(event)}>
      {!token && <label>Email<input type="email" autoComplete="email" required maxLength={254} value={email} onChange={event => setEmail(event.target.value)} /></label>}
      {token && <label>Name (optional)<input autoComplete="name" maxLength={100} value={name} onChange={event => setName(event.target.value)} /></label>}
      {(token || action === "login") && <label>{token ? "New password" : "Password"}<input type="password" autoComplete={token ? "new-password" : "current-password"} required minLength={token ? 15 : undefined} maxLength={1024} value={password} onChange={event => setPassword(event.target.value)} /></label>}
      {token && <label>Confirm password<input type="password" autoComplete="new-password" required minLength={15} maxLength={1024} value={confirmation} onChange={event => setConfirmation(event.target.value)} /></label>}
      <button className="primary-button" disabled={busy || (!token && action !== "login" && !providers.email_signup)} type="submit">{busy ? "Please wait…" : token ? "Save password and sign in" : action === "login" ? "Sign in with email" : "Send email link"}</button>
    </form>
    <div className="account-options">
      {token ? <button type="button" onClick={onCancel} disabled={busy}>Back to sign in</button> : <>
        <button type="button" disabled={busy} onClick={() => switchAction(action === "login" ? "signup" : "login")}>{action === "login" ? "Create an account" : "Back to sign in"}</button>
        {action !== "reset" && <button type="button" disabled={busy} onClick={() => switchAction("reset")}>Forgot password?</button>}
      </>}
    </div>
    {!providers.email_signup && !token && <p className="account-hint">Email signup and password recovery will be available once the site’s email service is configured.</p>}
    <p className="account-hint">Connect ChatGPT after signing in. Your jobs belong to your website account. You only need a browser.</p>
    {message && <p role="status">{message}</p>}
    {(error || notice) && <p role="alert" className="account-error">{error || notice}</p>}
  </section></main>;
}
