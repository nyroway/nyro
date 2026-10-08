import { describe, expect, it } from "vitest";

import type { GatewayNode } from "@/lib/types";
import { isNodeConnectionVerified, normalizeNodeConnectionMode } from "./node-topology";

function node(connMode: string | undefined): GatewayNode {
  return {
    node_id: "gateway-1",
    hostname: "gateway-1",
    app_version: "dev",
    service_port: "19530",
    remote_addr: "127.0.0.1:40000",
    ...(connMode === undefined ? {} : { conn_mode: connMode }),
    connected_at: "2026-08-10T08:00:00Z",
    applied_version: 1,
  };
}

describe("normalizeNodeConnectionMode", () => {
  it("keeps the reported stream modes", () => {
    expect(normalizeNodeConnectionMode(node("inprocess"))).toBe("inprocess");
    expect(normalizeNodeConnectionMode(node("mtls"))).toBe("mtls");
    expect(normalizeNodeConnectionMode(node("tls"))).toBe("tls");
  });

  it("falls back to plaintext for older gateways and unknown values", () => {
    expect(normalizeNodeConnectionMode(node(undefined))).toBe("plaintext");
    expect(normalizeNodeConnectionMode(node("carrier-pigeon"))).toBe("plaintext");
  });
});

describe("isNodeConnectionVerified", () => {
  it("trusts in-process and mTLS connections", () => {
    expect(isNodeConnectionVerified(node("inprocess"))).toBe(true);
    expect(isNodeConnectionVerified(node("mtls"))).toBe(true);
  });

  it("flags tls and plaintext as self-reported", () => {
    expect(isNodeConnectionVerified(node("tls"))).toBe(false);
    expect(isNodeConnectionVerified(node(undefined))).toBe(false);
  });
});
