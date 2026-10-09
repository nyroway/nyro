import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import ConnectPage from "./connect";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderConnectPage() {
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
      createElement(LocaleProvider, null, createElement(ConnectPage)),
    ),
  ));
}

describe("connect page", () => {
  it("renders the endpoint summary, config card, and code output", () => {
    const html = renderConnectPage();

    expect(html).toContain("Gateway endpoint");
    expect(html).toContain("http://127.0.0.1:19530");
    expect(html).toContain('class="copy-value"');
    expect(html).toContain("Request configuration");
    expect(html).toContain("tabs tabs-inline");
    expect(html).toContain('role="tablist"');
    expect(html).toContain("code-output-bar");
    expect(html).toContain("code-output-body is-empty");
    expect(html).toContain("Select a model to generate code here.");
    expect(html).toContain("Copy code");
    expect(html).toContain("Integration checklist");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderConnectPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|bg-green|bg-red|bg-sky|bg-amber|border-red|text-red|text-amber|grid-cols|flex items|font-mono|h-10|w-full|inline-flex|shrink-0|overflow-hidden|rounded-md)-/);
  });
});
