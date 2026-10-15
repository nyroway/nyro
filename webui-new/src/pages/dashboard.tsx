import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Check, CircleHelp, TriangleAlert } from "lucide-react";
import clsx from "clsx";

import { backend } from "@/lib/backend";
import type {
  ApiKey,
  GatewayStatus,
  LogPage,
  Model as ModelType,
  ModelStats,
  Provider,
  ProviderStats,
  RequestLog,
  StatsHourly,
  StatsOverview,
} from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { NyroChart } from "@/components/ui/nyro-chart";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { localizedMessage } from "@/lib/messages";


function formatCompact(value: number) {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function formatLatency(value: number | null | undefined) {
  if (value == null) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s` : `${Math.round(value)}ms`;
}

function statusCode(log: RequestLog) {
  return log.client_status_code ?? log.upstream_status_code ?? 0;
}

export default function DashboardPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const navigate = useNavigate();

  const { data: overview } = useQuery<StatsOverview>({
    queryKey: ["stats-overview"],
    queryFn: () => backend("get_stats_overview"),
    refetchInterval: 10_000,
  });
  const { data: hourly = [] } = useQuery<StatsHourly[]>({
    queryKey: ["stats-hourly"],
    queryFn: () => backend("get_stats_hourly", { hours: 24 }),
    refetchInterval: 30_000,
  });
  const { data: modelStats = [] } = useQuery<ModelStats[]>({
    queryKey: ["stats-models"],
    queryFn: () => backend("get_stats_by_model"),
    refetchInterval: 30_000,
  });
  const { data: providerStats = [] } = useQuery<ProviderStats[]>({
    queryKey: ["stats-providers"],
    queryFn: () => backend("get_stats_by_provider"),
    refetchInterval: 30_000,
  });
  const { data: status } = useQuery<GatewayStatus>({
    queryKey: ["gateway-status"],
    queryFn: () => backend("get_gateway_status"),
  });
  const { data: providers = [] } = useQuery<Provider[]>({
    queryKey: ["providers"],
    queryFn: () => backend("get_providers"),
  });
  const { data: routes = [] } = useQuery<ModelType[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_models"),
  });
  const { data: apiKeys = [] } = useQuery<ApiKey[]>({
    queryKey: ["api-keys"],
    queryFn: () => backend("list_api_keys"),
  });
  const { data: logsPage } = useQuery<LogPage>({
    queryKey: ["logs", "dashboard"],
    queryFn: () => backend("query_logs", { query: { limit: 12, offset: 0 } }),
    refetchInterval: 10_000,
  });

  const logs = logsPage?.items ?? [];
  const errors = logs.filter((log) => statusCode(log) >= 400);
  const totalRequests = overview?.total_requests ?? 0;
  const errorRate = totalRequests > 0 ? ((overview?.error_count ?? 0) / totalRequests) * 100 : 0;
  const successRate = totalRequests > 0 ? 100 - errorRate : 100;
  const running = status?.status === "running";
  const runtimeState = !running ? "unknown" : errorRate >= 1 ? "degraded" : "healthy";
  const enabledProviders = providers.filter((provider) => provider.is_enabled).length;
  const activeModels = routes.filter((route) => route.is_enabled).length;
  const inputTokens = overview?.total_input_tokens ?? 0;
  const outputTokens = overview?.total_output_tokens ?? 0;

  const stateTitle = runtimeState === "healthy"
    ? (localizedMessage(isZh, "dashboard.gatewayIsHealthy"))
    : runtimeState === "degraded"
      ? (localizedMessage(isZh, "dashboard.gatewayIsDegraded"))
      : (localizedMessage(isZh, "dashboard.gatewayStatusUnknown"));
  const stateSummary = running
    ? (localizedMessage(isZh, "dashboard.proxyRunningOnPort", { a: status?.proxy_port ?? "–" }))
    : (localizedMessage(isZh, "dashboard.proxyIsNotRunning"));
  const stateIcon = runtimeState === "healthy"
    ? <Check />
    : runtimeState === "degraded"
      ? <TriangleAlert />
      : <CircleHelp />;

  const chartData = hourly.map((point) => ({
    label: point.hour.slice(11, 16),
    value: point.request_count,
    error: point.error_count,
  }));
  const routeByName = new Map(routes.map((route) => [route.name, route]));
  const recentSamples = logs.slice(0, 6);

  const metrics = [
    {
      label: localizedMessage(isZh, "dashboard.requests24h"),
      value: new Intl.NumberFormat(locale).format(totalRequests),
      detail: localizedMessage(isZh, "dashboard.totalRequests"),
    },
    {
      label: localizedMessage(isZh, "dashboard.tokenUsage"),
      value: formatCompact(inputTokens + outputTokens),
      detail: localizedMessage(isZh, "common.inOut", { a: formatCompact(inputTokens), b: formatCompact(outputTokens) }),
    },
    {
      label: localizedMessage(isZh, "common.avgLatency"),
      value: formatLatency(overview?.avg_duration_ms),
      detail: localizedMessage(isZh, "common.allRequests"),
    },
    {
      label: localizedMessage(isZh, "common.errorRate"),
      value: `${errorRate.toFixed(2)}%`,
      detail: localizedMessage(isZh, "dashboard.target100"),
    },
    {
      label: localizedMessage(isZh, "dashboard.activeModels"),
      value: String(activeModels),
      detail: localizedMessage(isZh, "dashboard.fromEnabledProviders", { a: enabledProviders }),
    },
  ];

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "dashboard.overview")}
          description={localizedMessage(isZh, "dashboard.checkGatewayHealthFirstThenTrafficUpstreamsAndModels")}
        />
      )}
    >
      <section
        className={clsx("health-strip", runtimeState === "degraded" && "warn", runtimeState === "unknown" && "unknown")}
        aria-label={localizedMessage(isZh, "dashboard.platformHealth")}
      >
        <div className="health-icon" aria-hidden="true">{stateIcon}</div>
        <div className="health-strip-copy">
          <h3>{stateTitle}</h3>
          <p>{stateSummary}</p>
        </div>
        <div className="health-caps">
          <div className="health-cap"><span>{localizedMessage(isZh, "common.providers")}</span><strong>{providers.length}</strong></div>
          <div className="health-cap"><span>{localizedMessage(isZh, "common.models")}</span><strong>{routes.length}</strong></div>
          <div className="health-cap"><span>{localizedMessage(isZh, "dashboard.success")}</span><strong>{successRate.toFixed(2)}%</strong></div>
          <div className="health-cap"><span>{localizedMessage(isZh, "common.keys")}</span><strong>{apiKeys.length}</strong></div>
        </div>
      </section>

      <section className="metric-strip" aria-label={localizedMessage(isZh, "dashboard.gatewayStatus")}>
        <div className="metric-strip-stats">
          {metrics.map((metric) => (
            <div className="metric-cell" key={metric.label}>
              <span>{metric.label}</span>
              <strong>{metric.value}</strong>
              <small className="metric-hint">{metric.detail}</small>
            </div>
          ))}
        </div>
      </section>

      <section className="section-grid split-grid">
        <article className="card chart-full">
          <div className="card-header">
            <div>
              <div className="card-title-row">
                <h2 className="card-title">{localizedMessage(isZh, "common.requestTrend")}</h2>
                <span className="live-pill"><span className="status-dot" /><span>{localizedMessage(isZh, "dashboard.live")}</span></span>
              </div>
              <div className="card-subtitle">{localizedMessage(isZh, "dashboard.hourlyRequestsAndErrorsOverTheLast24Hours")}</div>
            </div>
          </div>
          {chartData.length < 2 ? (
            <div className="chart-wrap"><div className="empty">{localizedMessage(isZh, "dashboard.noTrafficDataYet")}</div></div>
          ) : (
            <NyroChart
              data={chartData}
              ariaLabel={localizedMessage(isZh, "dashboard.requestsOver24Hours")}
              valueLabel={localizedMessage(isZh, "common.requests")}
              errorLabel={localizedMessage(isZh, "common.errors")}
            />
          )}
          <div className="chart-legend">
            <span className="chart-legend-item"><span className="legend-dot" /><span>{localizedMessage(isZh, "common.requests2")}</span></span>
            <span className="chart-legend-item"><span className="legend-dot error" /><span>{localizedMessage(isZh, "common.errors2")}</span></span>
          </div>
        </article>

        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "dashboard.upstreamHealth")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "dashboard.availabilityFromEnablementAndRecentTraffic")}</div>
            </div>
          </div>
          <div className="upstream-list">
            {providers.slice(0, 5).map((provider) => {
              const stats = providerStats.find((item) => item.provider === provider.name);
              const providerErrorRate = stats && stats.request_count > 0 ? stats.error_count / stats.request_count : 0;
              const state = !provider.is_enabled ? "inactive" : providerErrorRate >= 0.1 ? "degraded" : "available";
              const label = state === "degraded"
                ? (localizedMessage(isZh, "dashboard.degraded"))
                : state === "inactive"
                  ? (localizedMessage(isZh, "common.disabled"))
                  : (localizedMessage(isZh, "dashboard.available"));
              return (
                <button
                  type="button"
                  className="upstream-item"
                  key={provider.id}
                  onClick={() => navigate("/providers")}
                >
                  <span>
                    <span className="upstream-name">{provider.name}</span>
                    <span className="upstream-meta">{provider.protocol || provider.vendor || "—"}</span>
                  </span>
                  <span className={clsx("health", state === "degraded" && "warn", state === "inactive" && "is-idle")}>
                    <span className="mini-dot" />
                    <span>{label}</span>
                  </span>
                </button>
              );
            })}
            {providers.length === 0 && <div className="empty">{localizedMessage(isZh, "dashboard.noProvidersYet")}</div>}
          </div>
        </article>
      </section>

      <section className="section-grid split-grid">
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "dashboard.modelPerformance")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "dashboard.topModelsByRequestVolume")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.model")}</th>
                  <th>{localizedMessage(isZh, "dashboard.provider")}</th>
                  <th>{localizedMessage(isZh, "common.requests2")}</th>
                  <th>Token</th>
                  <th>{localizedMessage(isZh, "dashboard.latency")}</th>
                  <th>{localizedMessage(isZh, "common.status")}</th>
                </tr>
              </thead>
              <tbody>
                {modelStats.slice(0, 6).map((stats) => {
                  const route = routeByName.get(stats.model);
                  return (
                    <tr key={stats.model}>
                      <td>
                        <div className="cell-stack">
                          <strong>{stats.model}</strong>
                          <small>{route?.balance || "—"}</small>
                        </div>
                      </td>
                      <td>{route?.target_provider || "—"}</td>
                      <td>{formatCompact(stats.request_count)}</td>
                      <td>{formatCompact(stats.total_input_tokens + stats.total_output_tokens)}</td>
                      <td>{formatLatency(stats.avg_duration_ms)}</td>
                      <td>
                        <span className={clsx("health", route?.is_enabled === false && "is-idle")}>
                          <span className="mini-dot" />
                          <span>{route?.is_enabled === false ? (localizedMessage(isZh, "dashboard.disabled")) : (localizedMessage(isZh, "dashboard.running"))}</span>
                        </span>
                      </td>
                    </tr>
                  );
                })}
                {modelStats.length === 0 && (
                  <tr><td colSpan={6}><div className="empty">{localizedMessage(isZh, "dashboard.noModelData")}</div></td></tr>
                )}
              </tbody>
            </table>
          </div>
        </article>

        <article className="card notice-card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "dashboard.runtimeActivity")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "dashboard.recentErrorsAndSampledRequests")}</div>
            </div>
            <div className="sample-chips">
              <span className="sample-chip">{localizedMessage(isZh, "dashboard.recentErrors", { a: errors.length })}</span>
              <span className="sample-chip">{localizedMessage(isZh, "dashboard.sampled", { a: logs.length })}</span>
            </div>
          </div>
          <div className="sample-list">
            <div className="sample-item sample-head">
              <span>{localizedMessage(isZh, "common.time")}</span>
              <span>{localizedMessage(isZh, "dashboard.result")}</span>
              <span>{localizedMessage(isZh, "dashboard.modelProvider")}</span>
              <span>{localizedMessage(isZh, "common.latency")}</span>
            </div>
            {recentSamples.map((log) => {
              const code = statusCode(log);
              const time = new Intl.DateTimeFormat(locale, { hour: "2-digit", minute: "2-digit" }).format(new Date(log.created_at));
              return (
                <button
                  type="button"
                  className={clsx("sample-item", code >= 400 ? "err" : "ok")}
                  key={log.id}
                  onClick={() => navigate("/logs")}
                >
                  <span className="sample-time">{time}</span>
                  <span className="sample-result">{code ? String(code) : "—"}</span>
                  <span className="sample-copy">
                    {log.model_name || log.client_model || "—"}
                    <span>{log.provider_name || "—"}</span>
                  </span>
                  <span className="sample-latency">{formatLatency(log.latency_total_ms)}</span>
                </button>
              );
            })}
            {recentSamples.length === 0 && <div className="empty">{localizedMessage(isZh, "dashboard.noRequestsYet")}</div>}
          </div>
        </article>
      </section>
    </PageLayout>
  );
}
