import { useQuery } from "@tanstack/react-query";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis, Line, LineChart } from "recharts";
import { backend } from "@/lib/backend";
import type { StatsOverview, StatsHourly, ModelStats, ProviderStats, GatewayStatus, Provider, Model as ModelType } from "@/lib/types";
import { Activity, Zap, Clock, AlertTriangle, Server, Route as RouteIcon } from "lucide-react";
import { useLocale } from "@/lib/i18n";

function fmt(n: number) {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
  if (n >= 1_000) return (n / 1_000).toFixed(1) + "K";
  return String(n);
}

function fmtLatency(ms: number) {
  if (ms >= 1000) {
    return `${(ms / 1000).toFixed(ms >= 10_000 ? 1 : 2)}s`;
  }
  return `${ms.toFixed(0)}ms`;
}

export default function DashboardPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

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

  const errorRate = overview && overview.total_requests > 0
    ? ((overview.error_count / overview.total_requests) * 100).toFixed(1)
    : "0";

  const latencyUseSeconds = hourly.some((h) => h.avg_duration_ms >= 1000);

  const cards = [
    { label: isZh ? "总请求数" : "Total Requests", value: fmt(overview?.total_requests ?? 0), icon: Activity, color: "text-blue-600" },
    { label: isZh ? "总 Token" : "Total Tokens", value: fmt((overview?.total_input_tokens ?? 0) + (overview?.total_output_tokens ?? 0)), icon: Zap, color: "text-amber-600" },
    { label: isZh ? "平均延迟" : "Avg Latency", value: fmtLatency(overview?.avg_duration_ms ?? 0), icon: Clock, color: "text-green-600" },
    { label: isZh ? "错误率" : "Error Rate", value: `${errorRate}%`, icon: AlertTriangle, color: "text-red-500" },
    { label: isZh ? "提供商" : "Providers", value: String(providers.length), icon: Server, color: "text-purple-600" },
    { label: isZh ? "模型" : "Models", value: String(routes.length), icon: RouteIcon, color: "text-indigo-600" },
  ];

  const chartHourly = hourly.map((h) => ({
    hour: h.hour.slice(11, 16),
    requests: h.request_count,
    errors: h.error_count,
    latency: latencyUseSeconds
      ? Number((h.avg_duration_ms / 1000).toFixed(2))
      : Math.round(h.avg_duration_ms),
  }));

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "概览" : "Dashboard"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh
            ? `代理状态 ${status?.status === "running" ? "运行中" : "–"}，端口 ${status?.proxy_port ?? "–"}`
            : `Proxy ${status?.status === "running" ? "running" : "–"} on port ${status?.proxy_port ?? "–"}`}
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-3 xl:grid-cols-6">
        {cards.map((c) => (
          <div key={c.label} className="glass rounded-2xl p-4 transition-all hover:-translate-y-0.5 hover:shadow-lg">
            <div className="flex items-center gap-2">
              <c.icon className={`h-4 w-4 ${c.color}`} />
              <p className="text-xs font-medium text-slate-500">{c.label}</p>
            </div>
            <p className="mt-1.5 text-[24px] leading-none font-semibold text-slate-900">{c.value}</p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "请求量（24h）" : "Requests (24h)"}</h3>
          <div className="h-48">
            {chartHourly.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={chartHourly}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#e2e8f0" />
                  <XAxis dataKey="hour" tick={{ fill: "#64748b", fontSize: 11 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#64748b", fontSize: 11 }} axisLine={false} tickLine={false} width={40} />
                  <Tooltip />
                  <Bar dataKey="requests" name={isZh ? "请求" : "Requests"} radius={[4, 4, 0, 0]} fill="#3b82f6" />
                  <Bar dataKey="errors" name={isZh ? "错误" : "Errors"} radius={[4, 4, 0, 0]} fill="#ef4444" />
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <div className="flex h-full items-center justify-center text-sm text-slate-400">
                {isZh ? "暂无流量数据" : "No traffic data yet"}
              </div>
            )}
          </div>
        </div>

        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "延迟（24h）" : "Latency (24h)"}</h3>
          <div className="h-48">
            {chartHourly.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <LineChart data={chartHourly}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#e2e8f0" />
                  <XAxis dataKey="hour" tick={{ fill: "#64748b", fontSize: 11 }} axisLine={false} tickLine={false} />
                  <YAxis
                    tick={{ fill: "#64748b", fontSize: 11 }}
                    axisLine={false}
                    tickLine={false}
                    width={40}
                    unit={latencyUseSeconds ? "s" : "ms"}
                  />
                  <Tooltip
                    formatter={(value) =>
                      typeof value === "number"
                        ? `${value}${latencyUseSeconds ? "s" : "ms"}`
                        : value
                    }
                  />
                  <Line type="monotone" dataKey="latency" name={isZh ? "平均延迟" : "Avg Latency"} stroke="#10b981" strokeWidth={2} dot={false} />
                </LineChart>
              </ResponsiveContainer>
            ) : (
              <div className="flex h-full items-center justify-center text-sm text-slate-400">
                {isZh ? "暂无延迟数据" : "No latency data yet"}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "热门模型" : "Top Models"}</h3>
          <div className="overflow-hidden rounded-xl border border-white/70 bg-white/50">
            <table className="w-full text-sm">
              <thead className="bg-white/70 text-slate-500">
                <tr>
                  <th className="px-3 py-2 text-left font-medium">{isZh ? "模型" : "Model"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "请求数" : "Requests"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "Token" : "Tokens"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "延迟" : "Latency"}</th>
                </tr>
              </thead>
              <tbody>
                {modelStats.length === 0 && (
                  <tr><td className="px-3 py-6 text-center text-slate-400" colSpan={4}>{isZh ? "暂无模型数据" : "No model data"}</td></tr>
                )}
                {modelStats.slice(0, 6).map((m) => (
                  <tr key={m.model} className="border-t border-white/70 text-slate-700">
                    <td className="px-3 py-2 font-medium">{m.model}</td>
                    <td className="px-3 py-2 text-right">{fmt(m.request_count)}</td>
                    <td className="px-3 py-2 text-right">{fmt(m.total_input_tokens + m.total_output_tokens)}</td>
                    <td className="px-3 py-2 text-right">{fmtLatency(m.avg_duration_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>

        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "提供商概览" : "Provider Overview"}</h3>
          <div className="overflow-hidden rounded-xl border border-white/70 bg-white/50">
            <table className="w-full text-sm">
              <thead className="bg-white/70 text-slate-500">
                <tr>
                  <th className="px-3 py-2 text-left font-medium">{isZh ? "提供商" : "Provider"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "请求数" : "Requests"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "错误数" : "Errors"}</th>
                  <th className="px-3 py-2 text-right font-medium">{isZh ? "延迟" : "Latency"}</th>
                </tr>
              </thead>
              <tbody>
                {providerStats.length === 0 && (
                  <tr><td className="px-3 py-6 text-center text-slate-400" colSpan={4}>{isZh ? "暂无提供商数据" : "No provider data"}</td></tr>
                )}
                {providerStats.slice(0, 6).map((p) => (
                  <tr key={p.provider} className="border-t border-white/70 text-slate-700">
                    <td className="px-3 py-2 font-medium">{p.provider}</td>
                    <td className="px-3 py-2 text-right">{fmt(p.request_count)}</td>
                    <td className="px-3 py-2 text-right text-red-500">{p.error_count}</td>
                    <td className="px-3 py-2 text-right">{fmtLatency(p.avg_duration_ms)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  );
}
