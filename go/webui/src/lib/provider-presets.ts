import type { ProviderPreset } from "./types";

export const CUSTOM_PROVIDER_PRESET_ID = "custom";

export function customProviderPreset(): ProviderPreset {
  return {
    id: CUSTOM_PROVIDER_PRESET_ID,
    name: "Custom",
    icon: CUSTOM_PROVIDER_PRESET_ID,
    defaultProtocol: "openai-chat",
    channels: [],
  };
}

export function isCustomProviderPreset(id?: string | null) {
  return id === CUSTOM_PROVIDER_PRESET_ID;
}

export function withCustomProviderPreset(presets: ProviderPreset[]): ProviderPreset[] {
  const custom = customProviderPreset();
  return [
    ...presets.filter((preset) => preset.id !== CUSTOM_PROVIDER_PRESET_ID),
    custom,
  ];
}
