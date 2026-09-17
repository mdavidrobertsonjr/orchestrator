import { Component, type ReactNode } from "react";

export class AppErrorBoundary extends Component<{ children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  render() {
    if (this.state.failed) {
      return <main className="account-shell"><section className="account-card" role="alert">
        <h1>Your workspace couldn’t be displayed</h1>
        <p>Your saved jobs are still on the server. Reload to try again.</p>
        <button className="primary-button" onClick={() => window.location.reload()}>Reload workspace</button>
      </section></main>;
    }
    return this.props.children;
  }
}
