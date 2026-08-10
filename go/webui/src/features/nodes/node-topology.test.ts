import { describe, expect, it } from "vitest";

import type { GatewayNode } from "@/lib/types";
import { buildNodeTopology } from "./node-topology";

function node(nodeID: string, connMode: string): GatewayNode {
  return {
    node_id: nodeID,
    hostname: nodeID,
    app_version: "dev",
    service_port: "19530",
    remote_addr: "127.0.0.1:40000",
    conn_mode: connMode,
    connected_at: "2026-08-10T08:00:00Z",
    applied_version: 1,
  };
}

describe("buildNodeTopology", () => {
  it("uses one direct edge for a single worker", () => {
    const topology = buildNodeTopology([node("gateway-local", "inprocess")]);

    expect(topology.layout).toBe("direct");
    expect(topology.connections.map((connection) => connection.node.node_id)).toEqual(["gateway-local"]);
    expect(topology.connections[0]?.mode.id).toBe("inprocess");
  });

  it("creates one branch per worker even when connection modes match", () => {
    const topology = buildNodeTopology([
      node("gateway-sz-01", "mtls"),
      node("gateway-sz-02", "mtls"),
    ]);

    expect(topology.layout).toBe("branched");
    expect(topology.connections.map((connection) => connection.node.node_id)).toEqual([
      "gateway-sz-01",
      "gateway-sz-02",
    ]);
    expect(topology.connections.map((connection) => connection.mode.id)).toEqual(["mtls", "mtls"]);
  });

  it("does not draw a connection when no workers exist", () => {
    expect(buildNodeTopology([])).toEqual({ layout: "empty", connections: [] });
  });
});
