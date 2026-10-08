import { createElement, type FunctionComponent } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { helpTipLayout } from "@/lib/help-tip-layout";
import { NyroHelpHint, NyroMultiCheck, NyroSearchSelect, type NyroMultiCheckProps, type NyroSearchSelectProps } from "./nyro-fields";

const source = readFileSync(resolve(__dirname, "nyro-fields.tsx"), "utf8");

const SingleSelect = NyroSearchSelect as FunctionComponent<NyroSearchSelectProps<string>>;
const MultiCheck = NyroMultiCheck as FunctionComponent<NyroMultiCheckProps<string>>;

describe("select family renders the new-webui baseline markup", () => {
  it("renders the single-select trigger as a baseline button, not an input", () => {
    const html = renderToStaticMarkup(createElement(SingleSelect, {
      label: "Protocol",
      options: ["openai-chat", "anthropic-messages"],
      value: "openai-chat",
      onChange: () => undefined,
      getOptionLabel: (option) => option,
      searchable: false,
    }));

    expect(html).toContain('class="select-control"');
    // 基线触发器：button.field-control + span.selected-value + span.control-icon 折角
    expect(html).toContain('class="field-control"');
    expect(html).toContain('<span class="selected-value">openai-chat</span>');
    expect(html).toContain('<span class="control-icon">');
    // 关闭态不渲染菜单；触发器里没有输入框（基线 searchable 的检索框在菜单内）
    expect(html).not.toContain("select-menu");
    expect(html).not.toMatch(/<input/);
  });

  it("mutes the placeholder value when nothing is selected", () => {
    const html = renderToStaticMarkup(createElement(SingleSelect, {
      label: "Model",
      options: ["gpt-4.1"],
      value: null,
      onChange: () => undefined,
      getOptionLabel: (option) => option,
      placeholder: "Select model",
      searchable: false,
    }));

    expect(html).toContain('class="selected-value is-placeholder"');
    expect(html).toContain("Select model");
  });

  it("renders the multi trigger with baseline tag chips and remove marks", () => {
    const html = renderToStaticMarkup(createElement(MultiCheck, {
      label: "Upstreams",
      options: ["deepseek", "openai"],
      selected: ["deepseek"],
      onChange: () => undefined,
      getOptionValue: (option) => option,
      getOptionLabel: (option) => option,
      placeholder: "Select upstreams",
    }));

    expect(html).toContain('class="custom-multi"');
    expect(html).toContain("field-control tags-control multi-display");
    expect(html).toContain('<span class="tag">');
    expect(html).toContain('class="tag-remove"');
    expect(html).toContain('<span class="control-icon">');
    // 关闭态不渲染菜单
    expect(html).not.toContain("custom-multi-options");
  });
});

describe("select menus render in place, never through a body portal", () => {
  it("keeps the menu inside .select-control so baseline CSS stacking applies", () => {
    // 回归：门户 + fixed 菜单在抽屉内被抽屉内容遮挡（elementFromPoint 探测
    // 五点全失，单选/多选“不生效”的根因）。基线结构是菜单作为
    // .select-control/.custom-multi 的子元素，打开只是一个 .open 类。
    expect(source).not.toContain("createPortal");
    expect(source).not.toContain("useFloatingMenu");
    expect(source).not.toContain('position: "fixed"');
    expect(source).not.toContain("zIndex");
    expect(source).not.toContain("document.body");
  });

  it("renders the menu and options with the baseline class vocabulary", () => {
    expect(source).toContain('clsx("select-menu", filtered.length === 0 && "is-empty")');
    expect(source).toContain('"select-option"');
    expect(source).toContain('"custom-multi-options"');
    expect(source).toContain('"custom-multi-option"');
    expect(source).toContain('"multi-check"');
  });

  it("keeps the searchable search box inside the menu like the baseline", () => {
    // 基线 waf-gateway 默认模型选择：检索框是菜单顶部的
    // .searchable-select-search > input.searchable-select-input
    expect(source).toContain('"searchable-select-search"');
    expect(source).toContain('"searchable-select-input"');
    expect(source).toContain('"searchable-select-empty"');
  });
});

