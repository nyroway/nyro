import { useEffect, useState } from "react";
import { NavLink, Outlet, useLocation, useNavigate } from "react-router-dom";
import {
  BarChart3,
  BookOpen,
  Database,
  ExternalLink,
  Github,
  Home,
  KeyRound,
  Moon,
  Network,
  Plug,
  Route,
  ScrollText,
  Server,
  Settings,
  Sun,
} from "lucide-react";

import nyroLogoUrl from "@/assets/logos/NYRO-logo.png";
import { useLocale, type MessageKey } from "@/lib/i18n";
import { openExternalUrl } from "@/lib/open-external";
import { GlobalSearch } from "./global-search";

/* Sidebar grouping (baseline new-webui providers.html .nav): Overview (no group name) | Upstream | Runtime,
   then a nav-divider, followed by Settings (no group name); every item carries a nav-icon. */
type NavEntry = { path: string; label: MessageKey; icon: typeof Server };
type NavGroup = { label?: MessageKey; entries: NavEntry[] };
type NavSection = NavGroup | "divider";

const NAV_SECTIONS: NavSection[] = [
  { entries: [{ path: "/", label: "nav.overview", icon: Home }] },
  { label: "nav.upstream", entries: [
    { path: "/providers", label: "nav.providers", icon: Server },
    { path: "/models", label: "nav.models", icon: Route },
    { path: "/api-keys", label: "nav.apiKeys", icon: KeyRound },
    { path: "/connect", label: "nav.connect", icon: Plug },
  ] },
  { label: "nav.runtime", entries: [
    { path: "/nodes", label: "nav.nodes", icon: Network },
    { path: "/services", label: "nav.services", icon: Database },
    { path: "/logs", label: "nav.logs", icon: ScrollText },
    { path: "/stats", label: "nav.stats", icon: BarChart3 },
  ] },
  "divider",
  { entries: [{ path: "/settings", label: "nav.settings", icon: Settings }] },
];

const EXTERNAL_LINKS: { label: MessageKey; href: string; icon: typeof Github }[] = [
  { label: "shell.github", href: "https://github.com/nyroway/nyro", icon: Github },
  { label: "shell.documentation", href: "https://github.com/nyroway/nyro#readme", icon: BookOpen },
];

type Theme = "light" | "dark";
const THEME_STORAGE_KEY = "nyro-theme";

