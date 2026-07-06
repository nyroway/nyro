import { describe, expect, it } from "vitest";

import type { ProviderPreset } from "./types";
import { CUSTOM_PROVIDER_PRESET_ID, withCustomProviderPreset } from "./provider-presets";

function preset(id: string): ProviderPreset {
  return {
    id,
    label: { en: id, zh: id },
    defaultProtocol: "openai-chatcompletions",
    channels: [],
  };
}

describe("withCustomProviderPreset", () => {
  it("appends Custom to backend presets without changing their order", () => {
    const out = withCustomProviderPreset([preset("openai"), preset("deepseek")]);

    expect(out.map((item) => item.id)).toEqual(["openai", "deepseek", CUSTOM_PROVIDER_PRESET_ID]);
    expect(out[out.length - 1]?.label).toEqual({ en: "Custom", zh: "自定义" });
  });

  it("deduplicates a backend custom preset and keeps the frontend Custom definition", () => {
    const backendCustom: ProviderPreset = {
      id: CUSTOM_PROVIDER_PRESET_ID,
      label: { en: "Backend Custom", zh: "后台自定义" },
      defaultProtocol: "anthropic-messages",
      channels: [{ id: "backend", label: { en: "Backend", zh: "Backend" }, baseUrls: {} }],
    };

    const out = withCustomProviderPreset([preset("openai"), backendCustom]);

    expect(out.map((item) => item.id)).toEqual(["openai", CUSTOM_PROVIDER_PRESET_ID]);
    expect(out[out.length - 1]?.label).toEqual({ en: "Custom", zh: "自定义" });
    expect(out[out.length - 1]?.defaultProtocol).toBe("openai-chatcompletions");
  });
});
