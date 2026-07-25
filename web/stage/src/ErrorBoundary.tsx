// Top-level error boundary for the projector. A malformed server frame (e.g. a
// board with a missing cells array) must never white-screen the stage in the
// middle of a show. When a render throws, we show an unobtrusive "recovering"
// notice and auto-retry on an interval — the next good frame re-renders the
// real UI. This keeps the projector self-healing with no manual reload.

import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  // Retry cadence in ms; overridable for tests.
  retryMs?: number;
}

interface State {
  hasError: boolean;
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { hasError: false };
  private timer: ReturnType<typeof setInterval> | null = null;

  static getDerivedStateFromError(): State {
    return { hasError: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error("[stage] render error, auto-recovering:", error, info.componentStack);
    // Clear the error state periodically so a subsequent (good) frame or state
    // update re-renders the children. If it throws again we land right back
    // here — a harmless loop until the data recovers.
    if (this.timer === null) {
      this.timer = setInterval(() => {
        this.setState({ hasError: false });
      }, this.props.retryMs ?? 2000);
    }
  }

  componentDidUpdate() {
    // Recovered: a good frame rendered without throwing, so stop retrying.
    if (!this.state.hasError && this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  componentWillUnmount() {
    if (this.timer !== null) {
      clearInterval(this.timer);
      this.timer = null;
    }
  }

  render() {
    if (this.state.hasError) {
      return (
        <div className="flex h-full w-full items-center justify-center text-2xl text-neon-amber/70">
          recovering…
        </div>
      );
    }
    return this.props.children;
  }
}

export default ErrorBoundary;
