import { describe, expect, it } from "vitest";

import type { Upstream } from "@/lib/types";
import { filterProviders } from "./provider-view-model";

const providers: Upstream[] = [
  {
    id: "openai-primary",
    name: "OpenAI Production",
    provider: "openai",
    protocol: "openai-responses",
    base_url: "https://api.openai.com/v1",
    models: ["gpt-5", "gpt-4.1"],
    enabled: true,
  },
  {
    id: "anthropic-backup",
    name: "Anthropic Backup",
    provider: "anthropic",
    protocol: "anthropic-messages",
    base_url: "https://api.anthropic.com",
    models: ["claude-sonnet-4"],
    enabled: false,
  },
];

describe("filterProviders", () => {
  it("matches provider identity and connection fields case-insensitively", () => {
    expect(filterProviders(providers, { query: "RESPONSES", protocol: "all", enabled: "all" }))
      .toEqual([providers[0]]);
    expect(filterProviders(providers, { query: "anthropic.com", protocol: "all", enabled: "all" }))
      .toEqual([providers[1]]);
  });

  it("combines protocol and enabled filters without mutating source order", () => {
    const result = filterProviders(providers, {
      query: "",
      protocol: "anthropic-messages",
      enabled: "disabled",
    });

    expect(result).toEqual([providers[1]]);
    expect(providers.map((provider) => provider.id)).toEqual(["openai-primary", "anthropic-backup"]);
  });
});
