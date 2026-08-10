import { describe, expect, it } from "vitest";
import { gatewayReadiness } from "./gateway-readiness";

describe("gatewayReadiness", () => {
  it("reports ready whenever at least one data-plane node is connected", () => {
    expect(gatewayReadiness([{ node_id: "local" }])).toBe("ready");
  });

  it("reports unavailable when no data-plane nodes are connected", () => {
    expect(gatewayReadiness([])).toBe("not-ready");
  });

  it("keeps request failures distinct from a confirmed empty node list", () => {
    expect(gatewayReadiness(undefined)).toBe("unknown");
  });
});
