import type { GatewayNode, RuntimeService } from "./types";

export type DashboardRuntimeSummary = {
  state: "healthy" | "degraded" | "unknown";
  runningServices: number;
  configVersion: string;
};

export function summarizeDashboardRuntime(
  nodes: GatewayNode[] | undefined,
  services: RuntimeService[] | undefined,
  failed = false,
): DashboardRuntimeSummary {
  const runningServices = services?.filter((service) => service.status === "running").length ?? 0;
  const revisions = [...new Set((nodes ?? []).map((node) => node.applied_version))];
  const configVersion = revisions.length === 0 ? "—" : revisions.length === 1 ? `rev-${revisions[0]}` : "mixed";
  return {
    state: failed || !nodes || !services ? "unknown" : nodes.length > 0 ? "healthy" : "degraded",
    runningServices,
    configVersion,
  };
}
