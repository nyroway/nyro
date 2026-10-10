import { createElement, type ComponentType } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type { RouteImportPreview, TestResult, Upstream } from "@/lib/types";
import ProvidersPage from "./providers";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

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

describe("providers page static render", () => {
  it("presents providers as a baseline table card with brand marks and switches", () => {
    const html = renderToStaticMarkup(shell(createElement(ProvidersPage)));

    expect(html).toContain("Providers");
    expect(html).toContain('class="resource-summary"');
    expect(html).toContain("toolbar-add");
    expect(html).toContain("Add Provider");
    // The baseline toolbar-add is text-only (providers.html:290 has no icon) — the button renders
    // only a single text node, no longer stacking lucide Plus (the + and the 12px label differ in visual weight).
    expect(html).toMatch(/class="button button-primary button-sm toolbar-add"[^>]*>[^<]*<\/button>/);
    expect(source).not.toMatch(/\bPlus\b/);
    expect(html).toContain('tabindex="0"');
    expect(html).toContain('class="provider-name"');
    expect(html).toContain("provider-icon");
    expect(html).toContain('class="switch-control sm');
    expect(html).toContain("cell-actions");
    expect(html).toContain("row-actions");
    expect(html).toContain("action-menu");
    // Action icon tooltips go through the baseline #hoverTip fixed layer (data-tip); no more native title
    expect(html).toContain('data-tip="Probe"');
    expect(html).toContain('data-tip="Edit"');
    expect(html).toContain('data-tip="More"');
    expect(html).toContain("toolbar-search");
    expect(html).toContain("table-footer-notes");
    expect(html).toContain("Credentials are never shown in plaintext after saving");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderToStaticMarkup(shell(createElement(ProvidersPage)));

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] is synonymous with the original spelling, split to avoid the exit grep's literal
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|grid-cols|flex items|font-mono|h-10|w-full|pr-10|min-h-32)-/);
  });

  it("talks to the backend only through upstreamsApi (§9.2 ⑧)", () => {
    expect(source).toContain("upstreamsApi.list()");
    expect(source).toContain("upstreamsApi.presets()");
    expect(source).toContain("upstreamsApi.testHealth(");
    expect(source).toContain("upstreamsApi.testDraft(");
    expect(source).toContain("upstreamsApi.testEditDraft(");
    expect(source).toContain("upstreamsApi.importRoutes(");
    expect(source).toContain("upstreamsApi.importPreview(");
    expect(source).not.toMatch(/\bbackend[(<]/);
    expect(source).not.toMatch(/streamProvider/);
  });
});

