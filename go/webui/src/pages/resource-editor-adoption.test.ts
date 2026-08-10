import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function pageSource(name: string) {
  return readFileSync(resolve(__dirname, name), "utf8");
}

function countTags(source: string, component: string) {
  return source.match(new RegExp(`<${component}(?:\\s|>)`, "g"))?.length ?? 0;
}

describe("resource editor adoption", () => {
  it("uses centered editors for provider forms while keeping provider details in a drawer", () => {
    const source = pageSource("providers.tsx");

    expect(countTags(source, "ResourceEditorDialog")).toBe(2);
    expect(countTags(source, "Inspector")).toBe(1);
  });

  it("uses centered editors for both model creation and editing", () => {
    const source = pageSource("models-v2.tsx");

    expect(countTags(source, "ResourceEditorDialog")).toBe(2);
    expect(countTags(source, "Inspector")).toBe(0);
  });

  it("uses centered editors for consumers without replacing compact key dialogs", () => {
    const source = pageSource("api-keys.tsx");

    expect(countTags(source, "ResourceEditorDialog")).toBe(2);
    expect(countTags(source, "Inspector")).toBe(0);
    expect(source).not.toContain("v2-consumer-editor");
    expect(countTags(source, "Dialog")).toBeGreaterThanOrEqual(3);
  });
});
