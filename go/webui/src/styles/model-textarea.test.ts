import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const repoRoot = resolve(__dirname, "../..");

function read(path: string) {
  return readFileSync(resolve(repoRoot, path), "utf8");
}

function modelTextareaRule(css: string) {
  const match = css.match(/\.nyro-shadcn-input\.model-textarea\s*\{(?<body>[\s\S]*?)\n\}/);
  if (!match?.groups?.body) {
    throw new Error("Missing .nyro-shadcn-input.model-textarea rule");
  }
  return match.groups.body;
}

describe("manual model discovery textarea styles", () => {
  it("starts at one input-height row and keeps row stripes aligned", () => {
    const css = read("src/index.css");
    const providers = read("src/pages/providers.tsx");
    const rule = modelTextareaRule(css);

    expect(providers).not.toContain("min-h-[48px]");
    expect(providers).toContain("min-h-[40px]");
    expect(providers.match(/rows=\{1\}/g)).toHaveLength(2);
    expect(rule).toContain("line-height: 40px;");
    expect(rule).toContain("min-height: 40px !important;");
    expect(rule).toContain("rgba(15, 23, 42, 0.045) 40px");
    expect(rule).toContain("transparent 40px");
    expect(rule).toContain("transparent 80px");
  });
});
