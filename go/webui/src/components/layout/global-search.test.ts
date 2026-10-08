import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { GlobalSearch } from "./global-search";

const source = readFileSync(resolve(__dirname, "global-search.tsx"), "utf8");

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderSearch() {
  vi.stubGlobal("window", {
    localStorage: { getItem: () => null, setItem: () => undefined },
  });
  const queryClient = new QueryClient({ defaultOptions: { queries: { enabled: false, retry: false } } });
  return renderToStaticMarkup(
    createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(
        MemoryRouter,
        null,
        createElement(LocaleProvider, null, createElement(GlobalSearch)),
      ),
    ),
  );
}

/* 全局检索（真源 #globalSearch）：不是弹层——顶栏搜索框原位展开面板。
   基线结构：label.global-search-field 真输入框 + .global-search-panel 内
   .search-group/.search-hit/.search-hit-meta/.search-empty。 */
describe("global search (in-place panel)", () => {
  it("renders a real input field inside the topbar search box", () => {
    const html = renderSearch();

    expect(html).toContain('class="global-search"');
    expect(html).toContain("<label");
    expect(html).toContain('class="global-search-field"');
    expect(html).toContain('type="search"');
    expect(html).toContain('placeholder="Search pages or resources"');
    // 基线是输入框，不是唤起弹层的按钮
    expect(html).not.toContain("command-panel");
  });

  it("opens a grouped hit panel below the field, never a modal", () => {
    const html = renderSearch();

    expect(html).toContain('class="global-search-panel"');
    expect(html).toContain('class="search-empty"');
    expect(html).toContain('class="search-group"');
    expect(html).toContain('class="search-group-label"');
    expect(html).toContain('class="search-hit"');
    expect(html).toContain('class="search-hit-icon"');
    expect(html).toContain('class="search-hit-copy"');
    expect(html).toContain('class="search-hit-meta"');
    // 页面/动作两组有命中，不算空态
    expect(html).not.toContain("global-search is-empty");
  });

  it("wires focus-open, escape, outside-click, ⌘K, and navigation in the source", () => {
    expect(source).toContain("onFocus={() => setOpen(true)}");
    expect(source).toContain('event.key === "Escape"');
    expect(source).toContain("pointerdown");
    expect(source).toContain('"k"');
    expect(source).toContain("navigate(href)");
    expect(source).toContain("buildResourceCommands");
  });
});