function readInitialTheme(): Theme {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // Storage access can throw in private windows — fall through to media query.
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function currentNavLabel(pathname: string): MessageKey {
  if (pathname === "/") return "nav.overview";
  for (const section of NAV_SECTIONS) {
    if (section === "divider") continue;
    for (const entry of section.entries) {
      if (entry.path !== "/" && (pathname === entry.path || pathname.startsWith(`${entry.path}/`))) {
        return entry.label;
      }
    }
  }
  return "nav.overview";
}

export function NyroAppShell({
  version,
  readinessLabel,
  readinessDetail,
  readinessReady,
}: {
  version: string;
  readinessLabel: string;
  readinessDetail: string;
  readinessReady: boolean;
}) {
  const { locale, setLocale, t } = useLocale();
  const navigate = useNavigate();
  const location = useLocation();
  const [theme, setTheme] = useState<Theme>(readInitialTheme);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    // The baseline's dark-mode switch rules are written with a body[data-theme="dark"] prefix (the only 4
    // body-prefixed rules in nyro-ui.css); with data-theme set on <html> alone they all fail to match, so
    // in dark mode the switch still renders light (white track and knob when ON, slider invisible). Mirror it onto <body> in sync.
    document.body.dataset.theme = theme;
    try {
      localStorage.setItem(THEME_STORAGE_KEY, theme);
    } catch {
      // Ignore storage failures — the in-memory theme still applies.
    }
  }, [theme]);

  useEffect(() => {
    const tip = document.getElementById("hoverTip");
    if (!tip) return;
    // Baseline hover-tip (providers.html's syncActionTips + delegated mouseover):
    // .icon-action hover tips go through the body-level fixed layer (.hover-tip, nyro-ui.css),
    // text comes from data-tip, falling back to aria-label when absent, floating centered 6px above the icon.
    const onOver = (event: MouseEvent) => {
      const button = (event.target as Element | null)?.closest?.(".icon-action");
      if (!(button instanceof HTMLElement)) { tip.hidden = true; return; }
      tip.textContent = button.dataset.tip || button.getAttribute("aria-label") || "";
      const box = button.getBoundingClientRect();
      tip.style.left = `${box.left + box.width / 2}px`;
      tip.style.top = `${box.top}px`;
      tip.hidden = !tip.textContent;
      if (!tip.hidden) {
        // The baseline does no viewport clamping; this adds left/right safety bounds (the action column hugs the
        // table's right edge, and in narrow windows a centered popup would leave the viewport — same no-clipping principle as help-tip); the normal position is unchanged.
        const rect = tip.getBoundingClientRect();
        const margin = 8;
        if (rect.left < margin) tip.style.left = `${margin + rect.width / 2}px`;
        else if (rect.right > window.innerWidth - margin) {
          tip.style.left = `${window.innerWidth - margin - rect.width / 2}px`;
        }
      }
    };
    const onOut = (event: MouseEvent) => {
      const next = event.relatedTarget as Element | null;
      if (!next || !next.closest?.(".icon-action")) tip.hidden = true;
    };
    document.addEventListener("mouseover", onOver);
    document.addEventListener("mouseout", onOut);
    return () => {
      document.removeEventListener("mouseover", onOver);
      document.removeEventListener("mouseout", onOut);
    };
  }, []);

  const pageLabel = currentNavLabel(location.pathname);

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">{t("shell.skipToContent")}</a>
      <aside className="sidebar">
        <NavLink className="brand" to="/" aria-label="Nyro Console">
          <img className="brand-logo" src={nyroLogoUrl} alt="Nyro" width={28} height={28} />
          <span className="brand-copy">
            <span className="brand-lockup">
              <span className="brand-title">NYRO</span>
              <span className="brand-console">CONSOLE</span>
            </span>
            <span className="brand-meta">{t("shell.version")} {version}</span>
          </span>
        </NavLink>
        <nav className="nav" aria-label="Primary navigation">
          {NAV_SECTIONS.map((section, index) => section === "divider" ? (
            <div className="nav-divider" key={`divider-${index}`} />
          ) : (
            <div className="nav-group" key={section.label ?? `group-${index}`}>
              {section.label && <p className="nav-label">{t(section.label)}</p>}
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
          {EXTERNAL_LINKS.map((link) => (
            <button
              key={link.href}
              type="button"
              className="nav-item external"
              onClick={() => void openExternalUrl(link.href)}
            >
              <link.icon className="nav-icon" aria-hidden="true" />
              <span>{t(link.label)}</span>
              <ExternalLink className="nav-external-icon" aria-hidden="true" />
            </button>
          ))}
        </div>
      </aside>
      <div className="main">
        <header className="topbar">
          <div className="breadcrumb">
            <span>{t("shell.console")}</span>
            <span className="breadcrumb-separator" aria-hidden="true">/</span>
            <strong>{t(pageLabel)}</strong>
          </div>
          <GlobalSearch />
          <div className="topbar-actions">
            <button
              type="button"
              className={readinessReady ? "status-tag" : "tag"}
              title={readinessDetail}
              onClick={() => navigate("/nodes")}
            >
              {readinessReady && <span className="status-dot" aria-hidden="true" />}
              {readinessLabel}
            </button>
            <div className="lang-switch" role="group" aria-label={t("shell.languageLabel")}>
              <button
                type="button"
                className={`lang-button${locale === "en-US" ? " active" : ""}`}
                onClick={() => setLocale("en-US")}
              >
                EN
              </button>
              <button
                type="button"
                className={`lang-button${locale === "zh-CN" ? " active" : ""}`}
                onClick={() => setLocale("zh-CN")}
              >
                中
              </button>
            </div>
            <button
              type="button"
              className="icon-button theme-button"
              title={t("shell.theme")}
              onClick={() => setTheme(theme === "dark" ? "light" : "dark")}
            >
              {theme === "dark" ? <Sun aria-hidden="true" /> : <Moon aria-hidden="true" />}
              <span>{t("shell.theme")}</span>
            </button>
          </div>
        </header>
        <main id="main-content">
          <Outlet />
        </main>
      </div>
      {/* Baseline body-level hover tip layer (providers.html #hoverTip): .hover-tip is a
          fixed layer driven by the delegated listeners above; the app-shell chain has no transform,
          so left/top are viewport coordinates. */}
      <div className="hover-tip" id="hoverTip" hidden />
    </div>
  );
}
