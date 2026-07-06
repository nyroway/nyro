import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

describe("provider form placeholders", () => {
  it("uses concise Chinese and English example prefixes consistently", () => {
    for (const expected of [
      "如：OpenAI 生产环境",
      "e.g. OpenAI Production",
      "如：sk-...",
      "e.g. sk-...",
      "如：https://api.openai.com/v1",
      "e.g. https://api.openai.com/v1",
      "如：http://127.0.0.1:7890",
      "e.g. http://127.0.0.1:7890",
      "如：https://api.openai.com/v1/models",
      "e.g. https://api.openai.com/v1/models",
      "每行一个模型名，如：gpt-4o",
      "One model per line, e.g. gpt-4o",
    ]) {
      expect(source).toContain(expected);
    }

    expect(source).not.toContain("例如 ");
    expect(source).not.toContain("For example");
  });
});
