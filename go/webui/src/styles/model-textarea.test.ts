import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repoRoot = resolve(__dirname, "../..");

function read(path: string) {
  return readFileSync(resolve(repoRoot, path), "utf8");
}

describe("manual model tag input styles (§9.2 ④)", () => {
  it("uses the baseline tags-control token container instead of a fixed-height textarea", () => {
    const appCss = read("src/styles/nyro-app.css");
    const providers = read("src/pages/providers.tsx");
    const tagInput = read("src/features/providers/model-tag-input.tsx");

    expect(providers).toContain("ModelTagInput");
    expect(providers).not.toContain("model-textarea");
    expect(tagInput).toContain('className="field-control tags-control model-tag-input"');
    expect(tagInput).toContain('className="tag-input"');
    // app-level reskin on top of the baseline .tag: mono model code + icon remove button
    expect(appCss).toContain(".model-tag-list {");
    expect(appCss).toContain(".model-tag-input .tag code {");
    expect(appCss).toContain(".tag-remove:focus-visible {");
  });
});
