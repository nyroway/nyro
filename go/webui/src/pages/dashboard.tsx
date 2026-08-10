import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  Area,
  AreaChart,
  CartesianGrid,
  ResponsiveContainer,
  Tooltip as ChartTooltip,
  XAxis,
  YAxis,
} from "recharts";

import { PageHeader } from "@/components/v2/page-header";
import { DashboardWorkspace } from "@/components/v2/dashboard-workspace";
import { PageLayout } from "@/components/v2/page-layout";
import { backend } from "@/lib/backend";
import { summarizeDashboardRuntime } from "@/lib/dashboard-runtime";
import { useLocale } from "@/lib/i18n";
import type {
  GatewayNode,
  LogPage,
  RequestLog,
  Route,
  RouteStats,
  RuntimeService,
  StatsHourly,
  StatsOverview,
  Upstream,
  UpstreamStats,
} from "@/lib/types";

function formatCompact(value: number) {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function formatLatency(value: number | null | undefined) {
  if (value == null) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s` : `${Math.round(value)}ms`;
}

function statusCode(log: RequestLog) {
  return log.response_status_code ?? log.upstream_status_code ?? 0;
}

export default function DashboardPage() {
  const { locale, t } = useLocale();
  const navigate = useNavigate();
  const [activityTab, setActivityTab] = useState<"errors" | "requests">("errors");

  const overviewQuery = useQuery<StatsOverview>({
    queryKey: ["stats-overview"],
    queryFn: () => backend("get_stats_overview", { hours: 24 }),
    refetchInterval: 10_000,
  });
  const hourlyQuery = useQuery<StatsHourly[]>({
    queryKey: ["stats-hourly"],
    queryFn: () => backend("get_stats_hourly", { hours: 24 }),
    refetchInterval: 30_000,
  });
  const routeStatsQuery = useQuery<RouteStats[]>({
    queryKey: ["stats-routes"],
    queryFn: () => backend("get_stats_by_route", { hours: 24 }),
    refetchInterval: 30_000,
  });
  const upstreamStatsQuery = useQuery<UpstreamStats[]>({
    queryKey: ["stats-upstreams"],
    queryFn: () => backend("get_stats_by_upstream", { hours: 24 }),
    refetchInterval: 30_000,
  });
  const providersQuery = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => backend("list_upstreams"),
  });
  const routesQuery = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_routes"),
  });
  const nodesQuery = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => backend("list_nodes"),
    refetchInterval: 10_000,
  });
  const servicesQuery = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => backend("list_runtime_services"),
    refetchInterval: 30_000,
  });
  const logsQuery = useQuery<LogPage>({
    queryKey: ["logs", "dashboard"],
    queryFn: () => backend("query_logs", { query: { limit: 12, offset: 0 } }),
    refetchInterval: 10_000,
  });

  const overview = overviewQuery.data;
  const routeStats = routeStatsQuery.data ?? [];
  const upstreamStats = upstreamStatsQuery.data ?? [];
  const providers = providersQuery.data ?? [];
  const routes = routesQuery.data ?? [];
  const nodes = nodesQuery.data ?? [];
  const logs = logsQuery.data?.items ?? [];
  const errors = logs.filter((log) => statusCode(log) >= 400);

  const totalRequests = overview?.total_requests ?? 0;
  const errorRate = totalRequests > 0 ? ((overview?.error_count ?? 0) / totalRequests) * 100 : 0;
  const successRate = totalRequests > 0 ? 100 - errorRate : 100;
  const activeRoutes = routes.filter((route) => route.enabled).length;
  const enabledProviders = providers.filter((provider) => provider.enabled);
  const runtime = summarizeDashboardRuntime(
    nodesQuery.data,
    servicesQuery.data,
    nodesQuery.isError || servicesQuery.isError,
  );
  const { configVersion, runningServices, state: runtimeState } = runtime;

  const chartData = (hourlyQuery.data ?? []).map((point) => ({
      hour: point.hour.slice(11, 16),
      requests: point.request_count,
      errors: point.error_count,
    }));

  const providerStats = new Map(upstreamStats.map((item) => [item.upstream_id, item]));
  const providerByID = new Map(providers.map((provider) => [provider.id, provider]));
  const routeByID = new Map(routes.map((route) => [route.id, route]));
  const healthyProviders = enabledProviders.filter((provider) => {
    const stats = providerStats.get(provider.id);
    return !stats || stats.request_count === 0 || stats.error_count / stats.request_count < 0.1;
  }).length;

  const metrics = [
    { label: t("dashboard.todayRequests"), value: formatCompact(totalRequests), detail: t("common.requests") },
    {
      label: t("dashboard.tokenUsage"),
      value: formatCompact((overview?.total_input_tokens ?? 0) + (overview?.total_output_tokens ?? 0)),
      detail: t("dashboard.inputOutputTokens"),
    },
    { label: t("dashboard.p95Latency"), value: formatLatency(overview?.p95_duration_ms), detail: "OTLP" },
    { label: t("dashboard.errorRate"), value: `${errorRate.toFixed(2)}%`, detail: t("dashboard.errorThreshold") },
    {
      label: t("dashboard.activeModels"),
      value: String(activeRoutes),
      detail: t("dashboard.fromProviders", { count: enabledProviders.length }),
    },
  ];

  return (
    <PageLayout
      header={<PageHeader title={t("page.dashboard.title")} description={t("page.dashboard.subtitle")} />}
    >
      <section className={`v2-status-ribbon state-${runtimeState}`}>
        <div className="v2-status-ribbon-main">
          <h2>
            {runtimeState === "healthy"
              ? t("dashboard.runtimeHealthy")
              : runtimeState === "degraded"
                ? t("dashboard.runtimeDegraded")
                : t("dashboard.runtimeUnknown")}
          </h2>
          <p>
            {runtimeState === "healthy"
              ? t("dashboard.runtimeSummary", { services: runningServices, nodes: nodes.length })
              : runtimeState === "degraded"
                ? t("dashboard.runtimeDegradedSummary")
                : t("gateway.unknownDetail")}
          </p>
        </div>
        <RibbonStat label={t("dashboard.successRate")} value={`${successRate.toFixed(2)}%`} />
        <RibbonStat label={t("dashboard.configVersion")} value={configVersion} mono />
        <RibbonStat label={t("dashboard.workerNodes")} value={String(nodes.length)} />
      </section>

      <section className="v2-metric-ledger">
        {metrics.map((metric) => (
          <div className="v2-ledger-item" key={metric.label}>
            <span>{metric.label}</span><strong>{metric.value}</strong><small>{metric.detail}</small>
          </div>
        ))}
      </section>

      <DashboardWorkspace>
        <article className="v2-surface v2-dashboard-trend">
          <SurfaceHead title={t("dashboard.requestTrend")} detail={t("dashboard.requestTrendDetail")}
            action={<div className="v2-chart-legend"><span><i />{t("common.requests")}</span><span><i className="error" />{t("common.errors")}</span></div>} />
          <div className="v2-chart-wrap">
            {chartData.length === 0 ? <EmptyText>{t("dashboard.noTraffic")}</EmptyText> : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={chartData} margin={{ top: 12, right: 12, bottom: 0, left: -16 }}>
                  <defs>
                    <linearGradient id="requests-fill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#285cc4" stopOpacity={0.15} />
                      <stop offset="100%" stopColor="#285cc4" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid stroke="#dfe3e7" strokeDasharray="3 5" vertical={false} />
                  <XAxis dataKey="hour" axisLine={false} tickLine={false} tick={{ fill: "#87909b", fontSize: 10 }} />
                  <YAxis axisLine={false} tickLine={false} tick={{ fill: "#87909b", fontSize: 10 }} allowDecimals={false} />
                  <ChartTooltip contentStyle={{ border: "1px solid #c5cbd2", borderRadius: 7, fontSize: 12 }} />
                  <Area type="monotone" dataKey="requests" name={t("common.requests")} stroke="#285cc4" strokeWidth={2} fill="url(#requests-fill)" />
                  <Area type="monotone" dataKey="errors" name={t("common.errors")} stroke="#b83a3a" strokeWidth={1.5} fill="transparent" />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
        </article>

        <aside className="v2-surface v2-health-panel">
          <SurfaceHead title={t("dashboard.upstreamHealth")} detail={t("dashboard.upstreamHealthDetail")} />
          <div className="v2-health-list">
            {providers.slice(0, 5).map((provider) => {
              const stats = providerStats.get(provider.id);
              const providerErrorRate = stats && stats.request_count > 0 ? stats.error_count / stats.request_count : 0;
              const state = !provider.enabled ? "inactive" : providerErrorRate >= 0.1 ? "degraded" : "available";
              return (
                <button type="button" className="v2-health-row" key={provider.id} onClick={() => navigate(`/providers?focus=${provider.id}`)}>
                  <span className="v2-provider-mark">{provider.name.slice(0, 2).toUpperCase()}</span>
                  <span className="v2-health-meta"><strong>{provider.name}</strong><small>{provider.protocol || provider.provider || "—"} · {provider.models?.length ?? 0}</small></span>
                  <span className="v2-health-value"><strong>{formatLatency(stats?.p95_duration_ms ?? stats?.avg_duration_ms)}</strong><Status tone={state === "available" ? "success" : state === "degraded" ? "danger" : "neutral"} label={t(`dashboard.${state}`)} /></span>
                </button>
              );
            })}
            {providers.length === 0 && <EmptyText>{t("dashboard.acceptingTraffic", { healthy: 0, total: 0 })}</EmptyText>}
          </div>
          <div className="v2-surface-foot"><span>{t("dashboard.acceptingTraffic", { healthy: healthyProviders, total: providers.length })}</span><button onClick={() => navigate("/providers")}>{t("dashboard.manageProviders")}</button></div>
        </aside>

        <article className="v2-surface v2-route-performance">
          <SurfaceHead title={t("dashboard.modelPerformance")} detail={t("dashboard.modelPerformanceDetail")}
            action={<button className="v2-button" onClick={() => navigate("/models")}>{t("dashboard.manageModels")}</button>} />
          <div className="v2-table-wrap">
            <table className="v2-data-table">
              <thead><tr><th>{t("dashboard.model")}</th><th>{t("dashboard.primaryProvider")}</th><th>{t("common.requests")}</th><th>{t("dashboard.success")}</th><th>P95</th><th>{t("dashboard.status")}</th></tr></thead>
              <tbody>
                {routeStats.slice(0, 6).map((stats) => {
                  const route = routeByID.get(stats.route_id);
                  const primary = route?.upstreams?.[0];
                  const provider = primary ? providerByID.get(primary.upstream_id) : undefined;
                  const routeSuccess = stats.request_count > 0 ? 100 - (stats.error_count / stats.request_count) * 100 : 100;
                  return (
                    <tr key={stats.route_id} onClick={() => navigate(`/models?focus=${stats.route_id}`)}>
                      <td><strong className="v2-mono">{stats.route_model || route?.model || stats.route_id}</strong><small>{route?.balance || "—"} · {route?.upstreams?.length ?? 0}</small></td>
                      <td>{provider?.name || "—"}</td><td>{formatCompact(stats.request_count)}</td><td>{routeSuccess.toFixed(2)}%</td><td>{formatLatency(stats.p95_duration_ms ?? stats.avg_duration_ms)}</td>
                      <td><Status tone={route?.enabled === false ? "neutral" : "success"} label={route?.enabled === false ? t("common.disabled") : t("common.running")} /></td>
                    </tr>
                  );
                })}
                {routeStats.length === 0 && <tr><td colSpan={6}><EmptyText>{t("dashboard.noModelData")}</EmptyText></td></tr>}
              </tbody>
            </table>
          </div>
        </article>

        <aside className="v2-surface v2-activity-panel">
          <SurfaceHead title={t("dashboard.runtimeActivity")} detail={t("dashboard.runtimeActivityDetail")}
            action={<div className="v2-activity-tabs"><button className={activityTab === "errors" ? "active" : ""} onClick={() => setActivityTab("errors")}>{t("dashboard.incidents")}<span>{errors.length}</span></button><button className={activityTab === "requests" ? "active" : ""} onClick={() => setActivityTab("requests")}>{t("dashboard.recentRequests")}</button></div>} />
          <div className="v2-activity-body">
            {(activityTab === "errors" ? errors : logs).slice(0, 4).map((log) => <ActivityRow key={log.id} log={log} locale={locale} onClick={() => navigate(`/logs?focus=${log.id}`)} />)}
            {(activityTab === "errors" ? errors : logs).length === 0 && <EmptyText>{activityTab === "errors" ? t("dashboard.noIncidents") : t("dashboard.noRequests")}</EmptyText>}
          </div>
          <div className="v2-surface-foot"><span>{activityTab === "errors" ? t("dashboard.incidentNote") : t("dashboard.requestNote")}</span><button onClick={() => navigate("/logs")}>{t("dashboard.allLogs")}</button></div>
        </aside>
      </DashboardWorkspace>
    </PageLayout>
  );
}

function RibbonStat({ label, value, mono = false }: { label: string; value: string; mono?: boolean }) {
  return <div className="v2-status-ribbon-stat"><span>{label}</span><strong className={mono ? "v2-mono" : undefined}>{value}</strong></div>;
}

function SurfaceHead({ title, detail, action }: { title: string; detail: string; action?: React.ReactNode }) {
  return <div className="v2-surface-head"><div><h2>{title}</h2><p>{detail}</p></div>{action}</div>;
}

function Status({ tone, label }: { tone: "success" | "danger" | "neutral"; label: string }) {
  return <span className={`v2-status status-${tone}`}><i aria-hidden="true" />{label}</span>;
}

function EmptyText({ children }: { children: React.ReactNode }) {
  return <div className="v2-inline-empty">{children}</div>;
}

function ActivityRow({ log, locale, onClick }: { log: RequestLog; locale: string; onClick: () => void }) {
  const code = statusCode(log);
  const time = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit", second: "2-digit" }).format(new Date(log.created_at));
  return (
    <button type="button" className="v2-activity-row" onClick={onClick}>
      <Status tone={code >= 500 ? "danger" : code >= 400 ? "neutral" : "success"} label={code ? String(code) : "—"} />
      <span><strong className="v2-mono">{log.route_model || log.client_model || "—"}</strong><small>{log.consumer_key_name || log.consumer_id || "—"} · {log.upstream_name || log.upstream_id || "—"}</small></span>
      <span><strong className="v2-mono">{time}</strong><small className="v2-mono">{formatLatency(log.latency_total_ms)}</small></span>
    </button>
  );
}
