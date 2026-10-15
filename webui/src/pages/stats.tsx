import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import clsx from "clsx";

import { backend } from "@/lib/backend";
import type { ApiKeyStats, ModelStats, ProviderStats, StatsHourly, StatsOverview } from "@/lib/types";
import { formatLogTime } from "@/lib/format";
import { useLocale } from "@/lib/i18n";
import { NyroChart } from "@/components/ui/nyro-chart";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { copyableTipProps } from "@/lib/copyable";
import { localizedMessage, type MessageKey } from "@/lib/messages";


function compact(value: number) {
  return new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(value);
}

function latency(value: number | null | undefined) {
  if (value == null) return "—";
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 10_000 ? 1 : 2)}s` : `${Math.round(value)}ms`;
}

const RANGES: { hours: number; label: string; aria: MessageKey }[] = [
  { hours: 6, label: "6H", aria: "stats.last6Hours" },
  { hours: 24, label: "24H", aria: "stats.last24Hours" },
  { hours: 72, label: "3D", aria: "stats.last3Days" },
  { hours: 168, label: "7D", aria: "stats.last7Days" },
];

function ranked(items: { id: string; label: string; value: number }[], limit: number) {
  const top = [...items].sort((a, b) => b.value - a.value).slice(0, limit);
  const total = top.reduce((sum, item) => sum + item.value, 0);
  return top.map((item) => ({ ...item, share: total > 0 ? (item.value / total) * 100 : 0 }));
}

export default function StatsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const [hours, setHours] = useState(24);

  const { data: overview } = useQuery<StatsOverview>({
    queryKey: ["stats-overview", hours],
    queryFn: () => backend("get_stats_overview", { hours }),
    refetchInterval: 10_000,
  });
  const { data: hourly = [] } = useQuery<StatsHourly[]>({
    queryKey: ["stats-hourly", hours],
    queryFn: () => backend("get_stats_hourly", { hours }),
    refetchInterval: 30_000,
  });
  const { data: modelStats = [] } = useQuery<ModelStats[]>({
    queryKey: ["stats-models", hours],
    queryFn: () => backend("get_stats_by_model", { hours }),
    refetchInterval: 30_000,
  });
  const { data: providerStats = [] } = useQuery<ProviderStats[]>({
    queryKey: ["stats-providers", hours],
    queryFn: () => backend("get_stats_by_provider", { hours }),
    refetchInterval: 30_000,
  });
  const { data: apiKeyStats = [] } = useQuery<ApiKeyStats[]>({
    queryKey: ["stats-apikeys", hours],
    queryFn: () => backend("get_stats_by_api_key", { hours }),
    refetchInterval: 30_000,
  });

  const requests = overview?.total_requests ?? 0;
  const errors = overview?.error_count ?? 0;
  const totalTokens = (overview?.total_input_tokens ?? 0) + (overview?.total_output_tokens ?? 0);
  const errorRate = requests > 0 ? (errors / requests) * 100 : 0;
  const chartData = hourly.map((point) => ({
    label: point.hour.slice(11, 16),
    value: point.request_count,
    error: point.error_count,
  }));
  const modelRanks = ranked(modelStats.map((item) => ({
    id: item.model,
    label: item.model,
    value: item.request_count,
  })), 6);
  const tokenRanks = ranked(modelStats.map((item) => ({
    id: item.model,
    label: item.model,
    value: item.total_input_tokens + item.total_output_tokens,
  })), 6);

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "stats.statistics")}
          description={localizedMessage(isZh, "stats.requestsTokensLatencyAndErrorsInTheSelectedWindow")}
          actions={(
            <div className="chart-actions">
              {RANGES.map((range) => (
                <button
                  key={range.hours}
                  type="button"
                  className={clsx("period-button", hours === range.hours && "active")}
                  aria-pressed={hours === range.hours}
                  aria-label={localizedMessage(isZh, range.aria)}
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
      <section className="metric-strip">
        <div className="metric-strip-stats">
          <div className="metric-cell">
            <span>{localizedMessage(isZh, "common.requests2")}</span>
            <strong>{compact(requests)}</strong>
            <small className="metric-hint">{localizedMessage(isZh, "stats.hWindow", { a: hours })}</small>
          </div>
          <div className="metric-cell">
            <span>{localizedMessage(isZh, "stats.tokens")}</span>
            <strong>{compact(totalTokens)}</strong>
            <small className="metric-hint">
              {localizedMessage(isZh, "common.inOut", { a: compact(overview?.total_input_tokens ?? 0), b: compact(overview?.total_output_tokens ?? 0) })}
            </small>
          </div>
          <div className="metric-cell">
            <span>{localizedMessage(isZh, "common.avgLatency")}</span>
            <strong>{latency(overview?.avg_duration_ms)}</strong>
            <small className="metric-hint">{localizedMessage(isZh, "common.allRequests")}</small>
          </div>
          <div className="metric-cell">
            <span>{localizedMessage(isZh, "common.errors2")}</span>
            <strong>{compact(errors)}</strong>
            <small className="metric-hint">{localizedMessage(isZh, "stats.errorRate", { a: errorRate.toFixed(2) })}</small>
          </div>
          <div className="metric-cell">
            <span>{localizedMessage(isZh, "common.keys")}</span>
            <strong>{apiKeyStats.length}</strong>
            <small className="metric-hint">{localizedMessage(isZh, "stats.usedInThisWindow")}</small>
          </div>
        </div>
      </section>

      <article className="card">
        <div className="card-header">
          <div>
            <h2 className="card-title">{localizedMessage(isZh, "common.requestTrend")}</h2>
            <div className="card-subtitle">{localizedMessage(isZh, "stats.requestsAndErrorsInTheSelectedWindow")}</div>
          </div>
        </div>
        {chartData.length < 2 ? (
          <div className="chart-wrap"><div className="empty">{localizedMessage(isZh, "stats.noTrendData")}</div></div>
        ) : (
          <NyroChart
            data={chartData}
            ariaLabel={localizedMessage(isZh, "common.requestTrend")}
            valueLabel={localizedMessage(isZh, "common.requests")}
            errorLabel={localizedMessage(isZh, "common.errors")}
          />
        )}
        <div className="chart-legend">
          <span className="chart-legend-item"><span className="legend-dot" /><span>{localizedMessage(isZh, "common.requests2")}</span></span>
          <span className="chart-legend-item"><span className="legend-dot error" /><span>{localizedMessage(isZh, "common.errors2")}</span></span>
        </div>
      </article>

      <section className="section-grid split-grid">
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "stats.modelDistribution")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "stats.rankedByRequestCount")}</div>
            </div>
          </div>
          {modelRanks.length === 0 ? (
            <div className="empty">{localizedMessage(isZh, "stats.noModelRequests")}</div>
          ) : (
            <div className="priority-list">
              {modelRanks.map((item, index) => (
                <div className="priority-item" key={item.id}>
                  <span className="priority-order">{index + 1}</span>
                  <span className="priority-label" {...copyableTipProps(item.label)}>{item.label}</span>
                  <span className="priority-share">{compact(item.value)} · {item.share.toFixed(0)}%</span>
                </div>
              ))}
            </div>
          )}
        </article>
        <article className="card">
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "stats.tokenUsage")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "stats.rankedByTotalTokens")}</div>
            </div>
          </div>
          {tokenRanks.length === 0 ? (
            <div className="empty">{localizedMessage(isZh, "stats.noTokenData")}</div>
          ) : (
            <div className="priority-list">
              {tokenRanks.map((item, index) => (
                <div className="priority-item" key={item.id}>
                  <span className="priority-order">{index + 1}</span>
                  <span className="priority-label" {...copyableTipProps(item.label)}>{item.label}</span>
                  <span className="priority-share">{compact(item.value)} · {item.share.toFixed(0)}%</span>
                </div>
              ))}
            </div>
          )}
        </article>
      </section>

      <section className="section-grid split-grid">
        <article className={clsx("card table-card stats-table", providerStats.length === 0 && "is-empty")}>
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "stats.providerRanking")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "stats.volumeErrorsAndLatency")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.provider")}</th>
                  <th>{localizedMessage(isZh, "common.requests2")}</th>
                  <th>{localizedMessage(isZh, "common.errors2")}</th>
                  <th>{localizedMessage(isZh, "common.errorRate")}</th>
                  <th>{localizedMessage(isZh, "common.avgLatency")}</th>
                </tr>
              </thead>
              <tbody>
                {providerStats.slice(0, 8).map((item) => (
                  <tr key={item.provider}>
                    <td><strong className="name-ellipsis" {...copyableTipProps(item.provider)}>{item.provider}</strong></td>
                    <td>{compact(item.request_count)}</td>
                    <td>{item.error_count}</td>
                    <td>{item.request_count > 0 ? ((item.error_count / item.request_count) * 100).toFixed(2) : "0.00"}%</td>
                    <td>{latency(item.avg_duration_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="table-empty">{localizedMessage(isZh, "stats.noProviderData")}</div>
        </article>

        <article className={clsx("card table-card stats-table", apiKeyStats.length === 0 && "is-empty")}>
          <div className="card-header">
            <div>
              <h2 className="card-title">{localizedMessage(isZh, "stats.keyRanking")}</h2>
              <div className="card-subtitle">{localizedMessage(isZh, "stats.resourceUseByKey")}</div>
            </div>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "stats.key")}</th>
                  <th>{localizedMessage(isZh, "common.requests2")}</th>
                  <th>{localizedMessage(isZh, "stats.inputTokens")}</th>
                  <th>{localizedMessage(isZh, "stats.outputTokens")}</th>
                  <th>{localizedMessage(isZh, "stats.cacheReads")}</th>
                  <th>{localizedMessage(isZh, "stats.lastUsed")}</th>
                </tr>
              </thead>
              <tbody>
                {apiKeyStats.slice(0, 8).map((item) => (
                  <tr key={item.api_key_id}>
                    <td><code className="code-pill name-ellipsis" {...copyableTipProps(item.api_key_name || item.api_key_id)}>{item.api_key_name || item.api_key_id}</code></td>
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
          <div className="table-empty">{localizedMessage(isZh, "stats.noKeyData")}</div>
        </article>
      </section>
    </PageLayout>
  );
}
