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
    // eslint-disable-next-line no-console
    console.error("[nyro-console] runtime error:", error);
  }

  private onRetry = () => {
    this.setState({ hasError: false, errorMessage: "" });
  };

  render() {
    if (!this.state.hasError) return this.props.children;

    return (
      <div className="min-h-screen bg-background px-6 py-10">
        <div className="mx-auto max-w-xl rounded-2xl border border-rose-200 bg-white p-6 shadow-sm">
          <h1 className="text-lg font-semibold text-slate-900">页面运行异常</h1>
          <p className="mt-2 text-sm text-slate-600">
            前端已阻止白屏崩溃。你可以重试，或查看控制台日志定位问题。
          </p>
          {this.state.errorMessage && (
            <pre className="mt-4 overflow-x-auto rounded-lg bg-slate-50 p-3 text-xs text-rose-700">
              {this.state.errorMessage}
            </pre>
          )}
          <button
            onClick={this.onRetry}
            className="mt-4 cursor-pointer rounded-lg bg-slate-900 px-3 py-2 text-xs font-medium text-white"
          >
            重试
          </button>
        </div>
      </div>
    );
  }
}

