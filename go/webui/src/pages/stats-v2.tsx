import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import clsx from "clsx";

import { NyroChart, type NyroChartDatum } from "@/components/ui/nyro-chart";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { errorRate, rankedShare } from "@/features/stats/stats-view-model";
import { statsApi } from "@/lib/api/stats";
import { formatLogTime } from "@/lib/format";
import { useLocale } from "@/lib/i18n";
import { localizedMessage } from "@/lib/messages";
import type { ConsumerStats, RouteStats, StatsHourly, StatsOverview, UpstreamStats } from "@/lib/types";

function compact(value: number) {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function latency(value: number | null | undefined) {
  if (value == null) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s` : `${Math.round(value)}ms`;
}

/** Time-window segments (the baseline period-button form); the selected window drives every query on this page. */
const RANGES = [
  { hours: 6, label: "6H", key: "v2.stats.last6Hours" },
  { hours: 24, label: "24H", key: "v2.stats.last24Hours" },
  { hours: 72, label: "3D", key: "v2.stats.last3Days" },
  { hours: 168, label: "7D", key: "v2.stats.last7Days" },
] as const;

type RankItem = { id: string; label: string; value: number; share: number };

function PriorityRanking({ items, empty }: { items: RankItem[]; empty: string }) {
  if (!items.length) return <div className="empty">{empty}</div>;
  return (
    <div className="priority-list">
      {items.map((item, index) => (
        <div className="priority-item" key={item.id}>
          <span className="priority-order">{index + 1}</span>
          <span className="priority-label" title={item.label}>{item.label}</span>
          <span className="priority-share">{compact(item.value)} · {item.share.toFixed(0)}%</span>
        </div>
      ))}
    </div>
  );
}

export default function StatsV2Page() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const [hours, setHours] = useState(24);

  const { data: overview } = useQuery<StatsOverview>({ queryKey: ["stats-overview", hours], queryFn: () => statsApi.overview(hours), refetchInterval: 10_000 });
  const { data: hourly = [] } = useQuery<StatsHourly[]>({ queryKey: ["stats-hourly", hours], queryFn: () => statsApi.hourly(hours), refetchInterval: 30_000 });
  const { data: routeStats = [] } = useQuery<RouteStats[]>({ queryKey: ["stats-routes", hours], queryFn: () => statsApi.byRoute(hours), refetchInterval: 30_000 });
  const { data: upstreamStats = [] } = useQuery<UpstreamStats[]>({ queryKey: ["stats-upstreams", hours], queryFn: () => statsApi.byUpstream(hours), refetchInterval: 30_000 });
  const { data: consumerStats = [] } = useQuery<ConsumerStats[]>({ queryKey: ["stats-consumers", hours], queryFn: () => statsApi.byConsumer(hours), refetchInterval: 30_000 });

  const requests = overview?.total_requests ?? 0;
  const errors = overview?.error_count ?? 0;
  const totalTokens = (overview?.total_input_tokens ?? 0) + (overview?.total_output_tokens ?? 0);
  const chartData: NyroChartDatum[] = hourly.map((point) => ({
    label: point.hour.slice(11, 16),
    value: point.request_count,
    error: point.error_count,
  }));
  const modelRanks = rankedShare(routeStats.map((route) => ({ id: route.route_id, label: route.route_model || route.route_id, value: route.request_count })), 6);
  const errorRanks = rankedShare(routeStats.filter((route) => route.error_count > 0).map((route) => ({ id: route.route_id, label: route.route_model || route.route_id, value: route.error_count })), 6);
  const upstreamRows = upstreamStats.slice(0, 8);
  const consumerRows = consumerStats.slice(0, 8);

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "page.stats.title")}
          description={localizedMessage(isZh, "page.stats.subtitle")}
          actions={(
            <div className="chart-actions">
              {RANGES.map((range) => (
                <button
                  key={range.hours}
                  type="button"
                  className={clsx("period-button", hours === range.hours && "active")}
                  aria-pressed={hours === range.hours}
                  aria-label={localizedMessage(isZh, range.key)}
                  onClick={() => setHours(range.hours)}
                >
                  {range.label}
                </button>
              ))}
            </div>
          )}
        />
      )}
    >

      <MetricLedger items={[
        { key: "requests", label: localizedMessage(isZh, "v2.stats.requests2"), value: compact(requests), detail: localizedMessage(isZh, "stats.windowHours", { hours }) },
        { key: "tokens", label: localizedMessage(isZh, "v2.stats.totalTokens"), value: compact(totalTokens), detail: localizedMessage(isZh, "stats.tokenBreakdown", { input: compact(overview?.total_input_tokens ?? 0), output: compact(overview?.total_output_tokens ?? 0) }) },
        { key: "average", label: localizedMessage(isZh, "v2.stats.averageLatency"), value: latency(overview?.avg_duration_ms), detail: `P95 ${latency(overview?.p95_duration_ms)}` },
        { key: "errors", label: localizedMessage(isZh, "v2.stats.errors2"), value: compact(errors), detail: localizedMessage(isZh, "stats.errorRateValue", { rate: errorRate(errors, requests).toFixed(2) }), tone: errors ? "danger" : "default" },
        { key: "consumers", label: localizedMessage(isZh, "v2.stats.activeConsumers"), value: consumerStats.length, detail: localizedMessage(isZh, "v2.stats.inThisTimeWindow") },
      ]} />

      <article className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{localizedMessage(isZh, "v2.stats.requestTrend")}</h2>
            <div className="card-subtitle">{localizedMessage(isZh, "v2.stats.requestsAndErrorsOverTheSelectedTimeWindow")}</div>
          </div>
        </div>
        {chartData.length < 2 ? (
          <div className="chart-wrap"><div className="empty">{localizedMessage(isZh, "v2.stats.noTrendData")}</div></div>
        ) : (
          <NyroChart
            data={chartData}
            ariaLabel={localizedMessage(isZh, "v2.stats.requestsAndErrorsOverTheSelectedTimeWindow")}
            valueLabel={localizedMessage(isZh, "v2.stats.requests")}
            errorLabel={localizedMessage(isZh, "v2.stats.errors")}
          />
        )}
        <div className="chart-legend">
          <span className="chart-legend-item"><span className="legend-dot" /><span>{localizedMessage(isZh, "v2.stats.requests")}</span></span>
          <span className="chart-legend-item"><span className="legend-dot error" /><span>{localizedMessage(isZh, "v2.stats.errors")}</span></span>
        </div>
      </article>

      <section className="section-grid split-grid">
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "v2.stats.modelDistribution")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "v2.stats.rankedByRequestCount")}</div>
            </div>
          </div>
          <PriorityRanking items={modelRanks} empty={localizedMessage(isZh, "v2.stats.noModelRequests")} />
        </article>
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "v2.stats.errorsByModel")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "v2.stats.rankedByErrorCount")}</div>
            </div>
          </div>
          <PriorityRanking items={errorRanks} empty={localizedMessage(isZh, "v2.stats.noErrorsInThisWindow")} />
        </article>
      </section>

      <section className="section-grid split-grid">
        <article className={clsx("card table-card stats-table", upstreamRows.length === 0 && "is-empty")}>
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "v2.stats.upstreamRanking")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "v2.stats.requestVolumeErrorsAndLatency")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "v2.stats.upstream")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.requests")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.errors")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.errorRate")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.avgLatency")}</th>
                  <th>P95</th>
                </tr>
              </thead>
              <tbody>
                {upstreamRows.map((item) => (
                  <tr key={item.upstream_id}>
                    <td>
                      <div className="cell-stack">
                        <strong>{item.upstream_name || item.upstream_id}</strong>
                        <small>{item.upstream_id}</small>
                      </div>
                    </td>
                    <td>{compact(item.request_count)}</td>
                    <td>{item.error_count}</td>
                    <td>{errorRate(item.error_count, item.request_count).toFixed(2)}%</td>
                    <td>{latency(item.avg_duration_ms)}</td>
                    <td>{latency(item.p95_duration_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="table-empty">{localizedMessage(isZh, "v2.stats.noUpstreamData")}</div>
        </article>

        <article className={clsx("card table-card stats-table", consumerRows.length === 0 && "is-empty")}>
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "v2.stats.consumerRanking")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "v2.stats.resourceUseByIdentity")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "v2.api-keys.consumer")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.requests")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.inputTokens")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.outputTokens")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.cacheReads")}</th>
                  <th>{localizedMessage(isZh, "v2.stats.lastUsed")}</th>
                </tr>
              </thead>
              <tbody>
                {consumerRows.map((item) => (
                  <tr key={item.consumer_id}>
                    <td><code className="code-pill">{item.consumer_id}</code></td>
                    <td>{compact(item.request_count)}</td>
                    <td>{compact(item.total_input_tokens)}</td>
                    <td>{compact(item.total_output_tokens)}</td>
                    <td>{compact(item.cache_read_tokens)}</td>
                    <td>{formatLogTime(item.last_used_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="table-empty">{localizedMessage(isZh, "v2.stats.noConsumerData")}</div>
        </article>
      </section>
    </PageLayout>
  );
}
