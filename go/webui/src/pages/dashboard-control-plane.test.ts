import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "dashboard.tsx"), "utf8");

describe("Dashboard control-plane summary", () => {
  it("shows the Admin version, storage backend, and writable state", () => {
    expect(source).toContain("status?.version");
    expect(source).toContain("status?.backend");
    expect(source).toContain("status?.writable");
  });

  it("does not repeat the fixed status value", () => {
    expect(source).not.toContain("status?.status");
  });
});
