import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

function handleTemplateChangeSource() {
  const start = source.indexOf("function handleTemplateChange");
  const end = source.indexOf("\n  function handleEditProtocolChange", start);
  if (start < 0 || end < 0) {
    throw new Error("Could not locate handleTemplateChange");
  }
  return source.slice(start, end);
}

function handleProtocolChangeSource() {
  const start = source.indexOf("function handleProtocolChange");
  const end = source.indexOf("\n  function handleTemplateChange", start);
  if (start < 0 || end < 0) {
    throw new Error("Could not locate handleProtocolChange");
  }
  return source.slice(start, end);
}

describe("create provider preset switching", () => {
  it("resets create form fields instead of carrying values from the previous provider", () => {
    const body = handleTemplateChangeSource();

    expect(body).toContain('setModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));');
    expect(body).toContain("const protocol = isCustomProviderPreset(preset.id) ? protocolOptions[0].value : resolvePresetProtocol(preset);");
    expect(body).toContain("setForm({");
    expect(body).toContain("...emptyCreate,");
    expect(body).toContain('api_key: config.apiKey || "",');
    expect(body).toContain("credentials: defaultCredentialValues(credentialFieldsForPreset(preset)),");
    expect(body).not.toContain("setForm((prev)");
    expect(body).not.toContain("...prev");
    expect(body).not.toContain("prev.api_key");
    expect(body).not.toContain("prev.credentials");
  });

  it("treats Custom as manual protocol configuration so Base URL follows protocol defaults", () => {
    const body = handleProtocolChangeSource();

    expect(body).toContain("&& !isCustomProviderPreset(selectedPreset.id)");
    expect(body).toContain("base_url: config?.baseUrl || protocolUrl(protocol) || prev.base_url,");
    expect(body).not.toContain('base_url: config?.baseUrl || (preset ? "" : protocolUrl(protocol)) || prev.base_url,');
  });
});
