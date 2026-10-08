import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "confirm-dialog.tsx"), "utf8");
const providersSource = readFileSync(resolve(__dirname, "../../pages/providers.tsx"), "utf8");

describe("ConfirmDialog confirm button variant", () => {
  it("lets confirmClassName replace (not stack on) the default danger variant", () => {
    // 回归：旧写法 `button button-danger${confirmClassName ? ...}` 把 danger 与
    // 传入类叠加到同一按钮，两个背景类谁在样式表靠后谁生效——导入模型弹层
    // 传 button-primary 时确认按钮仍是红色。改为整体替换语义。
    expect(source).toContain('className={`button ${confirmClassName ?? "button-danger"}`}');
    expect(source).not.toMatch(/button-danger\$\{confirmClassName/);
  });

  it("keeps danger as the default for destructive confirmations", () => {
    expect(source).toContain("confirmClassName?: string");
    expect(source).toContain('?? "button-danger"');
  });

  it("renders the import-models confirm as primary blue via confirmClassName", () => {
    // 用户口径：确认导入＝蓝色，与新增提供商按钮同色（button-primary）。
    expect(providersSource).toContain('confirmClassName="button-primary"');
  });
});
