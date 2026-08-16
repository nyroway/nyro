import { describe, expect, it } from "vitest";

import {
  sameStateSettings,
  stateSettingsFromValues,
  stateSettingsPayload,
  validateStateSettings,
} from "./state-settings";

describe("state settings", () => {
  it("defaults absent settings to memory", () => {
    expect(stateSettingsFromValues(null, null)).toEqual({ type: "memory", url: "" });
  });

  it("accepts redis and rejects unsupported or fragmented URLs", () => {
    expect(validateStateSettings({ type: "redis", url: "redis://user:secret@host:6379/0" })).toBeNull();
    expect(validateStateSettings({ type: "redis", url: "rediss://host:6379/0" })).toBe("invalid");
    expect(validateStateSettings({ type: "redis", url: "redis://host:6379/0#" })).toBe("invalid");
  });

  it("clears the URL in a memory payload", () => {
    expect(stateSettingsPayload({ type: "memory", url: "redis://old:6379/0" })).toEqual({
      "state.type": "memory",
      "state.url": "",
    });
  });

  it("compares normalized drafts", () => {
    expect(
      sameStateSettings(
        { type: "redis", url: " redis://host:6379/0 " },
        { type: "redis", url: "redis://host:6379/0" },
      ),
    ).toBe(true);
  });
});
