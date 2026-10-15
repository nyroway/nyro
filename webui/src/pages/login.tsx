import { useEffect, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Eye, EyeOff } from "lucide-react";
import { setAdminToken, getAdminToken } from "@/lib/auth";
import { NyroIcon } from "@/components/ui/nyro-icon";
import { useLocale } from "@/lib/i18n";

export default function LoginPage() {
  const { t } = useLocale();
  const navigate = useNavigate();

  const [token, setToken] = useState("");
  const [showToken, setShowToken] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    const saved = localStorage.getItem("nyro-theme");
    const theme =
      saved === "dark" || saved === "light"
        ? saved
        : window.matchMedia("(prefers-color-scheme: dark)").matches
          ? "dark"
          : "light";
    document.documentElement.dataset.theme = theme;
    document.body.dataset.theme = theme;
  }, []);

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
      } catch {
        // Stay on the login page when the service is unreachable
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
        setError(t("login.errorInvalid"));
        inputRef.current?.focus();
      } else {
        setError(t("login.errorNetwork"));
      }
    } catch {
      setError(t("login.errorNetwork"));
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="login-screen">
      <form className="card login-card" onSubmit={handleSubmit}>
        <div className="login-brand">
          <NyroIcon size={48} />
          <h1>Nyro AI Gateway</h1>
          <p>{t("login.subtitle")}</p>
        </div>
        <div className="form-stack">
          <div className="secret-control">
            <input
              ref={inputRef}
              className="field-control"
              type={showToken ? "text" : "password"}
              value={token}
              onChange={(e) => {
                setToken(e.target.value);
                setError(null);
              }}
              placeholder={t("login.placeholder")}
              autoComplete="current-password"
              autoFocus
            />
            <button
              type="button"
              className="secret-toggle"
              data-tip={showToken ? t("login.hide") : t("login.show")}
              aria-label={showToken ? t("login.hide") : t("login.show")}
              onClick={() => setShowToken((v) => !v)}
            >
              {showToken ? <EyeOff size={16} /> : <Eye size={16} />}
            </button>
          </div>
          {error ? <p className="field-hint error">{error}</p> : null}
          <button className="button button-primary is-full" type="submit" disabled={loading || !token.trim()}>
            {loading ? t("login.submitting") : t("login.submit")}
          </button>
        </div>
      </form>
    </div>
  );
}
