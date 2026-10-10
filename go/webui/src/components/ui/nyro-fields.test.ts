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
    // Baseline trigger: button.field-control + span.selected-value + span.control-icon chevron
    expect(html).toContain('class="field-control"');
    expect(html).toContain('<span class="selected-value">openai-chat</span>');
    expect(html).toContain('<span class="control-icon">');
    // No menu rendered while closed; no input inside the trigger (the baseline searchable search box lives inside the menu)
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
    // No menu rendered while closed
    expect(html).not.toContain("custom-multi-options");
  });
});

describe("select menus render in place, never through a body portal", () => {
  it("keeps the menu inside .select-control so baseline CSS stacking applies", () => {
    // Regression: a portal + fixed menu inside a drawer gets covered by the
    // drawer content (elementFromPoint probing failed at all five points, the
    // root cause of single/multi select "not working"). The baseline structure
    // is the menu as a child of .select-control/.custom-multi; opening is just
    // an .open class.
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
    // The baseline waf-gateway default-model picker: the search box is the
    // menu-top .searchable-select-search > input.searchable-select-input
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
    // Baseline options are divs (transparent background — the menu's white is
    // the option's ground); the React side uses a button, whose UA default
    // buttonface light-gray background would gray out every unselected option,
    // so an explicit transparent background is required — only
    // .selected/:hover/keyboard .active get the gray ground.
    const css = readFileSync(resolve(__dirname, "../../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.select-option,\s*\.custom-multi-option\s*\{[^}]*width: 100%;[^}]*background: transparent;/s);
  });

  it("only highlights an option after real keyboard navigation", () => {
    // On open, activeIndex is merely preseeded on the selected option (so Enter
    // picks it directly) and does not render the .active gray background; the
    // keyboard highlight appears only after arrow-key navigation.
    expect(source).toContain('index === activeIndex && navigated && "active"');
    expect(source).toContain("setNavigated(false)");
    expect(source).toContain("setNavigated(true)");
  });

  it("closes on outside pointer-down and toggles on trigger click", () => {
    expect(source).toContain('document.addEventListener("mousedown", onPointerDown)');
    expect(source).toContain('onClick={() => (open ? closeMenu() : openMenu())}');
    // The multi-select trigger is also a toggle (baseline toggleCustomMulti)
    expect(source).toContain("onClick={() => setOpen((current) => !current)}");
  });

  it("keeps the multi menu open while toggling options (baseline toggleCustomMultiOption)", () => {
    // Single-select: clicking an option selects it and closes; multi-select: clicking only toggles the selection, the menu stays open.
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

    // Baseline .help: a 16px round question mark (baseline nyro-ui.css),
    // focusable (button); the tip goes through span.help-tip (switches to a
    // fixed layer on hover, see helpTipLayout).
    expect(html).toContain('<button type="button" class="help" aria-label="help">?');
    expect(html).toContain('<span class="help-tip" role="tooltip">Auto-fetch the model list</span>');
  });

  it("neutralizes the button UA padding that would burst the 16px circle", () => {
    // Under the global *{box-sizing:border-box}, the button UA default
    // padding(1px 6px) would squeeze the content box down to 2px wide with the
    // question mark spilling outside the circle — the adaptation layer must
    // zero the padding and flatten the line-height.
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
    // Icon hugging the left edge (the ? button at the drawer's left edge): centering would push the left half out of the viewport → clamp to the 8px safe bound
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
    // On hover/focus the tip switches to fixed (escaping the overflow clipping
    // of .drawer-body/.card); the .drawer open state's transform rewrites the
    // fixed containing block — the "zero out, read the baseline, then
    // compensate" two-pass approach guarantees the viewport target under any
    // containing block.
    expect(source).toContain("onMouseEnter={placeTip}");
    expect(source).toContain("onFocus={placeTip}");
    expect(source).toContain('tip.style.position = "fixed"');
    expect(source).toContain("left - base.left");
    expect(source).toContain("top - base.top");
  });
});
