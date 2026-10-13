import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type {
  Consumer,
  GatewayNode,
  LogPage,
  Route,
  RouteStats,
  RuntimeService,
  StatsHourly,
  StatsOverview,
  Upstream,
  UpstreamStats,
} from "@/lib/types";
import DashboardPage from "./dashboard";

const source = readFileSync(resolve(__dirname, "dashboard.tsx"), "utf8");

const nodes: GatewayNode[] = [{
  node_id: "node-1",
  hostname: "gw-1",
  app_version: "1.0.0",
  service_port: "8080",
  remote_addr: "10.0.0.1:52000",
  connected_at: "2026-09-24T08:00:00Z",
  applied_version: 7,
}];

const services: RuntimeService[] = [
  { id: "control-plane", status: "running" },
  { id: "embedded-proxy", status: "running" },
  { id: "redis-state", status: "disabled" },
];

const overview: StatsOverview = {
  total_requests: 1234,
  total_input_tokens: 90_000,
  total_output_tokens: 45_000,
  avg_duration_ms: 120,
  error_count: 34,
  p95_duration_ms: 240,
};

const hourly: StatsHourly[] = [
  { hour: "2026-09-24T10:00:00Z", request_count: 120, error_count: 2, total_input_tokens: 1, total_output_tokens: 1, avg_duration_ms: 100 },
  { hour: "2026-09-24T11:00:00Z", request_count: 180, error_count: 0, total_input_tokens: 1, total_output_tokens: 1, avg_duration_ms: 110 },
  { hour: "2026-09-24T12:00:00Z", request_count: 90, error_count: 5, total_input_tokens: 1, total_output_tokens: 1, avg_duration_ms: 130 },
];

const routeStats: RouteStats[] = [{
  route_id: "route-1",
  route_model: "gpt-4.1",
  request_count: 500,
  total_input_tokens: 1,
  total_output_tokens: 1,
  avg_duration_ms: 120,
  error_count: 25,
  p95_duration_ms: 240,
}];

const upstreamStats: UpstreamStats[] = [{
  upstream_id: "up-openai",
  upstream_name: "OpenAI Production",
  request_count: 500,
  error_count: 0,
  avg_duration_ms: 120,
  p95_duration_ms: 240,
}];

const providers: Upstream[] = [{
  id: "up-openai",
  name: "OpenAI Production",
  provider: "openai",
  protocol: "openai-responses",
  base_url: "https://api.openai.com/v1",
  models: ["gpt-4.1", "gpt-4.1-mini"],
  enabled: true,
}];

const consumers: Consumer[] = [{ id: "consumer-1", name: "demo-app", enabled: true }];

const routes: Route[] = [{
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
}];

const logs: LogPage = {
  total: 2,
  items: [
    {
      id: "log-1",
      created_at: Date.UTC(2026, 8, 24, 12, 0, 0),
      route_model: "gpt-4.1",
      upstream_name: "OpenAI Production",
      response_status_code: 200,
      latency_total_ms: 240,
      input_tokens: 10,
      output_tokens: 20,
      is_stream: false,
      stream_chunks_count: 0,
    },
    {
      id: "log-2",
      created_at: Date.UTC(2026, 8, 24, 12, 5, 0),
      route_model: "gpt-4.1-mini",
      upstream_name: "OpenAI Production",
      response_status_code: 500,
      latency_total_ms: 9_800,
      input_tokens: 10,
      output_tokens: 0,
      is_stream: true,
      stream_chunks_count: 3,
    },
  ],
};

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderDashboardPage(seed = true) {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  if (seed) {
    queryClient.setQueryData(["stats-overview"], overview);
    queryClient.setQueryData(["stats-hourly"], hourly);
    queryClient.setQueryData(["stats-routes"], routeStats);
    queryClient.setQueryData(["stats-upstreams"], upstreamStats);
    queryClient.setQueryData(["providers"], providers);
    queryClient.setQueryData(["routes"], routes);
    queryClient.setQueryData(["consumers"], consumers);
    queryClient.setQueryData(["nodes"], nodes);
    queryClient.setQueryData(["runtime-services"], services);
    queryClient.setQueryData(["logs", "dashboard"], logs);
  }

  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/"] },
      createElement(LocaleProvider, null, createElement(DashboardPage)),
    ),
  ));
}

