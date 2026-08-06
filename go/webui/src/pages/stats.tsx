import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Bar, BarChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis, PieChart, Pie, Cell } from "recharts";
import { backend } from "@/lib/backend";
import type { StatsOverview, StatsHourly, RouteStats, UpstreamStats, ConsumerStats } from "@/lib/types";
import { Zap, Clock, Activity } from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { formatLogTime } from "@/lib/format";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";

const COLORS = ["#3b82f6", "#10b981", "#f59e0b", "#ef4444", "#8b5cf6", "#ec4899", "#06b6d4", "#84cc16"];

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

	const { data: routeStats = [] } = useQuery<RouteStats[]>({
		queryKey: ["stats-routes", hours],
		queryFn: () => backend("get_stats_by_route", { hours }),
    refetchInterval: 30_000,
  });

	const { data: upstreamStats = [] } = useQuery<UpstreamStats[]>({
		queryKey: ["stats-upstreams", hours],
		queryFn: () => backend("get_stats_by_upstream", { hours }),
    refetchInterval: 30_000,
  });

	const { data: consumerStats = [] } = useQuery<ConsumerStats[]>({
		queryKey: ["stats-consumers", hours],
		queryFn: () => backend("get_stats_by_consumer", { hours }),
    refetchInterval: 30_000,
  });

  const tokenChart = hourly.map((h) => ({
    hour: h.hour.slice(11, 16),
    input: h.total_input_tokens,
    output: h.total_output_tokens,
  }));

	const modelPie = routeStats.slice(0, 6).map((route) => ({
		name: route.route_model || route.route_id,
		value: route.request_count,
  }));
  const modelTotal = modelPie.reduce((acc, m) => acc + m.value, 0);

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">{isZh ? "统计" : "Statistics"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh ? "Token 使用、延迟与错误分析" : "Token usage, latency, and error analytics"}
          </p>
        </div>
        <Select value={String(hours)} onValueChange={(value) => setHours(Number(value))}>
          <SelectTrigger className="w-40">
            <SelectValue placeholder={isZh ? "选择时间范围" : "Select range"} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="6">{isZh ? "最近 6 小时" : "Last 6h"}</SelectItem>
            <SelectItem value="24">{isZh ? "最近 24 小时" : "Last 24h"}</SelectItem>
            <SelectItem value="72">{isZh ? "最近 3 天" : "Last 3d"}</SelectItem>
            <SelectItem value="168">{isZh ? "最近 7 天" : "Last 7d"}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        {[
          { label: isZh ? "总请求数" : "Total Requests", value: fmt(overview?.total_requests ?? 0), icon: Activity, color: "text-blue-600" },
          { label: isZh ? "输入 Token" : "Input Tokens", value: fmt(overview?.total_input_tokens ?? 0), icon: Zap, color: "text-amber-600" },
          { label: isZh ? "输出 Token" : "Output Tokens", value: fmt(overview?.total_output_tokens ?? 0), icon: Zap, color: "text-green-600" },
          { label: isZh ? "平均延迟" : "Avg Latency", value: `${(overview?.avg_duration_ms ?? 0).toFixed(0)}ms`, icon: Clock, color: "text-purple-600" },
        ].map((c) => (
          <div key={c.label} className="glass rounded-2xl p-4">
            <div className="flex items-center gap-2">
              <c.icon className={`h-4 w-4 ${c.color}`} />
              <p className="text-xs font-medium text-slate-500">{c.label}</p>
            </div>
            <p className="mt-1.5 text-[24px] leading-none font-semibold text-slate-900">
              {c.label === (isZh ? "平均延迟" : "Avg Latency") ? fmtLatency(overview?.avg_duration_ms ?? 0) : c.value}
            </p>
          </div>
        ))}
      </div>

      <div className="grid grid-cols-1 gap-3 xl:grid-cols-2">
        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "Token 时序" : "Token Usage Over Time"}</h3>
          <div className="h-48">
            {tokenChart.length > 0 ? (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={tokenChart}>
                  <CartesianGrid strokeDasharray="3 3" vertical={false} stroke="#e2e8f0" />
                  <XAxis dataKey="hour" tick={{ fill: "#64748b", fontSize: 11 }} axisLine={false} tickLine={false} />
                  <YAxis tick={{ fill: "#64748b", fontSize: 11 }} axisLine={false} tickLine={false} width={50} tickFormatter={fmt} />
                  <Tooltip />
                  <Bar dataKey="input" name={isZh ? "输入" : "Input"} stackId="a" fill="#3b82f6" />
                  <Bar dataKey="output" name={isZh ? "输出" : "Output"} stackId="a" fill="#10b981" radius={[4, 4, 0, 0]} />
                </BarChart>
              </ResponsiveContainer>
            ) : (
              <div className="flex h-full items-center justify-center text-sm text-slate-400">{isZh ? "暂无数据" : "No data"}</div>
            )}
          </div>
        </div>

        <div className="glass rounded-2xl p-6">
          <h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "模型请求分布" : "Requests by Model"}</h3>
          <div className="grid grid-cols-1 gap-3 lg:grid-cols-[240px_1fr] lg:items-center">
            <div className="h-44">
            {modelPie.length > 0 ? (
                <ResponsiveContainer width="100%" height="100%">
                  <PieChart>
                    <Pie
                      data={modelPie}
                      cx="50%"
                      cy="50%"
                      innerRadius={44}
                      outerRadius={68}
                      paddingAngle={3}
                      dataKey="value"
                      label={false}
                      labelLine={false}
                    >
                      {modelPie.map((_, i) => (
                        <Cell key={i} fill={COLORS[i % COLORS.length]} />
                      ))}
                    </Pie>
                    <Tooltip />
                  </PieChart>
                </ResponsiveContainer>
            ) : (
              <div className="flex h-full items-center justify-center text-sm text-slate-400">{isZh ? "暂无数据" : "No data"}</div>
            )}
            </div>
            {modelPie.length > 0 && (
              <div className="space-y-2">
                {modelPie.map((item, i) => {
                  const pct = modelTotal > 0 ? Math.round((item.value / modelTotal) * 100) : 0;
                  return (
                    <div key={item.name} className="flex items-center justify-between gap-3 text-sm">
                      <div className="min-w-0 flex items-center gap-2">
                        <span
                          className="h-2.5 w-2.5 shrink-0 rounded-full"
                          style={{ backgroundColor: COLORS[i % COLORS.length] }}
                        />
                        <span className="truncate text-slate-600">{item.name}</span>
                      </div>
                      <span className="shrink-0 font-medium text-slate-900">{pct}%</span>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </div>
      </div>

      <div className="glass rounded-2xl p-6">
		<h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "上游分布" : "Upstream Breakdown"}</h3>
        <div className="overflow-hidden rounded-xl border border-white/70 bg-white/50">
          <table className="w-full text-sm">
            <thead className="bg-white/70 text-slate-500">
              <tr>
			<th className="px-4 py-2.5 text-left font-medium">{isZh ? "上游" : "Upstream"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "请求数" : "Requests"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "错误数" : "Errors"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "错误率" : "Error Rate"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "平均延迟" : "Avg Latency"}</th>
              </tr>
            </thead>
            <tbody>
			{upstreamStats.length === 0 && (
                <tr><td className="px-4 py-6 text-center text-slate-400" colSpan={5}>{isZh ? "暂无数据" : "No data"}</td></tr>
              )}
			{upstreamStats.slice(0, 8).map((upstream) => (
				<tr key={upstream.upstream_id} className="border-t border-white/70 text-slate-700">
					<td className="px-4 py-2.5 font-medium">{upstream.upstream_name || upstream.upstream_id}</td>
					<td className="px-4 py-2.5 text-right">{fmt(upstream.request_count)}</td>
					<td className="px-4 py-2.5 text-right text-red-500">{upstream.error_count}</td>
                  <td className="px-4 py-2.5 text-right">
						{upstream.request_count > 0 ? ((upstream.error_count / upstream.request_count) * 100).toFixed(1) : "0"}%
                  </td>
					<td className="px-4 py-2.5 text-right">{fmtLatency(upstream.avg_duration_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>

      <div className="glass rounded-2xl p-6">
		<h3 className="mb-4 text-sm font-semibold text-slate-800">{isZh ? "消费者调用统计" : "Consumer Usage"}</h3>
        <div className="overflow-hidden rounded-xl border border-white/70 bg-white/50">
          <table className="w-full text-sm">
            <thead className="bg-white/70 text-slate-500">
              <tr>
			<th className="px-4 py-2.5 text-left font-medium">{isZh ? "消费者" : "Consumer"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "请求数" : "Requests"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "输入 Token" : "Input Tokens"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "输出 Token" : "Output Tokens"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "缓存命中" : "Cache Hits"}</th>
                <th className="px-4 py-2.5 text-right font-medium">{isZh ? "最后调用" : "Last Used"}</th>
              </tr>
            </thead>
            <tbody>
			{consumerStats.length === 0 && (
                <tr><td className="px-4 py-6 text-center text-slate-400" colSpan={6}>{isZh ? "暂无数据" : "No data"}</td></tr>
              )}
			{consumerStats.slice(0, 8).map((consumer) => {
				const cacheTotal = consumer.total_input_tokens + consumer.cache_read_tokens;
				const cacheRate = cacheTotal > 0 ? Math.round((consumer.cache_read_tokens / cacheTotal) * 100) : 0;
                return (
					<tr key={consumer.consumer_id} className="border-t border-white/70 text-slate-700">
						<td className="px-4 py-2.5 font-medium">{consumer.consumer_id}</td>
						<td className="px-4 py-2.5 text-right">{fmt(consumer.request_count)}</td>
						<td className="px-4 py-2.5 text-right">{fmt(consumer.total_input_tokens)}</td>
						<td className="px-4 py-2.5 text-right">{fmt(consumer.total_output_tokens)}</td>
                    <td className="px-4 py-2.5 text-right">
							<span>{fmt(consumer.cache_read_tokens)}</span>
                      {cacheRate > 0 && <span className="ml-1 text-[11px] text-slate-400">{cacheRate}%</span>}
                    </td>
						<td className="px-4 py-2.5 text-right text-xs text-slate-500 whitespace-nowrap">{formatLogTime(consumer.last_used_at)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
