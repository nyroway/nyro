import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "settings.tsx"), "utf8");

describe("Settings control-plane layout", () => {
  it("separates data-plane and control-plane configuration", () => {
    expect(source).toContain("DATA PLANE");
    expect(source).toContain("CONTROL PLANE");
    expect(source).toContain("Public Gateway URL");
    expect(source).toContain("Local Telemetry Retention");
  });

  it("uses two columns for telemetry cards and names the restart target", () => {
    expect(source).toContain("lg:grid-cols-2");
    expect(source).toContain("Restart Gateway to apply");
    expect(source).toContain("Restart Admin to apply");
  });

  it("removes unavailable backup and duplicate gateway-status UI", () => {
    expect(source).not.toContain("Config Backup");
    expect(source).not.toContain("Gateway Status");
    expect(source).not.toContain("export_config");
    expect(source).not.toContain("import_config");
  });
});
