import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repoRoot = resolve(__dirname, "../..");

function read(path: string) {
  return readFileSync(resolve(repoRoot, path), "utf8");
}

describe("manual model tag input styles", () => {
  it("uses a wrapping token container instead of a fixed-height textarea", () => {
    const css = read("src/styles/v2.css");
    const providers = read("src/pages/providers.tsx");
    const legacyCss = read("src/index.css");

    expect(providers).toContain("ModelTagInput");
    expect(providers).not.toContain("model-textarea");
    expect(css).toContain(".v2-model-tag-input {");
    expect(css).toContain("flex-wrap: wrap;");
    expect(css).toContain(".v2-model-tag-input:focus-within");
    expect(css).toContain(".v2-model-tag-input-remove:focus-visible");
    expect(legacyCss).not.toContain(".nyro-shadcn-input.model-textarea");
  });
});