describe("select open/close behavior follows the baseline JS semantics", () => {
  it("opens one select at a time (baseline toggleSelect closes the others)", () => {
    expect(source).toContain("const openSelectClosers = new Set<() => void>()");
    expect(source).toContain("function claimSelectMenu(close: () => void)");
    expect(source).toContain("openSelectClosers.add(close)");
    expect(source).toContain("openSelectClosers.delete(close)");
  });

  it("keeps unselected options on the white menu background", () => {
    // 基线选项是 div（背景透明，菜单白底即选项底色）；React 侧用 button
    // 承载，button 的 UA 默认 buttonface 浅灰底会让所有未选中项发灰，
    // 需显式透明——只有 .selected/:hover/键盘 .active 才是灰底。
    const css = readFileSync(resolve(__dirname, "../../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.select-option,\s*\.custom-multi-option\s*\{[^}]*width: 100%;[^}]*background: transparent;/s);
  });

  it("only highlights an option after real keyboard navigation", () => {
    // 打开菜单时 activeIndex 只是预置在选中项上（供回车直选），不渲染
    // .active 灰底；方向键导航后才出现键盘高亮。
    expect(source).toContain('index === activeIndex && navigated && "active"');
    expect(source).toContain("setNavigated(false)");
    expect(source).toContain("setNavigated(true)");
  });

  it("closes on outside pointer-down and toggles on trigger click", () => {
    expect(source).toContain('document.addEventListener("mousedown", onPointerDown)');
    expect(source).toContain('onClick={() => (open ? closeMenu() : openMenu())}');
    // 多选触发器也是开关（基线 toggleCustomMulti）
    expect(source).toContain("onClick={() => setOpen((current) => !current)}");
  });

  it("keeps the multi menu open while toggling options (baseline toggleCustomMultiOption)", () => {
    // 单选点击选项即选中并关闭；多选点击只切换选中，菜单保持打开。
    expect(source).toContain("onClick={() => selectOption(option)}");
    expect(source).toMatch(/onMouseDown=\{\(event\) => event\.preventDefault\(\)\}\s*\n\s*onClick=\{\(\) => toggle\(option\)\}/);
  });

  it("supports keyboard navigation on the trigger and the in-menu search input", () => {
    expect(source).toContain('event.key === "ArrowDown"');
    expect(source).toContain('event.key === "ArrowUp"');
    expect(source).toContain('event.key === "Escape"');
    expect(source).toContain('aria-activedescendant');
    expect(source).toContain("scrollIntoView({ block: \"nearest\" })");
  });
});

describe("NyroHelpHint renders the baseline .help button with a hover tip", () => {
  it("renders a focusable circle button carrying the tip payload", () => {
    const html = renderToStaticMarkup(createElement(NyroHelpHint, { text: "Auto-fetch the model list" }));

    // 基线 .help：16px 圆形问号（真源 nyro-ui.css），可聚焦（button），
    // 提示走 span.help-tip（悬停时切 fixed 固定层，见 helpTipLayout）。
    expect(html).toContain('<button type="button" class="help" aria-label="help">?');
    expect(html).toContain('<span class="help-tip" role="tooltip">Auto-fetch the model list</span>');
  });

  it("neutralizes the button UA padding that would burst the 16px circle", () => {
    // 全局 *{box-sizing:border-box} 下按钮 UA 默认 padding(1px 6px) 会把
    // 内容盒挤到 2px 宽、问号溢出圆外——适配层必须归零 padding 并压平行高。
    const css = readFileSync(resolve(__dirname, "../../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.help\s*\{[^}]*padding:\s*0/s);
    expect(css).toMatch(/\.help\s*\{[^}]*line-height:\s*1/s);
  });
});

describe("helpTipLayout (fixed-layer tip placement)", () => {
  const viewport = { w: 1440, h: 813 };

  it("centers the tip above the icon with a 6px gap", () => {
    expect(helpTipLayout({ left: 300, top: 400, right: 316, bottom: 416 }, { w: 240, h: 40 }, viewport))
      .toEqual({ left: 188, top: 354 });
  });

  it("clamps the tip inside the viewport when the icon hugs an edge", () => {
    // 图标贴左缘（抽屉左缘的 ? 钮）：居中会让左半出视口 → 钳到 8px 安全界
    expect(helpTipLayout({ left: 20, top: 400, right: 36, bottom: 416 }, { w: 240, h: 40 }, viewport).left).toBe(8);
    expect(helpTipLayout({ left: 1420, top: 400, right: 1436, bottom: 416 }, { w: 240, h: 40 }, viewport).left).toBe(1440 - 240 - 8);
  });

  it("flips below the icon when there is no room above", () => {
    expect(helpTipLayout({ left: 300, top: 20, right: 316, bottom: 36 }, { w: 240, h: 40 }, viewport).top).toBe(36 + 6);
  });

  it("keeps the tip at the safe margin when it is wider than the viewport", () => {
    expect(helpTipLayout({ left: 300, top: 400, right: 316, bottom: 416 }, { w: 2000, h: 40 }, viewport).left).toBe(8);
  });
});

describe("NyroHelpHint escapes clipping ancestors while shown", () => {
  it("switches the tip to a fixed layer and compensates for any containing block", () => {
    // 悬停/聚焦时切 fixed（脱离 .drawer-body/.card 的 overflow 裁剪）；
    // .drawer 打开态的 transform 会改写 fixed 包含块——“归零读基线再补偿”
    // 两遍法保证任何包含块下都落在视口目标。
    expect(source).toContain("onMouseEnter={placeTip}");
    expect(source).toContain("onFocus={placeTip}");
    expect(source).toContain('tip.style.position = "fixed"');
    expect(source).toContain("left - base.left");
    expect(source).toContain("top - base.top");
  });
});
