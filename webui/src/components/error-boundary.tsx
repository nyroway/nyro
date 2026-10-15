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
    console.error("[nyro-console] runtime error:", error);
  }

  private onRetry = () => {
    this.setState({ hasError: false, errorMessage: "" });
  };

  render() {
    if (!this.state.hasError) return this.props.children;

    return (
      <div className="login-screen">
        <div className="card login-card">
          <h1>页面运行异常</h1>
          <p className="help">前端已阻止白屏崩溃。你可以重试，或查看控制台日志定位问题。</p>
          {this.state.errorMessage ? (
            <pre className="code-output">{this.state.errorMessage}</pre>
          ) : null}
          <button type="button" className="button button-primary" onClick={this.onRetry}>
            重试
          </button>
        </div>
      </div>
    );
  }
}
