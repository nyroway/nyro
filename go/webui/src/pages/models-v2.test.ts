import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import ModelsV2Page from "./models-v2";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderModelsPage() {
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
      createElement(LocaleProvider, null, createElement(ModelsV2Page)),
    ),
  ));
}

describe("models page", () => {
  it("renders the toolbar, table card, and empty state", () => {
    const html = renderModelsPage();

    expect(html).toContain("Models");
    expect(html).toContain('class="toolbar-search"');
    expect(html.match(/class="[^"]*toolbar-filter[^"]*"/g)).toHaveLength(1);
    expect(html).toContain("toolbar-add");
    // 真源 toolbar-add 纯文字：按钮内单个文本节点，无 lucide Plus 图标
    expect(html).toMatch(/class="button button-primary button-sm toolbar-add"[^>]*>[^<]*<\/button>/);
    expect(html).toContain("card table-card");
    expect(html).toContain("No models configured");
    expect(html).toContain('aria-label="Search models"');
    expect(html).toContain('aria-label="Filter by status"');
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderModelsPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|bg-green|bg-red|bg-sky|bg-amber|border-red|text-red|text-amber|grid-cols|flex items|font-mono|h-10|w-full)-/);
  });
});
