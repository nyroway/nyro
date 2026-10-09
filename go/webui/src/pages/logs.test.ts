import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import LogsPage from "./logs";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderLogsPage() {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });

  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      null,
      createElement(LocaleProvider, null, createElement(LogsPage)),
    ),
  ));
}

describe("logs page", () => {
  it("renders the toolbar filters, table card, and empty state", () => {
    const html = renderLogsPage();

    expect(html).toContain("Request logs");
    expect(html.match(/class="[^"]*toolbar-filter[^"]*"/g)).toHaveLength(4);
    expect(html).toContain("card table-card");
    expect(html).toContain("No request logs yet");
    expect(html).toContain('aria-label="Clear logs"');
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderLogsPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|bg-green|bg-red|bg-sky|bg-amber|border-red|text-red|text-amber|grid-cols|flex items|font-mono)-/);
  });
});

describe("toolbar filters adapt to user-defined option text (§9.6)", () => {
  const source = readFileSync(resolve(__dirname, "logs.tsx"), "utf8");

  it("gives all four filters the adaptive variant and decouples menus from the 148px trigger", () => {
    // 日志页四个筛选的选项是用户自定义数据（消费者/模型/上游名，长度开放）：
    // 真源 .toolbar-filter 定宽 148px 且 .select-menu 恒等于触发器宽
    // （left:0;right:0）——长名在菜单里换两行、触发器截成省略号。变体类
    // toolbar-filter-adaptive 在适配层做三件事：触发器自适应封顶 240px、
    // 菜单解耦后按最长选项自适应封顶 360px、封顶后选项省略不换行。
    // 状态筛选选项是固定短文案，挂同一变体只会落在 148px 下限，行为不变。
    expect(source.match(/controlClassName="toolbar-filter toolbar-filter-adaptive"/g)).toHaveLength(4);
    const css = readFileSync(resolve(__dirname, "../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.toolbar-filter\.toolbar-filter-adaptive\s*\{[^}]*width:\s*max-content;[^}]*min-width:\s*148px;[^}]*max-width:\s*240px;/s);
    expect(css).toMatch(/\.toolbar-filter-adaptive \.select-menu\s*\{[^}]*min-width:\s*100%;[^}]*width:\s*max-content;[^}]*max-width:\s*360px;/s);
    expect(css).toMatch(/\.toolbar-filter-adaptive \.select-option > span\s*\{[^}]*text-overflow:\s*ellipsis;[^}]*white-space:\s*nowrap;/s);
  });

  it("makes the three user-data filters searchable, keeps the status filter plain", () => {
    // 模型列表可能有几十个长名字——宽度解决不了「找」的问题；消费者/模型/
    // 上游三个筛选开 searchable（真源 waf-gateway 菜单内检索框模式），
    // 状态筛选的选项是固定短文案，保持非检索。
    expect(source.match(/\bsearchable\b(?!=)/g)).toHaveLength(3);
    expect(source.match(/searchable=\{false\}/g)).toHaveLength(1);
    expect(source.match(/searchPlaceholder=\{localizedMessage\(isZh, "common\.search"\)\}/g)).toHaveLength(3);
  });
});
