import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Check, CircleHelp, TriangleAlert } from "lucide-react";
import clsx from "clsx";

import { consumersApi } from "@/lib/api/consumers";
import { logsApi } from "@/lib/api/logs";
import { routesApi } from "@/lib/api/routes";
import { statsApi } from "@/lib/api/stats";
import { systemApi } from "@/lib/api/system";
import { upstreamsApi } from "@/lib/api/upstreams";
import { NyroChart, type NyroChartDatum } from "@/components/ui/nyro-chart";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { summarizeDashboardRuntime } from "@/lib/dashboard-runtime";
import { useLocale } from "@/lib/i18n";
import type {
  Consumer,
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

  const overviewQuery = useQuery<StatsOverview>({
    queryKey: ["stats-overview"],
    queryFn: () => statsApi.overview(24),
    refetchInterval: 10_000,
  });
  const hourlyQuery = useQuery<StatsHourly[]>({
    queryKey: ["stats-hourly"],
    queryFn: () => statsApi.hourly(24),
    refetchInterval: 30_000,
  });
  const routeStatsQuery = useQuery<RouteStats[]>({
    queryKey: ["stats-routes"],
    queryFn: () => statsApi.byRoute(24),
    refetchInterval: 30_000,
  });
  const upstreamStatsQuery = useQuery<UpstreamStats[]>({
    queryKey: ["stats-upstreams"],
    queryFn: () => statsApi.byUpstream(24),
    refetchInterval: 30_000,
  });
  const providersQuery = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => upstreamsApi.list(),
  });
  const routesQuery = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => routesApi.list(),
  });
  const consumersQuery = useQuery<Consumer[]>({
    queryKey: ["consumers"],
    queryFn: () => consumersApi.list(),
  });
  const nodesQuery = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => systemApi.nodes(),
    refetchInterval: 10_000,
  });
  const servicesQuery = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => systemApi.runtimeServices(),
    refetchInterval: 30_000,
  });
  const logsQuery = useQuery<LogPage>({
    queryKey: ["logs", "dashboard"],
    queryFn: () => logsApi.query({ limit: 12, offset: 0 }),
    refetchInterval: 10_000,
  });

  const overview = overviewQuery.data;
  const routeStats = routeStatsQuery.data ?? [];
  const upstreamStats = upstreamStatsQuery.data ?? [];
  const providers = providersQuery.data ?? [];
  const routes = routesQuery.data ?? [];
  const consumers = consumersQuery.data ?? [];
  const nodes = nodesQuery.data ?? [];
  const logs = logsQuery.data?.items ?? [];
  const errors = logs.filter((log) => statusCode(log) >= 400);

  const totalRequests = overview?.total_requests ?? 0;
  const errorRate = totalRequests > 0 ? ((overview?.error_count ?? 0) / totalRequests) * 100 : 0;
  const successRate = totalRequests > 0 ? 100 - errorRate : 100;
  const activeRoutes = routes.filter((route) => route.enabled).length;
  const runtime = summarizeDashboardRuntime(
    nodesQuery.data,
    servicesQuery.data,
    nodesQuery.isError || servicesQuery.isError,
  );
  const { configVersion, runningServices, state: runtimeState } = runtime;

  const stateTitle = runtimeState === "healthy"
    ? t("dashboard.runtimeHealthy")
    : runtimeState === "degraded"
      ? t("dashboard.runtimeDegraded")
      : t("dashboard.runtimeUnknown");
  // 基线把配置版本放在健康条摘要文字里（“配置版本 rev-99 已同步”）；
  // 节点版本不一致（mixed）时“已同步”不成立，回落到无版本文案。
  const stateSummary = runtimeState === "healthy"
    ? (configVersion.startsWith("rev-")
        ? t("dashboard.runtimeSummaryVersioned", { services: runningServices, nodes: nodes.length, rev: configVersion })
        : t("dashboard.runtimeSummary", { services: runningServices, nodes: nodes.length }))
    : runtimeState === "degraded"
      ? t("dashboard.runtimeDegradedSummary")
      : t("gateway.unknownDetail");
  const stateIcon = runtimeState === "healthy"
    ? <Check />
    : runtimeState === "degraded"
      ? <TriangleAlert />
      : <CircleHelp />;

  const chartData: NyroChartDatum[] = (hourlyQuery.data ?? []).map((point) => ({
    label: point.hour.slice(11, 16),
    value: point.request_count,
    error: point.error_count,
  }));

  const providerStats = new Map(upstreamStats.map((item) => [item.upstream_id, item]));
  const providerByID = new Map(providers.map((provider) => [provider.id, provider]));
  const routeByID = new Map(routes.map((route) => [route.id, route]));

  // 基线指标带：24 小时请求是完整千分位数字（2,847,291），Token 用紧凑格式（43.8M），
  // Token 的 hint 是真实的输入/输出拆分，错误率 hint 带 < 号和两位小数。
  const inputTokens = overview?.total_input_tokens ?? 0;
  const outputTokens = overview?.total_output_tokens ?? 0;
  const metrics = [
    {
      label: t("dashboard.todayRequests"),
      value: new Intl.NumberFormat(locale).format(totalRequests),
      detail: t("common.requests"),
    },
    {
      label: t("dashboard.tokenUsage"),
      value: formatCompact(inputTokens + outputTokens),
      detail: t("dashboard.tokenSplit", { input: formatCompact(inputTokens), output: formatCompact(outputTokens) }),
    },
    { label: t("dashboard.p95Latency"), value: formatLatency(overview?.p95_duration_ms), detail: "OTLP" },
    { label: t("dashboard.errorRate"), value: `${errorRate.toFixed(2)}%`, detail: t("dashboard.errorThreshold") },
    {
      label: t("dashboard.activeModels"),
      value: String(activeRoutes),
      detail: t("dashboard.fromProviders", { count: providers.filter((provider) => provider.enabled).length }),
    },
  ];

  const recentSamples = logs.slice(0, 6);

  return (
    <PageLayout
      header={<PageHeader title={t("page.dashboard.title")} description={t("page.dashboard.subtitle")} />}
    >
      <section
        className={clsx("health-strip", runtimeState === "degraded" && "warn", runtimeState === "unknown" && "unknown")}
        aria-label={t("dashboard.runtimeStatus")}
      >
        <div className="health-icon" aria-hidden="true">{stateIcon}</div>
        <div className="health-strip-copy">
          <h3>{stateTitle}</h3>
          <p>{stateSummary}</p>
        </div>
        <div className="health-caps">
          <div className="health-cap"><span>{t("nav.providers")}</span><strong>{providers.length}</strong></div>
          <div className="health-cap"><span>{t("nav.models")}</span><strong>{routes.length}</strong></div>
          <div className="health-cap"><span>{t("dashboard.successRate")}</span><strong>{successRate.toFixed(2)}%</strong></div>
          <div className="health-cap"><span>{t("dashboard.workerNodes")}</span><strong>{nodes.length}</strong></div>
          <div className="health-cap"><span>{t("dashboard.runningServices")}</span><strong>{runningServices}</strong></div>
          <div className="health-cap"><span>{t("nav.apiKeys")}</span><strong>{consumers.length}</strong></div>
        </div>
      </section>

      <section className="metric-strip" aria-label={t("dashboard.runtimeStatus")}>
        <div className="metric-strip-stats">
          {metrics.map((metric) => (
            <div className="metric-cell" key={metric.label}>
              <span>{metric.label}</span><strong>{metric.value}</strong><i className="metric-hint">{metric.detail}</i>
            </div>
          ))}
        </div>
      </section>

      <section className="section-grid split-grid">
        <article className="card chart-full">
          <div className="card-header">
            <div>
              <div className="card-title-row">
                <h2 className="card-title">{t("dashboard.requestTrend")}</h2>
                <span className="live-pill"><span className="status-dot" /><span>{t("dashboard.live")}</span></span>
              </div>
              <div className="card-subtitle">{t("dashboard.requestTrendDetail")}</div>
            </div>
          </div>
          {chartData.length === 0 ? (
            <div className="chart-wrap"><div className="empty">{t("dashboard.noTraffic")}</div></div>
          ) : (
            <NyroChart
              data={chartData}
              ariaLabel={t("dashboard.requestTrendDetail")}
              valueLabel={t("common.requests")}
              errorLabel={t("common.errors")}
            />
          )}
          <div className="chart-legend">
            <span className="chart-legend-item"><span className="legend-dot" /><span>{t("common.requests")}</span></span>
            <span className="chart-legend-item"><span className="legend-dot error" /><span>{t("common.errors")}</span></span>
          </div>
        </article>

        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t("dashboard.upstreamHealth")}</h2>
              <div className="card-subtitle">{t("dashboard.upstreamHealthDetail")}</div>
            </div>
          </div>
          <div className="upstream-list">
            {providers.slice(0, 5).map((provider) => {
              const stats = providerStats.get(provider.id);
              const providerErrorRate = stats && stats.request_count > 0 ? stats.error_count / stats.request_count : 0;
              const state = !provider.enabled ? "inactive" : providerErrorRate >= 0.1 ? "degraded" : "available";
              return (
                <button
                  type="button"
                  className="upstream-item"
                  key={provider.id}
                  onClick={() => navigate(`/providers?focus=${provider.id}`)}
                >
                  <span>
                    <span className="upstream-name">{provider.name}</span>
                    <span className="upstream-meta">
                      {provider.protocol || provider.provider || "—"} · {provider.models?.length ?? 0}
                    </span>
                  </span>
                  <span className={clsx("health", state === "degraded" && "warn", state === "inactive" && "is-idle")}>
                    <span className="mini-dot" /><span>{t(`dashboard.${state}`)}</span>
                  </span>
                </button>
              );
            })}
            {providers.length === 0 && <div className="empty">{t("dashboard.noProviders")}</div>}
          </div>
        </article>
      </section>

      <section className="section-grid split-grid">
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t("dashboard.modelPerformance")}</h2>
              <div className="card-subtitle">{t("dashboard.modelPerformanceDetail")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{t("dashboard.model")}</th>
                  <th>{t("dashboard.primaryProvider")}</th>
                  <th>{t("common.requests")}</th>
                  <th>{t("dashboard.success")}</th>
                  <th>P95</th>
                  <th>{t("dashboard.status")}</th>
                </tr>
              </thead>
              <tbody>
                {routeStats.slice(0, 6).map((stats) => {
                  const route = routeByID.get(stats.route_id);
                  const primary = route?.upstreams?.[0];
                  const provider = primary ? providerByID.get(primary.upstream_id) : undefined;
                  const routeSuccess = stats.request_count > 0 ? 100 - (stats.error_count / stats.request_count) * 100 : 100;
                  return (
                    <tr
                      key={stats.route_id}
                      className="clickable"
                      tabIndex={0}
                      onClick={() => navigate(`/models?focus=${stats.route_id}`)}
                      onKeyDown={(event) => {
                        if (event.key === "Enter" || event.key === " ") navigate(`/models?focus=${stats.route_id}`);
                      }}
                    >
                      <td>
                        <div className="cell-stack">
                          <strong>{stats.route_model || route?.model || stats.route_id}</strong>
                          <small>{route?.balance || "—"} · {route?.upstreams?.length ?? 0}</small>
                        </div>
                      </td>
                      <td>{provider?.name || "—"}</td>
                      <td>{formatCompact(stats.request_count)}</td>
                      <td>{routeSuccess.toFixed(2)}%</td>
                      <td>{formatLatency(stats.p95_duration_ms ?? stats.avg_duration_ms)}</td>
                      <td>
                        <span className={clsx("health", route?.enabled === false && "is-idle")}>
                          <span className="mini-dot" />
                          <span>{route?.enabled === false ? t("common.disabled") : t("common.running")}</span>
                        </span>
                      </td>
                    </tr>
                  );
                })}
                {routeStats.length === 0 && (
                  <tr><td colSpan={6}><div className="empty">{t("dashboard.noModelData")}</div></td></tr>
                )}
              </tbody>
            </table>
          </div>
        </article>

        <article className="card notice-card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{t("dashboard.runtimeActivity")}</h2>
              <div className="card-subtitle">{t("dashboard.runtimeActivityDetail")}</div>
            </div>
            <div className="sample-chips">
              <span className="sample-chip">{t("dashboard.recentErrors", { count: errors.length })}</span>
              <span className="sample-chip">{t("dashboard.sampledRequests", { count: logs.length })}</span>
            </div>
          </div>
          <div className="sample-list">
            <div className="sample-item sample-head">
              <span>{t("dashboard.time")}</span>
              <span>{t("dashboard.result")}</span>
              <span>{t("dashboard.modelProvider")}</span>
              <span>{t("dashboard.latency")}</span>
            </div>
            {recentSamples.map((log) => {
              const code = statusCode(log);
              const time = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(new Date(log.created_at));
              return (
                <button
                  type="button"
                  className={clsx("sample-item", code >= 400 ? "err" : "ok")}
                  key={log.id}
                  onClick={() => navigate(`/logs?focus=${log.id}`)}
                >
                  <span className="sample-time">{time}</span>
                  <span className="sample-result">{code ? String(code) : "—"}</span>
                  <span className="sample-copy">
                    {log.route_model || log.client_model || "—"}
                    <span>{log.upstream_name || log.upstream_id || "—"}</span>
                  </span>
                  <span className="sample-latency">{formatLatency(log.latency_total_ms)}</span>
                </button>
              );
            })}
            {recentSamples.length === 0 && <div className="empty">{t("dashboard.noRequests")}</div>}
          </div>
        </article>
      </section>
    </PageLayout>
  );
}
