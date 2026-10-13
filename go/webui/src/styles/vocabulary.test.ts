import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { AppLayout } from "@/components/layout/app-layout";
import DashboardPage from "@/pages/dashboard";
import ProvidersPage from "@/pages/providers";
import ModelsPage from "@/pages/models-v2";
import ApiKeysPage from "@/pages/api-keys";
import ConnectPage from "@/pages/connect";
import NodesPage from "@/pages/nodes";
import ServicesPage from "@/pages/services";
import LogsPage from "@/pages/logs";
import StatsPage from "@/pages/stats-v2";
import SettingsPage from "@/pages/settings";
import type { GatewayNode, Route, Upstream } from "@/lib/types";

/* §12 structured equivalence checks for visual acceptance:
   ① Every class each page renders statically must have a definition in one of the three CSS
      files (nyro-ui / nyro-app / radix-bridge) — rendered output is DOM ground truth, zero parser false positives;
   ② Reverse: a class defined in nyro-app.css may not go unused (guards against dead CSS piling up).
      This direction uses "rendered output ∪ static source scan (incl. test assertions)"; enum-value false
      positives on the static side only grow the whitelist, never miss.
   nyro-ui.css is the shared baseline (kept in sync with the baseline source); it may hold classes we don't use, so no reverse check. */

// ── CSS side: every class that appears in selectors ──────────────
function cssClasses(css: string): Set<string> {
  const noComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  return new Set(
    [...noComments.matchAll(/\.([a-zA-Z][a-zA-Z0-9_-]*)/g)].map((m) => m[1]),
  );
}

const styleDir = __dirname;
const appCss = readFileSync(resolve(styleDir, "nyro-app.css"), "utf8");
const uiCss = readFileSync(resolve(styleDir, "nyro-ui.css"), "utf8");
const bridgeCss = readFileSync(resolve(styleDir, "radix-bridge.css"), "utf8");
const defined = new Set<string>([...cssClasses(appCss), ...cssClasses(uiCss), ...cssClasses(bridgeCss)]);

// ── Render side: 10 pages + the shell, statically rendered, classes pulled ──
const provider: Upstream = {
  id: "up-openai",
  name: "OpenAI Production",
  provider: "openai",
  protocol: "openai-responses",
  base_url: "https://api.openai.com/v1",
  models: ["gpt-4.1"],
  enabled: true,
};

const route: Route = {
  id: "route-1",
  model: "gpt-4.1",
  balance: "weighted",
  enable_auth: true,
  enabled: true,
  upstreams: [{
    id: "ru-1",
    route_id: "route-1",
    upstream_id: "up-openai",
    model: "gpt-4.1",
    weight: 100,
    priority: 0,
    enabled: true,
  }],
};

const node: GatewayNode = {
  node_id: "node-1",
  hostname: "gw-1",
  app_version: "2.0.0",
  service_port: "8080",
  remote_addr: "10.0.0.1:52000",
  conn_mode: "mtls",
  connected_at: "2026-09-24T08:00:00Z",
  applied_version: 7,
};

const overview = {
  total_requests: 1234,
  total_input_tokens: 90_000,
  total_output_tokens: 45_000,
  avg_duration_ms: 120,
  error_count: 34,
  p95_duration_ms: 240,
};
const hourly = [{
  hour: "2026-09-24T10:00:00Z",
  request_count: 120,
  error_count: 2,
  total_input_tokens: 1,
  total_output_tokens: 1,
  avg_duration_ms: 100,
}];
const routeStats = [{
  route_id: "route-1",
  route_model: "gpt-4.1",
  request_count: 500,
  total_input_tokens: 1,
  total_output_tokens: 1,
  avg_duration_ms: 120,
  error_count: 25,
  p95_duration_ms: 240,
}];
const upstreamStats = [{
  upstream_id: "up-openai",
  upstream_name: "OpenAI Production",
  request_count: 500,
  error_count: 0,
  avg_duration_ms: 120,
  p95_duration_ms: 240,
}];
const consumerStats = [{
  consumer_id: "cons-1",
  request_count: 400,
  total_input_tokens: 60_000,
  total_output_tokens: 30_000,
  cache_read_tokens: 12_000,
  last_used_at: Date.UTC(2026, 8, 24, 12, 0, 0),
}];

