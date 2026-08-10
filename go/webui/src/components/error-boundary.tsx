import type { ReactNode } from "react";
import { Component } from "react";

interface Props {
  children: ReactNode;
}

interface State {
  hasError: boolean;
  errorMessage: string;
}

export class AppErrorBoundary extends Component<Props, State> {
  state: State = {
    hasError: false,
    errorMessage: "",
  };

  static getDerivedStateFromError(error: Error): State {
    return {
      hasError: true,
      errorMessage: error?.message || "Unknown error",
    };
  }

  componentDidCatch(error: Error) {
    // keep a stable fallback UI instead of full white screen
    // and still expose runtime detail in console for debugging.
    console.error("[nyro-console] runtime error:", error);
  }

  private onRetry = () => {
    this.setState({ hasError: false, errorMessage: "" });
  };

  render() {
    if (!this.state.hasError) return this.props.children;

    return (
      <div className="v2-error-screen">
        <div className="v2-error-surface">
          <span>NYRO CONSOLE</span>
          <h1>Something went wrong</h1>
          <p>The console stopped this page from crashing. Try again or check the browser console for details.</p>
          {this.state.errorMessage && (
            <pre>{this.state.errorMessage}</pre>
          )}
          <button onClick={this.onRetry}>Try again</button>
        </div>
      </div>
    );
  }
}
