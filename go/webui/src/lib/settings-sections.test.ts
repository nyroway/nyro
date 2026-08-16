import { describe, expect, it } from "vitest";

import { SETTINGS_SECTIONS } from "./settings-sections";

describe("settings navigation", () => {
  it("groups gateway settings under the data plane", () => {
    expect(SETTINGS_SECTIONS.filter((item) => item.group === "data-plane").map((item) => item.id)).toEqual([
      "forwarding",
      "state",
      "logs",
      "metrics",
      "traces",
    ]);
  });

  it("groups admin settings under the control plane", () => {
    expect(SETTINGS_SECTIONS.filter((item) => item.group === "control-plane").map((item) => item.id)).toEqual([
      "public",
      "retention",
    ]);
  });
});
