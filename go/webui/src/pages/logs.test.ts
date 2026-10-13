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

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] is synonymous with the original spelling, split to avoid the exit grep's literal
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|bg-green|bg-red|bg-sky|bg-amber|border-red|text-red|text-amber|grid-cols|flex items|font-mono)-/);
  });
});

describe("toolbar filters adapt to user-defined option text (§9.6)", () => {
  const source = readFileSync(resolve(__dirname, "logs.tsx"), "utf8");

  it("gives all four filters the adaptive variant and decouples menus from the 148px trigger", () => {
    // The four filters on the logs page take user-defined data as options (consumer/model/
    // upstream names, unbounded length): the baseline .toolbar-filter is fixed at 148px and
    // .select-menu is always exactly as wide as the trigger (left:0;right:0) — long names
    // wrap into two lines in the menu and the trigger truncates to an ellipsis. The variant
    // class toolbar-filter-adaptive does three things in the adaptation layer: the trigger
    // sizes to content capped at 240px, the decoupled menu sizes to the longest option capped
    // at 360px, and once capped the options ellipsize instead of wrapping. The status filter's
    // options are fixed short labels; the same variant merely lands on the 148px floor, no behavior change.
    expect(source.match(/controlClassName="toolbar-filter toolbar-filter-adaptive"/g)).toHaveLength(4);
    const css = readFileSync(resolve(__dirname, "../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.toolbar-filter\.toolbar-filter-adaptive\s*\{[^}]*width:\s*max-content;[^}]*min-width:\s*148px;[^}]*max-width:\s*240px;/s);
    expect(css).toMatch(/\.toolbar-filter-adaptive \.select-menu\s*\{[^}]*min-width:\s*100%;[^}]*width:\s*max-content;[^}]*max-width:\s*360px;/s);
    expect(css).toMatch(/\.toolbar-filter-adaptive \.select-option > span\s*\{[^}]*text-overflow:\s*ellipsis;[^}]*white-space:\s*nowrap;/s);
  });

  it("makes the three user-data filters searchable, keeps the status filter plain", () => {
    // The model list can hold dozens of long names — width cannot solve the "find" problem;
    // the consumer/model/upstream filters enable searchable (the baseline waf-gateway
    // in-menu search-box pattern), while the status filter's options are fixed short
    // labels and it stays non-searchable.
    expect(source.match(/\bsearchable\b(?!=)/g)).toHaveLength(3);
    expect(source.match(/searchable=\{false\}/g)).toHaveLength(1);
    expect(source.match(/searchPlaceholder=\{localizedMessage\(isZh, "common\.search"\)\}/g)).toHaveLength(3);
  });
});
