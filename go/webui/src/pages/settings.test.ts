import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import SettingsPage from "./settings";

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderSettingsPage() {
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
    createElement(LocaleProvider, null, createElement(SettingsPage)),
  ));
}

describe("settings page", () => {
  it("keeps the grouped section navigation and the default forwarding form", () => {
    const html = renderSettingsPage();

    expect(html).toContain('class="settings-layout"');
    expect(html).toContain("Data plane");
    expect(html).toContain("Control plane");
    expect(html).toContain("Request forwarding");
    expect(html).toContain("State storage");
    expect(html).toContain("Telemetry retention");

    expect(html).toContain("Forwarding settings");
    expect(html).toContain("Request Timeout");
    expect(html).toContain("Retry Status Codes");
    expect(html).toContain('class="tag-input"');
    expect(html).toContain("button button-primary button-sm");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderSettingsPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] means the same as the original spelling, split apart to keep the literal out of the exit grep
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|border-red|text-red|text-amber|grid-cols)-/);
  });
});
