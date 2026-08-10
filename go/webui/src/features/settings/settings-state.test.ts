import { describe, expect, it } from "vitest";

import { isSettingsDirty } from "./settings-state";

describe("settings dirty state", () => {
  it("treats reordered retry status codes as the same settings value", () => {
    expect(isSettingsDirty(
      { requestTimeout: "120s", retryStatusCodes: [429, 503] },
      { requestTimeout: "120s", retryStatusCodes: [503, 429] },
    )).toBe(false);
  });

  it("normalizes surrounding whitespace but detects a changed value", () => {
    expect(isSettingsDirty({ endpoint: " https://otel.example.com " }, { endpoint: "https://otel.example.com" })).toBe(false);
    expect(isSettingsDirty({ endpoint: "https://otel-a.example.com" }, { endpoint: "https://otel-b.example.com" })).toBe(true);
  });
});
