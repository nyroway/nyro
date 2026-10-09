import type { MessageKey } from "@/lib/i18n";
import type { GatewayNode } from "@/lib/types";

export type NodeConnectionMode = "inprocess" | "mtls" | "tls" | "plaintext";

export interface NodeConnectionModeDefinition {
  id: NodeConnectionMode;
  label: MessageKey;
  detail: MessageKey;
}

export const NODE_CONNECTION_MODES: Record<NodeConnectionMode, NodeConnectionModeDefinition> = {
  inprocess: { id: "inprocess", label: "nodes.inProcess", detail: "nodes.embeddedDetail" },
  mtls: { id: "mtls", label: "nodes.remoteMTLS", detail: "nodes.remoteDetail" },
  tls: { id: "tls", label: "nodes.remoteTLS", detail: "nodes.remoteDetail" },
  plaintext: { id: "plaintext", label: "nodes.remotePlaintext", detail: "nodes.remoteDetail" },
};

export function normalizeNodeConnectionMode(node: GatewayNode): NodeConnectionMode {
  if (node.conn_mode === "inprocess" || node.conn_mode === "mtls" || node.conn_mode === "tls") {
    return node.conn_mode;
  }
  return "plaintext";
}

export function isNodeConnectionVerified(node: GatewayNode): boolean {
  const mode = normalizeNodeConnectionMode(node);
  return mode === "inprocess" || mode === "mtls";
}
