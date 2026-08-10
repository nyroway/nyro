import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Surface } from "@/components/v2/surface";
import { errorRate, rankedShare } from "@/features/stats/stats-view-model";
import { backend } from "@/lib/backend";
import { formatLogTime } from "@/lib/format";
import { useLocale } from "@/lib/i18n";
import type { ConsumerStats, RouteStats, StatsHourly, StatsOverview, UpstreamStats } from "@/lib/types";
import { localizedMessage } from "@/lib/messages";

function compact(value: number) {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function latency(value: number | null | undefined) {
  if (value == null) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s` : `${Math.round(value)}ms`;
}

export default function StatsV2Page() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const [hours, setHours] = useState(24);

  const { data: overview } = useQuery<StatsOverview>({ queryKey: ["stats-overview", hours], queryFn: () => backend("get_stats_overview", { hours }), refetchInterval: 10_000 });
  const { data: hourly = [] } = useQuery<StatsHourly[]>({ queryKey: ["stats-hourly", hours], queryFn: () => backend("get_stats_hourly", { hours }), refetchInterval: 30_000 });
  const { data: routeStats = [] } = useQuery<RouteStats[]>({ queryKey: ["stats-routes", hours], queryFn: () => backend("get_stats_by_route", { hours }), refetchInterval: 30_000 });
  const { data: upstreamStats = [] } = useQuery<UpstreamStats[]>({ queryKey: ["stats-upstreams", hours], queryFn: () => backend("get_stats_by_upstream", { hours }), refetchInterval: 30_000 });
  const { data: consumerStats = [] } = useQuery<ConsumerStats[]>({ queryKey: ["stats-consumers", hours], queryFn: () => backend("get_stats_by_consumer", { hours }), refetchInterval: 30_000 });

  const requests = overview?.total_requests ?? 0;
  const errors = overview?.error_count ?? 0;
  const totalTokens = (overview?.total_input_tokens ?? 0) + (overview?.total_output_tokens ?? 0);
  const chartData = hourly.map((point) => ({ hour: point.hour.slice(11, 16), requests: point.request_count, errors: point.error_count }));
  const modelRanks = rankedShare(routeStats.map((route) => ({ id: route.route_id, label: route.route_model || route.route_id, value: route.request_count })), 6);
  const errorRanks = rankedShare(routeStats.filter((route) => route.error_count > 0).map((route) => ({ id: route.route_id, label: route.route_model || route.route_id, value: route.error_count })), 6);

  const upstreamColumns: DataTableColumn<UpstreamStats>[] = [
    { key: "upstream", header: localizedMessage(isZh, "v2.stats.upstream"), render: (item) => <div className="v2-rank-name"><strong>{item.upstream_name || item.upstream_id}</strong><code>{item.upstream_id}</code></div> },
    { key: "requests", header: localizedMessage(isZh, "v2.stats.requests"), className: "v2-table-number", render: (item) => compact(item.request_count) },
    { key: "errors", header: localizedMessage(isZh, "v2.stats.errors"), className: "v2-table-number", render: (item) => item.error_count },
    { key: "error-rate", header: localizedMessage(isZh, "v2.stats.errorRate"), className: "v2-table-number", render: (item) => `${errorRate(item.error_count, item.request_count).toFixed(2)}%` },
    { key: "average", header: localizedMessage(isZh, "v2.stats.avgLatency"), className: "v2-table-number", render: (item) => latency(item.avg_duration_ms) },
    { key: "p95", header: "P95", className: "v2-table-number", render: (item) => latency(item.p95_duration_ms) },
  ];

  const consumerColumns: DataTableColumn<ConsumerStats>[] = [
    { key: "consumer", header: localizedMessage(isZh, "v2.api-keys.consumer"), render: (item) => <code className="v2-rank-consumer">{item.consumer_id}</code> },
    { key: "requests", header: localizedMessage(isZh, "v2.stats.requests"), className: "v2-table-number", render: (item) => compact(item.request_count) },
    { key: "input", header: localizedMessage(isZh, "v2.stats.inputTokens"), className: "v2-table-number", render: (item) => compact(item.total_input_tokens) },
    { key: "output", header: localizedMessage(isZh, "v2.stats.outputTokens"), className: "v2-table-number", render: (item) => compact(item.total_output_tokens) },
    { key: "cache", header: localizedMessage(isZh, "v2.stats.cacheReads"), className: "v2-table-number", render: (item) => compact(item.cache_read_tokens) },
    { key: "last-used", header: localizedMessage(isZh, "v2.stats.lastUsed"), render: (item) => <span className="v2-stat-time">{formatLogTime(item.last_used_at)}</span> },
  ];

  return (
    <PageLayout
      header={(
        <PageHeader
          title={t("page.stats.title")}
          description={t("page.stats.subtitle")}
          actions={(
            <Select value={String(hours)} onValueChange={(value) => setHours(Number(value))}>
              <SelectTrigger className="v2-range-select"><SelectValue /></SelectTrigger>
              <SelectContent><SelectItem value="6">{localizedMessage(isZh, "v2.stats.last6Hours")}</SelectItem><SelectItem value="24">{localizedMessage(isZh, "v2.stats.last24Hours")}</SelectItem><SelectItem value="72">{localizedMessage(isZh, "v2.stats.last3Days")}</SelectItem><SelectItem value="168">{localizedMessage(isZh, "v2.stats.last7Days")}</SelectItem></SelectContent>
            </Select>
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

      <Surface title={localizedMessage(isZh, "v2.stats.requestTrend")} description={localizedMessage(isZh, "v2.stats.requestsAndErrorsOverTheSelectedTimeWindow")} actions={<div className="v2-chart-legend"><span><i />{localizedMessage(isZh, "v2.stats.requests")}</span><span><i className="error" />{localizedMessage(isZh, "v2.stats.errors")}</span></div>}>
        <div className="v2-stats-chart">
          {chartData.length ? <ResponsiveContainer width="100%" height="100%"><AreaChart data={chartData} margin={{ top: 12, right: 12, left: -18, bottom: 0 }}><CartesianGrid stroke="#e8edf1" vertical={false} /><XAxis dataKey="hour" tick={{ fill: "#7a8792", fontSize: 9 }} axisLine={false} tickLine={false} /><YAxis tick={{ fill: "#7a8792", fontSize: 9 }} axisLine={false} tickLine={false} allowDecimals={false} /><Tooltip /><Area type="monotone" dataKey="requests" stroke="#285cc4" strokeWidth={1.6} fill="none" /><Area type="monotone" dataKey="errors" stroke="#c54444" strokeWidth={1.4} fill="none" /></AreaChart></ResponsiveContainer> : <EmptyState title={localizedMessage(isZh, "v2.stats.noTrendData")} />}
        </div>
      </Surface>

      <div className="v2-distribution-grid">
        <Surface title={localizedMessage(isZh, "v2.stats.modelDistribution")} description={localizedMessage(isZh, "v2.stats.rankedByRequestCount")}><RankBars items={modelRanks} empty={localizedMessage(isZh, "v2.stats.noModelRequests")} tone="primary" /></Surface>
        <Surface title={localizedMessage(isZh, "v2.stats.errorsByModel")} description={localizedMessage(isZh, "v2.stats.rankedByErrorCount")}><RankBars items={errorRanks} empty={localizedMessage(isZh, "v2.stats.noErrorsInThisWindow")} tone="danger" /></Surface>
      </div>

      <div className="v2-distribution-grid v2-ranking-grid">
        <Surface className="v2-table-surface" title={localizedMessage(isZh, "v2.stats.upstreamRanking")} description={localizedMessage(isZh, "v2.stats.requestVolumeErrorsAndLatency")}><DataTable columns={upstreamColumns} rows={upstreamStats.slice(0, 8)} rowKey={(item) => item.upstream_id} empty={<EmptyState title={localizedMessage(isZh, "v2.stats.noUpstreamData")} />} /></Surface>
        <Surface className="v2-table-surface" title={localizedMessage(isZh, "v2.stats.consumerRanking")} description={localizedMessage(isZh, "v2.stats.resourceUseByIdentity")}><DataTable columns={consumerColumns} rows={consumerStats.slice(0, 8)} rowKey={(item) => item.consumer_id} empty={<EmptyState title={localizedMessage(isZh, "v2.stats.noConsumerData")} />} /></Surface>
      </div>
    </PageLayout>
  );
}

function RankBars({ items, empty, tone }: { items: Array<{ id: string; label: string; value: number; share: number }>; empty: string; tone: "primary" | "danger" }) {
  if (!items.length) return <EmptyState title={empty} />;
  return (
    <div className={`v2-rank-bars tone-${tone}`}>
      {items.map((item) => <div key={item.id}><strong title={item.label}>{item.label}</strong><span><i style={{ width: `${item.share}%` }} /></span><code>{compact(item.value)}</code></div>)}
    </div>
  );
}
