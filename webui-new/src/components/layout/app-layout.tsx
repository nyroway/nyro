import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import {
  BarChart3,
  BookOpen,
  ExternalLink,
  Github,
  Home,
  KeyRound,
  LogOut,
  Moon,
  Plug,
  Route,
  ScrollText,
  Server,
  Settings,
  Sun,
} from "lucide-react";

import nyroLogoUrl from "@/assets/logos/NYRO-logo.png";
import { clearAdminToken, getAdminToken } from "@/lib/auth";
import { useLocale } from "@/lib/i18n";
import { openExternalUrl } from "@/lib/open-external";
import type { MessageKey } from "@/lib/messages";

type NavEntry = { path: string; label: MessageKey; icon: typeof Server };

const NAV_SECTIONS: Array<{ label?: MessageKey; entries: NavEntry[] } | "divider"> = [
  { entries: [{ path: "/", label: "shell.navOverview", icon: Home }] },
  {
    label: "shell.navUpstream",
    entries: [
      { path: "/providers", label: "shell.navProviders", icon: Server },
      { path: "/models", label: "shell.navModels", icon: Route },
      { path: "/api-keys", label: "shell.navApiKeys", icon: KeyRound },
      { path: "/connect", label: "shell.navConnect", icon: Plug },
    ],
  },
  {
    label: "shell.navRuntime",
    entries: [
      { path: "/logs", label: "shell.navLogs", icon: ScrollText },
      { path: "/stats", label: "shell.navStats", icon: BarChart3 },
    ],
  },
  "divider",
  { entries: [{ path: "/settings", label: "shell.navSettings", icon: Settings }] },
];

function navEntries() {
  return NAV_SECTIONS.flatMap((section) => (section === "divider" ? [] : section.entries));
}

type Theme = "light" | "dark";

function readInitialTheme(): Theme {
  try {
    const stored = localStorage.getItem("nyro-theme");
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // Fall back to the system preference when storage cannot be read, such as in private mode
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function currentPath(pathname: string) {
  if (pathname === "/") return "/";
  const match = navEntries().find((entry) => entry.path !== "/" && (pathname === entry.path || pathname.startsWith(`${entry.path}/`)));
  return match?.path ?? "/";
}

export function AppLayout() {
  const { locale, setLocale, t } = useLocale();
  const navigate = useNavigate();
  const location = useLocation();
  const [theme, setTheme] = useState<Theme>(readInitialTheme);
  const version = import.meta.env.VITE_APP_VERSION;
  const hasToken = getAdminToken() !== null;

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.body.dataset.theme = theme;
    try {
      localStorage.setItem("nyro-theme", theme);
    } catch {
      // Ignore storage failures; the theme still applies for the current session
    }
  }, [theme]);

  const page = navEntries().find((entry) => entry.path === currentPath(location.pathname));

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <NavLink className="brand" to="/" aria-label="Nyro Console">
          <img className="brand-logo" src={nyroLogoUrl} alt="Nyro" width={28} height={28} />
          <span className="brand-copy">
            <span className="brand-lockup">
              <span className="brand-title">NYRO</span>
              <span className="brand-console">CONSOLE</span>
            </span>
            <span className="brand-meta">{version}</span>
          </span>
        </NavLink>
        <nav className="nav" aria-label="Primary">
          {NAV_SECTIONS.map((section, index) => section === "divider" ? (
            <div className="nav-divider" key={`divider-${index}`} />
          ) : (
            <div className="nav-group" key={section.label ?? `group-${index}`}>
              {section.label ? (
                <p className="nav-label">{t(section.label)}</p>
              ) : null}
              {section.entries.map((entry) => (
                <NavLink key={entry.path} to={entry.path} end={entry.path === "/"} className="nav-item">
                  <entry.icon className="nav-icon" aria-hidden="true" />
                  <span>{t(entry.label)}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>
        <div className="nav-external">
          <button type="button" className="nav-item external" onClick={() => void openExternalUrl("https://github.com/nyroway/nyro")}>
            <Github className="nav-icon" aria-hidden="true" />
            <span>GitHub</span>
            <ExternalLink className="nav-external-icon" aria-hidden="true" />
          </button>
          <button type="button" className="nav-item external" onClick={() => void openExternalUrl("https://github.com/nyroway/nyro#readme")}>
            <BookOpen className="nav-icon" aria-hidden="true" />
            <span>{t("shell.docs")}</span>
            <ExternalLink className="nav-external-icon" aria-hidden="true" />
          </button>
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <div className="breadcrumb">
            <span>{t("shell.console")}</span>
            <span className="breadcrumb-separator" aria-hidden="true">/</span>
            <strong>{page ? t(page.label) : t("shell.navOverview")}</strong>
          </div>
          <div className="topbar-actions">
            <div className="lang-switch" role="group" aria-label={t("shell.language")}>
              <button type="button" className={`lang-button${locale === "en-US" ? " active" : ""}`} onClick={() => setLocale("en-US")}>EN</button>
              <button type="button" className={`lang-button${locale === "zh-CN" ? " active" : ""}`} onClick={() => setLocale("zh-CN")}>中</button>
            </div>
            <button
              type="button"
              className="icon-button theme-button"
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}
              <span>{t("shell.theme")}</span>
            </button>
            {hasToken ? (
              <button
                type="button"
                className="icon-button"
                onClick={() => {
                  clearAdminToken();
                  navigate("/login", { replace: true });
                }}
              >
                <LogOut aria-hidden="true" />
                <span>{t("shell.signOut")}</span>
              </button>
            ) : null}
          </div>
        </header>
        <main id="main-content">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
