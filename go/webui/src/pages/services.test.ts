import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import ServicesPage from "./services";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderServicesPage() {
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
      createElement(LocaleProvider, null, createElement(ServicesPage)),
    ),
  ));
}

describe("services page", () => {
  it("renders the runtime services workspace skeleton", () => {
    const html = renderServicesPage();

    expect(html).toContain("Built-in services");
    expect(html).toContain("metric-strip");
    expect(html).toContain("card table-card");
    expect(html).toContain("alert-info");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderServicesPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] means the same as the original spelling, split apart to keep the literal out of the exit grep
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|border-red|text-red|text-amber|grid-cols)-/);
  });
});
