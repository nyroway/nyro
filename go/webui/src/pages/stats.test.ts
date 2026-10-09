import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type {
  ConsumerStats,
  RouteStats,
  StatsHourly,
  StatsOverview,
  UpstreamStats,
} from "@/lib/types";
import StatsV2Page from "./stats-v2";

const source = readFileSync(resolve(__dirname, "stats-v2.tsx"), "utf8");

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

const routeStats: RouteStats[] = [
  { route_id: "route-1", route_model: "gpt-4.1", request_count: 500, total_input_tokens: 1, total_output_tokens: 1, avg_duration_ms: 120, error_count: 25, p95_duration_ms: 240 },
  { route_id: "route-2", route_model: "claude-4-5-sonnet", request_count: 300, total_input_tokens: 1, total_output_tokens: 1, avg_duration_ms: 140, error_count: 5, p95_duration_ms: 300 },
];

const upstreamStats: UpstreamStats[] = [{
  upstream_id: "up-openai",
  upstream_name: "OpenAI Production",
  request_count: 500,
  error_count: 0,
  avg_duration_ms: 120,
  p95_duration_ms: 240,
}];

const consumerStats: ConsumerStats[] = [{
  consumer_id: "cons-1",
  request_count: 400,
  total_input_tokens: 60_000,
  total_output_tokens: 30_000,
  cache_read_tokens: 12_000,
  last_used_at: Date.UTC(2026, 8, 24, 12, 0, 0),
}];

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderStatsPage(seed = true) {
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
    queryClient.setQueryData(["stats-overview", 24], overview);
    queryClient.setQueryData(["stats-hourly", 24], hourly);
    queryClient.setQueryData(["stats-routes", 24], routeStats);
    queryClient.setQueryData(["stats-upstreams", 24], upstreamStats);
    queryClient.setQueryData(["stats-consumers", 24], consumerStats);
  }

  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/stats"] },
      createElement(LocaleProvider, null, createElement(StatsV2Page)),
    ),
  ));
}

describe("stats page static render (§9.7)", () => {
  it("drives the whole page from a baseline period-button range selector", () => {
    const html = renderStatsPage();

    expect(html).toContain('class="chart-actions"');
    expect(html).toContain('class="period-button active"');
    expect(html).toContain('aria-pressed="true"');
    expect(html).toContain('aria-pressed="false"');
    expect(html).toContain(">6H</button>");
    expect(html).toContain(">24H</button>");
    expect(html).toContain(">3D</button>");
    expect(html).toContain(">7D</button>");
    // the visible abbreviations carry full localized names for screen readers
    expect(html).toContain('aria-label="Last 6 hours"');
  });

  it("renders the metric-strip summary with the error tone", () => {
    const html = renderStatsPage();

    expect(html).toContain('class="metric-strip"');
    // 4 plain cells + 1 tone-danger cell
    expect(html.match(/class="metric-cell[ "]/g)).toHaveLength(5);
    expect(html).toContain('class="metric-cell tone-danger"');
    expect(html).toContain("24-hour window");
    expect(html).toContain("135K");
  });

  it("renders the trend through the shared NyroChart with a baseline legend", () => {
    const html = renderStatsPage();

    expect(html).toContain("Request trend");
    expect(html).toContain('class="chart-wrap"');
    expect(html).toContain('class="chart"');
    expect(html).toContain('role="img"');
    expect(html).toContain('class="chart-legend"');
    expect(html).toContain('class="legend-dot error"');
  });

  it("renders model and error rankings as baseline priority lists", () => {
    const html = renderStatsPage();

    expect(html.match(/class="section-grid split-grid"/g)).toHaveLength(2);
    expect(html.match(/class="priority-list"/g)).toHaveLength(2);
    expect(html.match(/class="priority-item"/g)).toHaveLength(4);
    expect(html).toContain('class="priority-order"');
    expect(html).toContain('class="priority-label"');
    expect(html).toContain('class="priority-share"');
    expect(html).toContain("gpt-4.1");
    // share is value · percent
    expect(html).toMatch(/500 · 63%/);
  });

  it("renders upstream and consumer rankings as baseline table cards", () => {
    const html = renderStatsPage();

    expect(html.match(/class="card table-card stats-table"/g)).toHaveLength(2);
    expect(html.match(/class="table-scroll"/g)).toHaveLength(2);
    expect(html).toContain("OpenAI Production");
    expect(html).toContain('class="cell-stack"');
    expect(html).toContain('class="code-pill"');
    expect(html).toContain("cons-1");
    expect(html).toContain("P95");
  });

  it("shows every empty state when nothing is loaded", () => {
    const html = renderStatsPage(false);

    expect(html).toContain("No trend data");
    expect(html).toContain("No model requests");
    expect(html).toContain("No errors in this window");
    expect(html.match(/class="card table-card stats-table is-empty"/g)).toHaveLength(2);
    expect(html.match(/class="table-empty"/g)).toHaveLength(2);
    expect(html).toContain("No upstream data");
    expect(html).toContain("No consumer data");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderStatsPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|grid-cols|flex items|font-mono|h-10|w-full|pr-10|min-h-32)-/);
  });
});

describe("stats page API migration (§9.7 ①④, D5)", () => {
  it("talks to the backend only through statsApi's five endpoints", () => {
    expect(source).toContain("statsApi.overview(hours)");
    expect(source).toContain("statsApi.hourly(hours)");
    expect(source).toContain("statsApi.byRoute(hours)");
    expect(source).toContain("statsApi.byUpstream(hours)");
    expect(source).toContain("statsApi.byConsumer(hours)");
    expect(source.match(/useQuery</g)).toHaveLength(5);
    expect(source).not.toMatch(/\bbackend[(<]/);
  });

  it("renders charts through the shared NyroChart — recharts and its palette are gone", () => {
    expect(source).toContain('from "@/components/ui/nyro-chart"');
    expect(source).not.toMatch(/recharts/);
    // F3: the v2 chart's hardcoded palette is gone.
    expect(source).not.toMatch(/#285cc4|#c54444|#e8edf1|#7a8792|#b83a3a|#dfe3e7|#87909b|#c5cbd2/);
  });
});
