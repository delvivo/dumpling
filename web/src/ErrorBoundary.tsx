import { Component, type ReactNode } from "react";

// Shows the error instead of unmounting the whole app (blank page) when a page throws while rendering.
export default class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="card error">
        <strong>Something went wrong on this page.</strong>
        <code>{this.state.error.message}</code>
        <div><button onClick={() => window.location.reload()}>Reload</button></div>
      </div>
    );
  }
}
