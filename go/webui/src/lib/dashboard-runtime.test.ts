import { describe, expect, it } from "vitest";

import { summarizeDashboardRuntime } from "./dashboard-runtime";
import type { GatewayNode, RuntimeService } from "./types";

const node = (version: number): GatewayNode => ({
  node_id: `node-${version}`,
  hostname: `gateway-${version}`,
  app_version: "dev",
  service_port: "19530",
  remote_addr: "127.0.0.1:40000",
  conn_mode: "mtls",
  connected_at: "2026-08-09T00:00:00Z",
  applied_version: version,
});

const service = (status: RuntimeService["status"]): RuntimeService => ({
  id: "control-plane",
  status,
});

describe("summarizeDashboardRuntime", () => {
  it("reports a healthy runtime when a worker is connected", () => {
    expect(summarizeDashboardRuntime([node(286)], [service("running")])).toEqual({
      state: "healthy",
      runningServices: 1,
      configVersion: "rev-286",
    });
  });

  it("reports a degraded runtime without workers and mixed config revisions", () => {
    expect(summarizeDashboardRuntime([], [service("running")]).state).toBe("degraded");
    expect(summarizeDashboardRuntime([node(285), node(286)], []).configVersion).toBe("mixed");
  });

  it("keeps loading and failed API state distinct", () => {
    expect(summarizeDashboardRuntime(undefined, [])).toMatchObject({ state: "unknown" });
    expect(summarizeDashboardRuntime([], [], true)).toMatchObject({ state: "unknown" });
  });
});