const PAGES: { path: string; element: ReactNode }[] = [
  { path: "/", element: createElement(DashboardPage) },
  { path: "/providers", element: createElement(ProvidersPage) },
  { path: "/models", element: createElement(ModelsPage) },
  { path: "/api-keys", element: createElement(ApiKeysPage) },
  { path: "/connect", element: createElement(ConnectPage) },
  { path: "/nodes", element: createElement(NodesPage) },
  { path: "/services", element: createElement(ServicesPage) },
  { path: "/logs", element: createElement(LogsPage) },
  { path: "/stats", element: createElement(StatsPage) },
  { path: "/settings", element: createElement(SettingsPage) },
];

afterEach(() => {
  vi.unstubAllGlobals();
});

function seedAll(queryClient: QueryClient) {
  queryClient.setQueryData(["providers"], [provider]);
  queryClient.setQueryData(["provider-presets"], []);
  queryClient.setQueryData(["routes"], [route]);
  queryClient.setQueryData(["consumers"], []);
  queryClient.setQueryData(["nodes"], [node]);
  queryClient.setQueryData(["runtime-services"], [{ id: "control-plane", status: "running" }]);
  queryClient.setQueryData(["gateway-status"], { status: "ok", version: "2.0.0" });
  // dashboard and stats use one key with hours and one without — seed both
  for (const key of [["stats-overview"], ["stats-overview", 24]]) {
    queryClient.setQueryData(key, overview);
  }
  for (const key of [["stats-hourly"], ["stats-hourly", 24]]) {
    queryClient.setQueryData(key, hourly);
  }
  for (const key of [["stats-routes"], ["stats-routes", 24]]) {
    queryClient.setQueryData(key, routeStats);
  }
  for (const key of [["stats-upstreams"], ["stats-upstreams", 24]]) {
    queryClient.setQueryData(key, upstreamStats);
  }
  queryClient.setQueryData(["stats-consumers", 24], consumerStats);
}

function renderAll(): string {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  seedAll(queryClient);

  const parts = PAGES.map(({ path, element }) =>
    renderToStaticMarkup(createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(
        MemoryRouter,
        { initialEntries: [path] },
        createElement(LocaleProvider, null, element),
      ),
    )),
  );
  // The shell and the command palette (closed state) go through the same pass
  parts.push(renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/"] },
      createElement(LocaleProvider, null, createElement(AppLayout)),
    ),
  )));
  return parts.join("\n");
}

function htmlClasses(html: string): Set<string> {
  const names = new Set<string>();
  for (const m of html.matchAll(/class="([^"]+)"/g)) {
    for (const token of m[1].split(/\s+/)) names.add(token);
  }
  return names;
}

// ── Static side: class literals in source (incl. test assertions), reverse dead-CSS check only ──
function collectSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = resolve(dir, entry.name);
    if (entry.isDirectory()) out.push(...collectSourceFiles(path));
    else if (/\.(ts|tsx)$/.test(entry.name)) out.push(path);
  }
  return out;
}

