import { describe, expect, it } from "vitest";

import type { Consumer } from "@/lib/types";
import { filterConsumers } from "./consumer-view-model";

const consumers: Consumer[] = [
  { id: "analytics", name: "Analytics Worker", enabled: true, routes: ["gpt-5"], protocols: ["openai"], ip_allowlist: ["10.0.0.0/8"] },
  { id: "batch", name: "Batch Pipeline", enabled: false, routes: ["claude-sonnet"], protocols: ["anthropic"] },
];

describe("filterConsumers", () => {
  it("searches identity and access fields", () => {
    expect(filterConsumers(consumers, { query: "10.0.0.0", status: "all" })).toEqual([consumers[0]]);
    expect(filterConsumers(consumers, { query: "ANTHROPIC", status: "all" })).toEqual([consumers[1]]);
  });

  it("filters enabled state", () => {
    expect(filterConsumers(consumers, { query: "", status: "enabled" })).toEqual([consumers[0]]);
    expect(filterConsumers(consumers, { query: "", status: "disabled" })).toEqual([consumers[1]]);
  });
});
