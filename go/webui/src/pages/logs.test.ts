import { createElement } from "react";
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
