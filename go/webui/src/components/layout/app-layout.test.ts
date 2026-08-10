import { createElement, type ComponentType, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { AppLayout } from "./app-layout";
import { Sidebar } from "./sidebar";

afterEach(() => {
  vi.unstubAllGlobals();
});

function withShellContext(children: ReactNode, locale: "en-US" | "zh-CN" = "en-US") {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: (key: string) => key === "nyro-locale" ? locale : null,
      setItem: () => undefined,
    },
  });

  return createElement(
    MemoryRouter,
    { initialEntries: ["/"] },
    createElement(LocaleProvider, null, children),
  );
}

function renderAppLayout() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });

  return renderToStaticMarkup(
    createElement(
      QueryClientProvider,
      { client: queryClient },
      withShellContext(createElement(AppLayout)),
    ),
  );
}

function renderSidebar(locale: "en-US" | "zh-CN" = "en-US") {
  const SidebarWithVersion = Sidebar as unknown as ComponentType<Record<string, unknown>>;
  return renderToStaticMarkup(withShellContext(createElement(SidebarWithVersion, {
    version: "2.0.0",
    services: [{ id: "control-plane", status: "running", listen: "127.0.0.1:19531" }],
    servicesError: false,
  }), locale));
}

describe("application shell", () => {
  it("keeps the global bar limited to search, readiness, and language controls", () => {
    const html = renderAppLayout();

    expect(html).toContain("Search pages or resources");
    expect(html).toContain("Status unknown");
    expect(html).toContain("Switch to Chinese");
    expect(html).not.toContain('aria-label="About Nyro"');
  });

  it("puts project resource links ahead of version metadata", () => {
    const html = renderSidebar();
    const github = html.indexOf("GitHub");
    const documentation = html.indexOf("Documentation");
    const product = html.indexOf("Nyro Console", documentation);
    const versionLabel = html.indexOf("Version", product);
    const version = html.indexOf("2.0.0", versionLabel);

    expect(github).toBeGreaterThanOrEqual(0);
    expect(documentation).toBeGreaterThan(github);
    expect(product).toBeGreaterThan(documentation);
    expect(versionLabel).toBeGreaterThan(product);
    expect(version).toBeGreaterThan(versionLabel);
  });

  it("does not duplicate the built-in service summary in the sidebar", () => {
    expect(renderSidebar()).not.toContain("Built-in services healthy");
  });

  it("uses the concise key label in the Chinese navigation", () => {
    const html = renderSidebar("zh-CN");

    expect(html).toContain('href="/api-keys"');
    expect(html).toContain('>密钥</a>');
    expect(html).not.toContain('>API 密钥</a>');
  });
});
