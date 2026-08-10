import { createElement, type ComponentType } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type { TestResult, Upstream } from "@/lib/types";
import ProvidersPage from "./providers";

const provider: Upstream = {
  id: "up-openai",
  name: "OpenAI Production",
  provider: "openai",
  protocol: "openai-responses",
  base_url: "https://api.openai.com/v1",
  credentials: { api_key: "secret" },
  models: ["gpt-4.1", "gpt-4.1-mini"],
  models_url: "https://api.openai.com/v1/models",
  proxy_url: "",
  enabled: true,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

function shell(children: React.ReactNode) {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });

  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  queryClient.setQueryData(["providers"], [provider]);
  queryClient.setQueryData(["provider-presets"], []);

  return createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/providers"] },
      createElement(LocaleProvider, null, children),
    ),
  );
}

describe("providers V2 page", () => {
  it("presents providers as an interactive V2 data surface", () => {
    const html = renderToStaticMarkup(shell(createElement(ProvidersPage)));

    expect(html).toContain("Upstream providers");
    expect(html).toContain("Refresh status");
    expect(html).toContain('tabindex="0"');
    expect(html).not.toContain("v2-row-actions");
    expect(html).toContain("Credentials are never shown in plaintext after saving");
  });

  it("renders a focused provider detail view with secondary actions", async () => {
    const module = await import("./providers");
    const Detail = (module as unknown as {
      ProviderDetailContent?: ComponentType<{
        provider: Upstream;
        result?: TestResult;
        onTest: () => void;
        onImport: () => void;
        onEdit: () => void;
        onDelete: () => void;
      }>;
    }).ProviderDetailContent;

    expect(Detail).toBeTypeOf("function");
    if (!Detail) return;

    const html = renderToStaticMarkup(shell(createElement(Detail, {
      provider,
      result: { success: true, latency_ms: 243, model: "gpt-4.1" },
      onTest: () => undefined,
      onImport: () => undefined,
      onEdit: () => undefined,
      onDelete: () => undefined,
    })));

    expect(html).toContain("Connection summary");
    expect(html).toContain("243ms");
    expect(html).toContain("Test connection");
    expect(html).toContain("Import model routes");
    expect(html).toContain("Edit configuration");
  });

  it("uses one V2 section structure for create and edit forms", async () => {
    const module = await import("./providers");
    const FormSections = (module as unknown as {
      ProviderFormSections?: ComponentType<{
        connection: React.ReactNode;
        credentials: React.ReactNode;
        discovery: React.ReactNode;
      }>;
    }).ProviderFormSections;

    expect(FormSections).toBeTypeOf("function");
    if (!FormSections) return;

    const html = renderToStaticMarkup(shell(createElement(FormSections, {
      connection: createElement("input", { name: "connection" }),
      credentials: createElement("input", { name: "credentials" }),
      discovery: createElement("input", { name: "discovery" }),
    })));

    expect(html).toContain('data-provider-form-section="connection"');
    expect(html).toContain('data-provider-form-section="credentials"');
    expect(html).toContain('data-provider-form-section="discovery"');
    expect(html).toContain("Connection");
    expect(html).toContain("Credentials");
    expect(html).toContain("Model discovery");
  });

  it("uses V2 feedback surfaces for connection logs and route imports", async () => {
    const module = await import("./providers");
    const exported = module as unknown as {
      ProviderTestLog?: ComponentType<{
        logs: Array<{ timestamp: string; level: "info" | "success" | "error"; message: string }>;
        emptyLabel: string;
      }>;
      RouteImportSummary?: ComponentType<{ preview: { discovered: number; create: string[]; skip: string[] } }>;
    };

    expect(exported.ProviderTestLog).toBeTypeOf("function");
    expect(exported.RouteImportSummary).toBeTypeOf("function");
    if (!exported.ProviderTestLog || !exported.RouteImportSummary) return;

    const logs = renderToStaticMarkup(createElement(exported.ProviderTestLog, {
      emptyLabel: "Waiting",
      logs: [
        { timestamp: "10:00:00", level: "info", message: "Testing endpoint" },
        { timestamp: "10:00:01", level: "success", message: "Connection available" },
      ],
    }));
    const summary = renderToStaticMarkup(shell(createElement(exported.RouteImportSummary, {
      preview: { discovered: 3, create: ["gpt-4.1", "gpt-4.1-mini"], skip: ["gpt-4o"] },
    })));

    expect(logs).toContain("v2-provider-test-log");
    expect(logs).toContain('data-log-level="success"');
    expect(summary).toContain("v2-provider-import-summary");
    expect(summary).toContain("gpt-4.1-mini");
  });
});
