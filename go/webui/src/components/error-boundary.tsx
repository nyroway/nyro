import type { ReactNode } from "react";
import { Component } from "react";
import { CircleAlert } from "lucide-react";

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

    // The fallback screen sits outside LocaleProvider and cannot call t() — keep hardcoded English (§9.11).
    return (
      <div className="error-screen">
        <div className="empty">
          <strong>Something went wrong</strong>
          <p>The console stopped this page from crashing. Try again or check the browser console for details.</p>
          {this.state.errorMessage && (
            <div className="alert alert-error">
              <CircleAlert aria-hidden="true" />
              <span>{this.state.errorMessage}</span>
            </div>
          )}
          <div>
            <button type="button" className="button" onClick={this.onRetry}>Try again</button>
          </div>
        </div>
      </div>
    );
  }
}
