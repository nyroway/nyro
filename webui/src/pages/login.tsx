import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Eye, EyeOff, KeyRound, Loader2 } from "lucide-react";
import { setAdminToken, getAdminToken } from "@/lib/auth";
import { Button } from "@/components/ui/button";
import { NyroIcon } from "@/components/ui/nyro-icon";
import { useLocale } from "@/lib/i18n";

const T = {
  "zh-CN": {
    title: "Nyro AI Gateway",
    subtitle: "输入 Admin Token 以访问管理控制台",
    placeholder: "请输入 Admin Token",
    submit: "登录",
    submitting: "验证中...",
    errorInvalid: "Token 无效，请重试",
    errorNetwork: "无法连接到服务，请检查服务是否正常运行",
    show: "显示",
    hide: "隐藏",
  },
  "en-US": {
    title: "Nyro AI Gateway",
    subtitle: "Enter your Admin Token to access the dashboard",
    placeholder: "Enter Admin Token",
    submit: "Sign In",
    submitting: "Verifying...",
    errorInvalid: "Invalid token, please try again",
    errorNetwork: "Cannot connect to server, please check if it is running",
    show: "Show",
    hide: "Hide",
  },
} as const;

export default function LoginPage() {
  const { locale } = useLocale();
  const t = T[locale] ?? T["en-US"];
  const navigate = useNavigate();

  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  // Apply saved theme
  useEffect(() => {
    const saved = localStorage.getItem("nyro-theme");
    const theme =
      saved === "dark" || saved === "light"
        ? saved
        : window.matchMedia("(prefers-color-scheme: dark)").matches
          ? "dark"
          : "light";
    document.documentElement.setAttribute("data-theme", theme);
  }, []);

  // If no token is required (server returns 200 on /status without auth), skip login
  useEffect(() => {
    async function checkAuthRequired() {
      try {
        const existing = getAdminToken();
        const headers: HeadersInit = existing
          ? { Authorization: `Bearer ${existing}` }
          : {};
        const resp = await fetch("/api/v1/status", { headers });
        if (resp.ok) {
          navigate("/", { replace: true });
        }
        // 401 without token → auth is required, stay on login page
      } catch {
        // network error → server not reachable, stay on login
      }
    }
    checkAuthRequired();
  }, [navigate]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = token.trim();
    if (!trimmed) return;

    setLoading(true);
    setError(null);

    try {
      const resp = await fetch("/api/v1/status", {
        headers: { Authorization: `Bearer ${trimmed}` },
      });

      if (resp.ok) {
        setAdminToken(trimmed);
        navigate("/", { replace: true });
      } else if (resp.status === 401) {
        setError(t.errorInvalid);
        inputRef.current?.focus();
      } else {
        setError(t.errorNetwork);
      }
    } catch {
      setError(t.errorNetwork);
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4">
      <div className="w-full max-w-sm space-y-8">
        {/* Logo + Title */}
        <div className="flex flex-col items-center gap-3 text-center">
          <NyroIcon size={52} />
          <div>
            <h1 className="text-xl font-semibold tracking-tight text-foreground">
              {t.title}
            </h1>
            <p className="mt-1 text-sm text-muted-foreground">{t.subtitle}</p>
          </div>
        </div>

        {/* Form */}
        <form onSubmit={handleSubmit} className="space-y-4">
          <div className="relative">
            <div className="pointer-events-none absolute inset-y-0 left-3 flex items-center">
              <KeyRound className="h-4 w-4 text-muted-foreground" />
            </div>
            <input
              ref={inputRef}
              type={showToken ? "text" : "password"}
              value={token}
              onChange={(e) => {
                setToken(e.target.value);
                setError(null);
              }}
              placeholder={t.placeholder}
              autoComplete="current-password"
              autoFocus
              className="flex h-10 w-full rounded-md border border-input bg-background py-2 pl-9 pr-10 text-sm ring-offset-background placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
            />
            <button
              type="button"
              tabIndex={-1}
              title={showToken ? t.hide : t.show}
              onClick={() => setShowToken((v) => !v)}
              className="absolute inset-y-0 right-3 flex items-center text-muted-foreground hover:text-foreground"
            >
              {showToken ? (
                <EyeOff className="h-4 w-4" />
              ) : (
                <Eye className="h-4 w-4" />
              )}
            </button>
          </div>

          {error && (
            <p className="text-sm text-destructive">{error}</p>
          )}

          <Button
            type="submit"
            className="w-full"
            disabled={loading || !token.trim()}
          >
            {loading ? (
              <>
                <Loader2 className="h-4 w-4 animate-spin" />
                {t.submitting}
              </>
            ) : (
              t.submit
            )}
          </Button>
        </form>
      </div>
    </div>
  );
}