/** Extract className={...} expression text by balancing braces. */
function classNameExpressions(text: string): string[] {
  const exprs: string[] = [];
  for (const m of text.matchAll(/className=\{/g)) {
    let depth = 1;
    let i = m.index! + m[0].length;
    let templateDepth = 0; // >0 means we are inside a ${} of a template literal
    let inString: string | null = null;
    while (i < text.length && depth > 0) {
      const ch = text[i];
      if (ch === "\\") { i += 2; continue; }
      if (inString) {
        if (ch === inString) inString = null;
        i += 1;
        continue;
      }
      if (ch === '"' || ch === "'") { inString = ch; i += 1; continue; }
      if (ch === "`") { i += 1; continue; }
      if (ch === "$" && text[i + 1] === "{") { depth += 1; templateDepth += 1; i += 2; continue; }
      if (ch === "{") { depth += 1; i += 1; continue; }
      if (ch === "}") {
        depth -= 1;
        if (templateDepth > 0) templateDepth -= 1;
        i += 1;
        continue;
      }
      i += 1;
    }
    exprs.push(text.slice(m.index! + m[0].length, i - 1));
  }
  return exprs;
}

/** Class token shape: lowercase-initial kebab (filters out non-class literals such as locale enums, numbers, operators). */
const CLASS_TOKEN = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

function staticClassNames(): Set<string> {
  const names = new Set<string>();
  for (const file of collectSourceFiles(resolve(styleDir, ".."))) {
    const text = readFileSync(file, "utf8");
    for (const m of text.matchAll(/(?:class|className)="([^"]+)"/g)) {
      for (const token of m[1].split(/\s+/)) {
        if (CLASS_TOKEN.test(token)) names.add(token);
      }
    }
    for (const expr of classNameExpressions(text)) {
      for (const s of expr.matchAll(/"([^"]+)"/g)) {
        for (const token of s[1].split(/\s+/)) {
          if (CLASS_TOKEN.test(token)) names.add(token);
        }
      }
      for (const t of expr.matchAll(/`([^`]+)`/g)) {
        for (const part of t[1].split(/\$\{[^}]*\}/)) {
          for (const token of part.split(/\s+/)) {
            if (CLASS_TOKEN.test(token)) names.add(token);
          }
        }
      }
    }
  }
  return names;
}

const renderedClasses = htmlClasses(renderAll());
const usedClasses = new Set<string>([...renderedClasses, ...staticClassNames()]);

/** Dynamic stems: template-concatenation prefixes like `tok-${kind}`, `cols-${n}`, `tone-${tone}`.
    A defined class starting with a stem counts as reachable — the component API allows arbitrary values,
    and the style side keeps variants ready (e.g. no page uses cols-6 today, but it hits once MetricLedger is passed 6 items). */
function dynamicStems(): Set<string> {
  const stems = new Set<string>();
  for (const file of collectSourceFiles(resolve(styleDir, ".."))) {
    const text = readFileSync(file, "utf8");
    // Match dynamic stems at the start of template literals directly (leading spaces allowed); nested templates hit too:
    // `tok-${kind}`, ` cols-${n}`, ` tone-${tone}`.
    for (const m of text.matchAll(/`\s*([a-z][a-z0-9-]*-)\$\{/g)) {
      stems.add(m[1]);
    }
  }
  return stems;
}

const stems = dynamicStems();
const reachableViaStem = (name: string) =>
  [...stems].some((stem) => name.startsWith(stem));

/** Library marker classes lucide-react stamps on every icon SVG (lucide, lucide-check, ...);
    icon looks are controlled by svg selectors, so these classes themselves need no style definitions. */
const LIBRARY_MARKER = /^lucide(-|$)/;

describe("class vocabulary completeness (§12)", () => {
  it("renders a meaningful class surface from all 10 pages and the shell", () => {
    expect(renderedClasses.size).toBeGreaterThan(80);
    expect(usedClasses.size).toBeGreaterThan(120);
  });

  it("has a CSS definition for every class in the rendered DOM", () => {
    const undefinedClasses = [...renderedClasses].filter(
      (name) => !defined.has(name) && !LIBRARY_MARKER.test(name),
    );
    expect(undefinedClasses).toEqual([]);
  });

  it("keeps nyro-app.css free of unused rules", () => {
    const unused = [...cssClasses(appCss)].filter(
      (name) => !usedClasses.has(name) && !reachableViaStem(name),
    );
    expect(unused).toEqual([]);
  });
});
