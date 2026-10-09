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

/* 侧栏分组（真源 new-webui providers.html .nav）：概览（无组名）｜上游｜运行，
   之后一条 nav-divider，再接设置（无组名）；每项带 nav-icon。 */
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
    // 真源的暗色开关规则写在 body[data-theme="dark"] 前缀上（nyro-ui.css
    // 仅有的 4 条 body 前缀规则），data-theme 只挂 <html> 时它们全部失配——
    // 暗色下开关仍呈亮色（ON 态白轨白钮、滑块不可见）。同步镜像到 <body>。
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
    // 真源 hover-tip（providers.html 的 syncActionTips + 委托 mouseover）：
    // .icon-action 的悬停提示走 body 级固定层（.hover-tip，nyro-ui.css），
    // 文案取 data-tip，缺省回退 aria-label，居中浮在图标上方 6px。
    const onOver = (event: MouseEvent) => {
      const button = (event.target as Element | null)?.closest?.(".icon-action");
      if (!(button instanceof HTMLElement)) { tip.hidden = true; return; }
      tip.textContent = button.dataset.tip || button.getAttribute("aria-label") || "";
      const box = button.getBoundingClientRect();
      tip.style.left = `${box.left + box.width / 2}px`;
      tip.style.top = `${box.top}px`;
      tip.hidden = !tip.textContent;
      if (!tip.hidden) {
        // 真源不做视口钳制；这里补左右安全界（操作列贴表右缘，窄窗口下
        // 居中弹层会出视口——同 help-tip 的免裁剪原则），正常位置不变。
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
      {/* 真源 body 级悬停提示层（providers.html #hoverTip）：.hover-tip 为
          fixed 固定层，由上方委托监听驱动，app-shell 链上无 transform，
          left/top 即视口坐标。 */}
      <div className="hover-tip" id="hoverTip" hidden />
    </div>
  );
}