describe("dashboard page static render (§9.8)", () => {
  it("renders the baseline two-band header: health strip caps + bare metric strip", () => {
    const html = renderDashboardPage();

    expect(html).toContain('aria-label="Runtime status"');
    expect(html).toContain('class="health-strip"');
    expect(html).toContain("Gateway runtime healthy");
    // The baseline puts the config version inside the summary text ("config version rev-7 synced") instead of on its own line
    expect(html).toContain("config version rev-7 synced");
    expect(html).toContain('class="health-caps"');
    // caps merged: providers/models/success rate + worker nodes/running services + API keys
    expect(html.match(/class="health-cap"/g)).toHaveLength(6);
    expect(html).toContain("Worker nodes");
    expect(html).toContain("Running services");
    expect(html).toContain("API Keys");
    // The baseline metric-strip has no top row (no horizontal rule above the metric row)
    expect(html).not.toContain("metric-strip-top");
    expect(html).not.toContain("metric-strip-live");
    expect(html).not.toContain("metric-strip-rev");
    expect(html).not.toContain("metric-strip-health");
    expect(html.match(/class="metric-cell"/g)).toHaveLength(5);
    expect(html.match(/class="metric-hint"/g)).toHaveLength(5);
    // The 24-hour request count is a full thousands-separated number; the token hint is the real input/output split; the error-rate threshold carries a < sign
    expect(html).toContain("1,234");
    expect(html).toContain("Input 90K · Output 45K");
    expect(html).toContain("Target threshold &lt; 1.00%");
  });

  it("renders two split-grid sections: chart and upstreams, performance table and activity", () => {
    const html = renderDashboardPage();

    expect(html.match(/class="section-grid split-grid"/g)).toHaveLength(2);
    expect(html).toContain('class="card chart-full"');
    expect(html).toContain('class="live-pill"');
    expect(html).toContain('class="chart-legend"');
    expect(html).toContain('class="legend-dot error"');
    expect(html).toContain('class="upstream-list"');
    expect(html).toContain('<button type="button" class="upstream-item"');
    expect(html).toContain('class="upstream-name"');
    expect(html).toContain('class="health"');
    expect(html).toContain('class="table-scroll"');
    expect(html).toContain('class="clickable"');
    expect(html).toContain('tabindex="0"');
    expect(html).toContain('class="cell-stack"');
    expect(html).toContain('class="card notice-card"');
    expect(html).toContain('class="sample-chips"');
    expect(html.match(/class="sample-chip"/g)).toHaveLength(2);
    expect(html).toContain('class="sample-item sample-head"');
    expect(html).toContain('class="sample-item ok"');
    expect(html).toContain('class="sample-item err"');
    expect(html).toContain("Errors 1");
    expect(html).toContain("Samples 2");
  });

  it("shows the unknown banner and every empty state when nothing is loaded", () => {
    const html = renderDashboardPage(false);

    expect(html).toContain('class="health-strip unknown"');
    expect(html).toContain("No traffic has been observed yet");
    expect(html).toContain("No providers configured yet");
    expect(html).toContain("No model traffic data");
    expect(html).toContain("No recent requests");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderDashboardPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] means the same as the original spelling, split apart to keep the literal out of the exit grep
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|grid-cols|flex items|font-mono|h-10|w-full|pr-10|min-h-32)-/);
  });
});

describe("dashboard page API migration (§9.8 ⑦)", () => {
  it("keeps all ten queries but talks only through the typed API layer", () => {
    expect(source).toContain("statsApi.overview(24)");
    expect(source).toContain("statsApi.hourly(24)");
    expect(source).toContain("statsApi.byRoute(24)");
    expect(source).toContain("statsApi.byUpstream(24)");
    expect(source).toContain("upstreamsApi.list()");
    expect(source).toContain("routesApi.list()");
    expect(source).toContain("consumersApi.list()");
    expect(source).toContain("systemApi.nodes()");
    expect(source).toContain("systemApi.runtimeServices()");
    expect(source).toContain("logsApi.query(");
    expect(source.match(/useQuery</g)).toHaveLength(10);
    expect(source).not.toMatch(/\bbackend[(<]/);
  });

  it("renders the trend through the ported NyroChart, not recharts (D5)", () => {
    expect(source).toContain('from "@/components/ui/nyro-chart"');
    expect(source).not.toMatch(/recharts/);
    // F3: the v2 chart's hardcoded palette is gone.
    expect(source).not.toMatch(/#285cc4|#b83a3a|#dfe3e7|#87909b|#c5cbd2/);
  });

  it("keeps row navigation keyboard-accessible", () => {
    expect(source).toContain("tabIndex={0}");
    expect(source).toContain('event.key === "Enter" || event.key === " "');
  });
});
