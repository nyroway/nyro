import { useState } from "react";
import { Outlet, useNavigate } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import { Languages } from "lucide-react";

import { Sidebar } from "./sidebar";
import { CommandPalette } from "./command-palette";
import { backend } from "@/lib/backend";
import { gatewayReadiness } from "@/lib/gateway-readiness";
import { useLocale } from "@/lib/i18n";
import type { GatewayNode, GatewayStatus } from "@/lib/types";

export function AppLayout() {
  const { locale, setLocale, t } = useLocale();
  const navigate = useNavigate();
  const [paletteOpen, setPaletteOpen] = useState(false);

  const nodesQuery = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => backend("list_nodes"),
    refetchInterval: 10_000,
  });
  const statusQuery = useQuery<GatewayStatus>({
    queryKey: ["gateway-status"],
    queryFn: () => backend("get_gateway_status"),
    staleTime: 60_000,
  });

  const readiness = gatewayReadiness(nodesQuery.isError ? undefined : nodesQuery.data);
  const readinessLabel = readiness === "ready"
    ? t("gateway.ready")
    : readiness === "not-ready"
      ? t("gateway.notReady")
      : t("gateway.unknown");
  const readinessDetail = readiness === "ready"
    ? t("gateway.readyDetail")
    : readiness === "not-ready"
      ? t("gateway.notReadyDetail")
      : t("gateway.unknownDetail");

  return (
    <div className="v2-app-shell">
      <a className="skip-link" href="#main-content">{t("shell.skipToContent")}</a>
      <Sidebar version={statusQuery.data?.version || "dev"} />
      <div className="v2-workspace">
        <header className="v2-global-bar">
          <button type="button" className="v2-global-search" onClick={() => setPaletteOpen(true)}>
            <span>{t("shell.searchPlaceholder")}</span>
            <kbd>⌘ K</kbd>
          </button>
          <div className="v2-global-tools">
            <button
              type="button"
              className={`v2-readiness readiness-${readiness}`}
              title={readinessDetail}
              onClick={() => navigate("/nodes")}
            >
              <span className="v2-readiness-dot" aria-hidden="true" />
              {readinessLabel}
            </button>
            <button
              type="button"
              className="v2-text-tool"
              title={locale === "zh-CN" ? t("shell.switchToEnglish") : t("shell.switchToChinese")}
              onClick={() => setLocale(locale === "zh-CN" ? "en-US" : "zh-CN")}
            >
              <Languages aria-hidden="true" />
              {t("shell.language")}
            </button>
          </div>
        </header>
        <main id="main-content" className="v2-page-root">
          <Outlet />
        </main>
      </div>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  );
}
