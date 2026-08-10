export type GatewayReadiness = "ready" | "not-ready" | "unknown";

export function gatewayReadiness(nodes: ReadonlyArray<unknown> | undefined): GatewayReadiness {
  if (!nodes) return "unknown";
  return nodes.length > 0 ? "ready" : "not-ready";
}
