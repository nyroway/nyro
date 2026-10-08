import { useQuery } from "@tanstack/react-query";

import { NyroAppShell } from "./nyro-app-shell";
import { systemApi } from "@/lib/api/system";
import { gatewayReadiness } from "@/lib/gateway-readiness";
import { useLocale } from "@/lib/i18n";
import type { GatewayNode, GatewayStatus } from "@/lib/types";

export function AppLayout() {
  const { t } = useLocale();

  const nodesQuery = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => systemApi.nodes(),
    refetchInterval: 10_000,
  });
  const statusQuery = useQuery<GatewayStatus>({
    queryKey: ["gateway-status"],
    queryFn: () => systemApi.status(),
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
    <NyroAppShell
      version={statusQuery.data?.version || "dev"}
      readinessLabel={readinessLabel}
      readinessDetail={readinessDetail}
      readinessReady={readiness === "ready"}
    />
  );
}
