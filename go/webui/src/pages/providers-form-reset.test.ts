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

describe("create provider preset switching", () => {
  it("resets create form fields instead of carrying values from the previous provider", () => {
    const body = handleTemplateChangeSource();

    expect(body).toContain('setModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));');
    expect(body).toContain("setForm({");
    expect(body).toContain("...emptyCreate,");
    expect(body).toContain('api_key: config.apiKey || "",');
    expect(body).toContain("credentials: defaultCredentialValues(credentialFieldsForPreset(preset)),");
    expect(body).not.toContain("setForm((prev)");
    expect(body).not.toContain("...prev");
    expect(body).not.toContain("prev.api_key");
    expect(body).not.toContain("prev.credentials");
  });
});
