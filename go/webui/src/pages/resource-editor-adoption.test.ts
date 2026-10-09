import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

function pageSource(name: string) {
  return readFileSync(resolve(__dirname, name), "utf8");
}

function countTags(source: string, component: string) {
  return source.match(new RegExp(`<${component}(?:\\s|>)`, "g"))?.length ?? 0;
}

/* 浮层两态（真源 new-webui）：编辑/新增表单一律右侧抽屉（ResourceEditorDrawer，
   drawer-wide），详情走 Inspector 抽屉；居中 modal 只留给探测/一次性令牌
   （ResourceEditorDialog）与确认（ConfirmDialog）。 */
describe("resource editor adoption", () => {
  it("opens provider create/edit as drawers, keeping the SSE test modal centered and details in a drawer", () => {
    const source = pageSource("providers.tsx");

    // create + edit editors are right-side drawers …
    expect(countTags(source, "ResourceEditorDrawer")).toBe(2);
    // … the SSE test/import progress modal stays a stackable modal (§9.2 ⑤⑥) …
    expect(countTags(source, "ResourceEditorDialog")).toBe(1);
    // … and row details stay in the Inspector drawer.
    expect(countTags(source, "Inspector")).toBe(1);
  });

  it("opens both model creation and editing as drawers", () => {
    const source = pageSource("models-v2.tsx");

    expect(countTags(source, "ResourceEditorDrawer")).toBe(2);
    expect(countTags(source, "ResourceEditorDialog")).toBe(0);
  });

  it("opens consumers and keys alike as drawers (§9.4)", () => {
    const source = pageSource("api-keys.tsx");

    // create/edit consumer + add/edit key
    expect(countTags(source, "ResourceEditorDrawer")).toBe(4);
    expect(countTags(source, "ResourceEditorDialog")).toBe(0);
    expect(source).not.toContain(["v2", "-consumer-editor"].join(""));  // 拆写避开出口 grep 的字面量
    // confirmations go through the ConfirmDialog wrapper; no raw ui/dialog here
    expect(source).toContain("ConfirmDialog");
    expect(source).not.toContain('from "@/components/ui/dialog"');
  });

  it("drops the wide-modal workaround everywhere", () => {
    for (const page of ["providers.tsx", "models-v2.tsx", "api-keys.tsx"]) {
      expect(pageSource(page)).not.toContain("modal-form-wide");
    }
  });
});