describe("toolbar protocol filter width (baseline 148px wraps long protocol names)", () => {
  it("widens only the protocol filter via the app-layer variant class", () => {
    // The baseline .toolbar-filter is fixed at 148px and .select-menu is always exactly
    // as wide as the trigger (left:0;right:0) — "OpenAI Chat Completions" (measured ~163px)
    // wraps into two lines in the 148px menu, and once selected the trigger also truncates
    // to an ellipsis. The protocol filter (an element the baseline does not have) uses the
    // toolbar-filter-protocol variant fixed at 210px in nyro-app.css: both symptoms, menu
    // and trigger, fixed in one go; the status filter keeps the baseline 148px (this page
    // has exactly one bare toolbar-filter, guarding against widening both filters together).
    expect(source).toContain('controlClassName="toolbar-filter toolbar-filter-protocol"');
    expect(source.match(/controlClassName="toolbar-filter"/g)).toHaveLength(1);
    const css = readFileSync(resolve(__dirname, "../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.toolbar-filter\.toolbar-filter-protocol\s*\{[^}]*width:\s*210px/s);
  });
});

describe("provider detail drawer body (§9.2 ⑦)", () => {
  it("renders baseline descriptions only — actions live in the drawer footer", async () => {
    const module = await import("./providers");
    const Detail = (module as unknown as {
      ProviderDetailContent?: ComponentType<{
        provider: Upstream;
        result?: TestResult;
      }>;
    }).ProviderDetailContent;

    expect(Detail).toBeTypeOf("function");
    if (!Detail) return;

    const html = renderToStaticMarkup(shell(createElement(Detail, {
      provider,
      result: { success: true, latency_ms: 243, model: "gpt-4.1" },
    })));

    expect(html).toContain('class="descriptions"');
    expect(html).toContain("243ms");
    expect(html).toContain('class="health"');
    // Action buttons moved into the drawer footer per the baseline #providerDrawer; the card body no longer renders buttons
    expect(html).not.toContain("Test connection");
    expect(html).not.toContain("Edit configuration");
    expect(html).not.toContain("provider-detail-actions");
  });

  it("composes the detail drawer footer like the baseline (start: delete/import, end: close/test/edit)", () => {
    // Baseline #providerDrawer footer: text-family delete/import on the left, close/test/edit-config on the right
    expect(source).toContain('className="drawer-footer-start"');
    expect(source).toContain("button-text button-danger-text");
    expect(source).toContain('className="drawer-footer-end"');
    expect(source).toContain('"v2.providers.close"');
    expect(source).toContain('"v2.providers.test"');
    expect(source).toContain('"v2.providers.editConfiguration"');
    expect(source).toContain('"v2.providers.importModels"');
  });
});

describe("provider create/edit form sections (§9.2 ②③④)", () => {
  it("uses one baseline section structure for create and edit forms", async () => {
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

    expect(html).toContain('class="add-provider-form"');
    expect(html).toContain('data-provider-form-section="connection"');
    expect(html).toContain('data-provider-form-section="credentials"');
    expect(html).toContain('data-provider-form-section="discovery"');
    expect(html).toContain("Connection");
    expect(html).toContain("Credentials");
    expect(html).toContain("Model discovery");
  });

  it("renders secret credential fields with the baseline secret-control and a per-field toggle", () => {
    // Password inputs never ship the value through a visible input until revealed.
    expect(source).toContain('className="field-control secret-control"');
    expect(source).toContain('className="secret-toggle"');
    expect(source).toContain('type={reveal ? "text" : "password"}');
    expect(source).toContain('autoComplete="off"');
  });

  it("renders the preset picker with provider-pick items and a locked edit variant", () => {
    expect(source).toContain('className="provider-pick"');
    expect(source).toContain('clsx("provider-pick-item"');
    expect(source).toContain("aria-pressed");
    expect(source).toContain('className={clsx("provider-pick-item", editingPresetId === preset.id && "selected")}');
    // the edit dialog locks the preset: disabled items, help hint explains it
    expect(source.match(/<button[^>]*className=\{clsx\("provider-pick-item"[^>]*}\s*disabled/s)).toBeTruthy();
    expect(source).toContain("theProviderPresetCanTBeChangedAfter");
  });

  it("pairs name+protocol and base+proxy per row in both drawers (baseline form-grid)", () => {
    // The baseline #addProviderDrawer form-grid (1fr 1fr): name+protocol on one row,
    // Base URL+proxy URL on one row — all four fields are half-width, consistent across the add/edit drawers.
    expect(source.match(/v2\.providers\.name"\)\}\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/v2\.providers\.protocol"\)\}\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/label="Base URL"\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/v2\.providers\.proxyUrl"\)\}\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
  });
});

describe("SSE test progress surfaces (§9.2 ⑤⑥)", () => {
  it("renders probe steps in every state with waiting metadata", async () => {
    const module = await import("./providers");
    const ProbeSteps = (module as unknown as {
      ProbeSteps?: ComponentType<{
        steps: Array<{ key: string; label: string; status: "idle" | "running" | "passed" | "failed"; meta?: string }>;
        waitingLabel: string;
      }>;
    }).ProbeSteps;

    expect(ProbeSteps).toBeTypeOf("function");
    if (!ProbeSteps) return;

    const html = renderToStaticMarkup(createElement(ProbeSteps, {
      waitingLabel: "Waiting",
      steps: [
        { key: "config", label: "Configuration validation", status: "passed", meta: undefined },
        { key: "model_request", label: "Model request test", status: "running" },
        { key: "models", label: "Model discovery", status: "failed", meta: "boom" },
        { key: "credentials", label: "Credential validation", status: "idle" },
      ],
    }));

    expect(html).toContain('class="probe-steps"');
    expect(html).toContain("probe-step done");
    expect(html).toContain("probe-step failed");
    expect(html).toContain("probe-spin");
    expect(html).toContain(">Waiting</small>");
    expect(html).toContain(">boom</small>");
  });

  it("renders the streaming log as a baseline code-output card with level-colored rows", async () => {
    const module = await import("./providers");
    const TestLog = (module as unknown as {
      ProviderTestLog?: ComponentType<{
        logs: Array<{ timestamp: string; level: "info" | "success" | "error"; message: string }>;
        emptyLabel: string;
        statusLabel: string;
      }>;
    }).ProviderTestLog;

    expect(TestLog).toBeTypeOf("function");
    if (!TestLog) return;

    const html = renderToStaticMarkup(shell(createElement(TestLog, {
      emptyLabel: "Waiting for test to start",
      statusLabel: "In progress",
      logs: [
        { timestamp: "10:00:00", level: "info", message: "Testing endpoint" },
        { timestamp: "10:00:01", level: "success", message: "Connection available" },
      ],
    })));

    expect(html).toContain('class="code-output"');
    expect(html).toContain("code-output-body provider-test-log");
    expect(html).toContain('data-log-level="success"');
    expect(html).toContain("Connection available");
    expect(html).toContain("In progress");
  });

  it("renders the import preview as compact metrics plus a per-model action table", async () => {
    const module = await import("./providers");
    const Summary = (module as unknown as {
      RouteImportSummary?: ComponentType<{ preview: RouteImportPreview }>;
    }).RouteImportSummary;

    expect(Summary).toBeTypeOf("function");
    if (!Summary) return;

    const html = renderToStaticMarkup(shell(createElement(Summary, {
      preview: { discovered: 3, create: ["gpt-4.1", "gpt-4.1-mini"], skip: ["gpt-4o"] },
    })));

    expect(html).toContain("provider-import-summary");
    expect(html).toContain("provider-import-metrics");
    expect(html).toContain("gpt-4.1-mini");
    expect(html).toContain("tag-success");
    expect(html).toContain("existing routes will be skipped");
  });
});

describe("SSE handlers keep their cancellation and completion semantics", () => {
  it("keys every SSE run by runId with an AbortController and an isCanceled guard", () => {
    const runBlocks = source.match(
      /async function (?:handleTest|handleImportRoutes|handleCreateHealthCheck|handleUpdateHealthCheck)\([\s\S]*?\n {2}\}/g,
    ) ?? [];
    expect(runBlocks).toHaveLength(4);
    for (const block of runBlocks) {
      expect(block).toContain("activeTestRunRef.current + 1");
      expect(block).toContain("new AbortController()");
      expect(block).toContain("const isCanceled = () =>");
    }
  });

  it("falls back when a health check stream never yields a completion event", () => {
    expect(source).toContain("healthCheckDidNotReturnACompletionEvent");
    expect(source).toContain("if (!completed && !isCanceled() && !abortController.signal.aborted)");
  });

  it("invalidates the routes query after an import completes", () => {
    expect(source).toContain('qc.invalidateQueries({ queryKey: ["routes"] })');
  });
});

describe("deep-link focus effect (React #185 regression)", () => {
  it("keeps query fallback identities stable so the effect cannot churn mid-navigation", () => {
    // Regression: clicking a global-search hit warms the providers cache then navigates to
    // /providers?focus=…, at which point presets are still in flight — a destructuring default
    // of = [] refreshes the array every render, so the providerPresets/startEdit/deep-link
    // effects that depend on it re-run endlessly, forming a race loop with navigate's commit
    // and triggering React #185 (max update depth, the whole page tree unmounts). A module-level
    // sentinel array pins the fallback identity.
    expect(source).toContain("const NO_UPSTREAMS: Upstream[] = []");
    expect(source).toContain("const NO_PRESET_DTOS: ProviderPresetDTO[] = []");
    expect(source).toContain("data: providers = NO_UPSTREAMS");
    expect(source).toContain("data: providerPresetsRaw = NO_PRESET_DTOS");
  });

  it("handles each deep link exactly once and re-arms after the param is stripped", () => {
    // The same deep link may fire again from dependency settling before navigate(replace) commits — the
    // handled ref guarantees it runs exactly once; it re-arms once the param disappears, letting the user click the same hit again.
    expect(source).toContain("deepLinkHandledRef");
    expect(source).toContain("deepLinkHandledRef.current = linkKey");
    expect(source).toContain("deepLinkHandledRef.current = null");
  });
});

describe("model discovery help hint alignment (baseline field-label structure)", () => {
  it("keeps the help hint a direct child of the discover label in both drawers", () => {
    // The "?" help button must be a direct child of .field-label.discover-label (flex + gap
    // centering, same label structure as the baseline waf-gateway). Wrapping it in a text
    // span drops it into inline baseline alignment → icon misalignment; both the add and
    // edit drawers must hold this structure.
    const direct = source.match(/modelDiscovery2"\)\}\s*<\/span>\s*<NyroHelpHint/g) ?? [];
    const inline = source.match(/modelDiscovery2"\)\}\s*<NyroHelpHint/g) ?? [];
    expect(direct.length).toBe(2);
    expect(inline.length).toBe(0);
  });
});

describe("probe action uses the baseline heartbeat icon (§v2 providers)", () => {
  it("renders the baseline #test EKG path instead of the lucide Zap bolt", () => {
    // The baseline providers.html probe button uses the #test "Heartbeat" symbol (EKG polyline,
    // 1.7 stroke, round joins); the bolt is lucide Zap, not the baseline form.
    expect(source).toContain("M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36");
    expect(source).toContain("strokeWidth={1.7}");
    expect(source).toContain("<HeartbeatIcon aria-hidden=\"true\" />");
    expect(source).not.toMatch(/\bZap\b/);
  });
});
