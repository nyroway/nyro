import { describe, expect, it } from "vitest";

import type { Route } from "@/lib/types";
import { filterRoutes } from "./model-view-model";

const routes: Route[] = [
  {
    id: "primary",
    model: "gpt-5",
    balance: "weighted",
    enable_auth: true,
    enabled: true,
    upstreams: [{ id: "a", route_id: "primary", upstream_id: "openai", model: "gpt-5", weight: 100, priority: 1, enabled: true }],
  },
  {
    id: "backup",
    model: "claude-sonnet",
    balance: "priority",
    enable_auth: false,
    enabled: false,
    upstreams: [{ id: "b", route_id: "backup", upstream_id: "anthropic", model: "claude-sonnet-4", weight: 100, priority: 1, enabled: true }],
  },
];

describe("filterRoutes", () => {
  it("searches route, strategy, and target fields", () => {
    expect(filterRoutes(routes, { query: "ANTHROPIC", status: "all" })).toEqual([routes[1]]);
    expect(filterRoutes(routes, { query: "weighted", status: "all" })).toEqual([routes[0]]);
  });

  it("filters enabled state", () => {
    expect(filterRoutes(routes, { query: "", status: "enabled" })).toEqual([routes[0]]);
    expect(filterRoutes(routes, { query: "", status: "disabled" })).toEqual([routes[1]]);
  });
});
