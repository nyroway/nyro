import { describe, expect, it } from "vitest";

import type { Route, Upstream } from "@/lib/types";
import { filterRoutes, toggleTargetsProviders } from "./model-view-model";

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

const upstreams: Upstream[] = [
  { id: "openai", name: "OpenAI", protocol: "openai", base_url: "https://api.openai.com", enabled: true },
  { id: "anthropic", name: "Anthropic", protocol: "anthropic", base_url: "https://api.anthropic.com", enabled: true },
  { id: "gemini", name: "Gemini", protocol: "gemini", base_url: "https://api.gemini.com", enabled: true },
];

describe("toggleTargetsProviders", () => {
  it("appends an empty target for each newly checked provider", () => {
    const targets = toggleTargetsProviders([], [upstreams[0], upstreams[2]]);

    expect(targets).toEqual([
      { upstream_id: "openai", model: "", weight: 0, priority: 1, enabled: true },
      { upstream_id: "gemini", model: "", weight: 0, priority: 1, enabled: true },
    ]);
  });

  it("keeps edited targets of checked providers untouched", () => {
    const existing = [{ upstream_id: "openai", model: "gpt-5", weight: 60, priority: 1, enabled: false }];
    const targets = toggleTargetsProviders(existing, [upstreams[0], upstreams[1]]);

    expect(targets[0]).toEqual(existing[0]);
    expect(targets[1]).toEqual({ upstream_id: "anthropic", model: "", weight: 0, priority: 1, enabled: true });
  });

  it("removes every target of an unchecked provider but keeps duplicates of others", () => {
    const existing = [
      { upstream_id: "openai", model: "gpt-5", weight: 50, priority: 1, enabled: true },
      { upstream_id: "openai", model: "gpt-5-mini", weight: 20, priority: 1, enabled: true },
      { upstream_id: "anthropic", model: "claude", weight: 30, priority: 1, enabled: true },
    ];
    const targets = toggleTargetsProviders(existing, [upstreams[1]]);

    expect(targets).toEqual([existing[2]]);
  });

  it("restores a re-checked provider as a fresh empty target", () => {
    const edited = [{ upstream_id: "openai", model: "gpt-5", weight: 100, priority: 1, enabled: true }];
    const removed = toggleTargetsProviders(edited, []);
    const restored = toggleTargetsProviders(removed, [upstreams[0]]);

    expect(restored).toEqual([{ upstream_id: "openai", model: "", weight: 0, priority: 1, enabled: true }]);
  });
});
