import { describe, expect, it } from "vitest";
import { buildResourceCommands } from "./command-items";

describe("buildResourceCommands", () => {
  it("maps providers, routes, and consumers to stable focus URLs", () => {
    expect(
      buildResourceCommands(
        [{ id: "up-1", name: "OpenAI", provider: "openai", enabled: true }],
        [{ id: "route-1", model: "gpt-4.1", enabled: true, enable_auth: true }],
        [{ id: "consumer-1", name: "Console", enabled: true, keys: [] }],
      ).map(({ label, href }) => ({ label, href })),
    ).toEqual([
      { label: "OpenAI", href: "/providers?focus=up-1" },
      { label: "gpt-4.1", href: "/models?focus=route-1" },
      { label: "Console", href: "/api-keys?focus=consumer-1" },
    ]);
  });
});
