import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

describe("provider preset icon rendering", () => {
  it("passes preset icon keys directly so Custom uses custom.svg instead of aliases", () => {
    // create picker + locked edit picker (§9.2 ②)
    expect(source.match(/iconKey=\{preset\.icon\}/g)).toHaveLength(2);
    expect(source).not.toContain("iconKey={selectedPreset?.icon}");
  });
});
