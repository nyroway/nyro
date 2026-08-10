import { NavLink } from "react-router-dom";
import { ExternalLink } from "lucide-react";

import { Brand } from "@/components/v2/brand";
import { useLocale, type MessageKey } from "@/lib/i18n";
import { openExternalUrl } from "@/lib/open-external";

type NavEntry = { path: string; label: MessageKey };
type NavGroup = { label?: MessageKey; entries: NavEntry[] };

const NAV_GROUPS: NavGroup[] = [
  { entries: [{ path: "/", label: "nav.overview" }] },
  { label: "nav.configuration", entries: [
    { path: "/providers", label: "nav.providers" },
    { path: "/models", label: "nav.models" },
  ] },
  { label: "nav.access", entries: [
    { path: "/api-keys", label: "nav.apiKeys" },
    { path: "/connect", label: "nav.connect" },
  ] },
  { label: "nav.runtime", entries: [
    { path: "/nodes", label: "nav.nodes" },
    { path: "/services", label: "nav.services" },
  ] },
  { label: "nav.observability", entries: [
    { path: "/logs", label: "nav.logs" },
    { path: "/stats", label: "nav.stats" },
  ] },
  { label: "nav.system", entries: [{ path: "/settings", label: "nav.settings" }] },
];

export function Sidebar({ version }: { version: string }) {
  const { t } = useLocale();

  return (
    <aside className="v2-sidebar">
      <NavLink className="v2-brand" to="/" aria-label="Nyro Console">
        <Brand />
      </NavLink>
      <nav className="v2-primary-nav" aria-label="Primary navigation">
        {NAV_GROUPS.map((group, index) => (
          <div className="v2-nav-group" key={group.label || index}>
            {group.label && <p>{t(group.label)}</p>}
            {group.entries.map((entry) => (
              <NavLink key={entry.path} to={entry.path} end={entry.path === "/"}>
                {t(entry.label)}
              </NavLink>
            ))}
          </div>
        ))}
      </nav>
      <div className="v2-sidebar-footer">
        <button type="button" className="v2-sidebar-resource-link" onClick={() => void openExternalUrl("https://github.com/nyroway/nyro")}>
          {t("shell.github")}<ExternalLink aria-hidden="true" />
        </button>
        <button type="button" className="v2-sidebar-resource-link" onClick={() => void openExternalUrl("https://github.com/nyroway/nyro#readme")}>
          {t("shell.documentation")}<ExternalLink aria-hidden="true" />
        </button>
        <div className="v2-sidebar-product">
          <strong>Nyro Console</strong>
          <small>{t("shell.version")} <span>{version}</span></small>
        </div>
      </div>
    </aside>
  );
}
