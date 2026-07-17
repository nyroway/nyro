import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ChevronLeft, ChevronRight, ScrollText, Trash2 } from "lucide-react";

import { backend } from "@/lib/backend";
import type { Consumer, LogPage, LogQuery, ModelStats, Upstream, RequestLog } from "@/lib/types";
import { getRouteType } from "@/lib/types";
import { computeTps, formatDuration, formatLogTime, formatTokenCount, formatTps } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { cn } from "@/lib/utils";
import { useLocale } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LogDetailDialog } from "@/components/log-detail-dialog";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";

const PAGE_SIZE = 11;
const ALL_OPTION = "__all__";

export default function LogsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();

  const [page, setPage] = useState(0);
  const [filter, setFilter] = useState<LogQuery>({ limit: PAGE_SIZE, offset: 0 });
  const [selected, setSelected] = useState<RequestLog | null>(null);
  const [confirmOpen, setConfirmOpen] = useState(false);

  const clearMut = useMutation({
    mutationFn: () => backend("clear_logs"),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["logs"] });
      setPage(0);
      setConfirmOpen(false);
    },
  });

  const query: LogQuery = { ...filter, limit: PAGE_SIZE, offset: page * PAGE_SIZE };

  const { data, isLoading } = useQuery<LogPage>({
    queryKey: ["logs", query],
    queryFn: () => backend("query_logs", { query }),
    refetchInterval: 5_000,
  });
  const { data: providers = [] } = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => backend("list_upstreams"),
  });
  const { data: modelStats = [] } = useQuery<ModelStats[]>({
    queryKey: ["stats", "models", "log-filter"],
    queryFn: () => backend("get_stats_by_model"),
  });
  const { data: apiKeys = [] } = useQuery<Consumer[]>({
    queryKey: ["api-keys", "log-filter"],
    queryFn: () => backend("list_consumers"),
  });

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const providerOptions = useMemo(
    () => [
      { value: "", label: isZh ? "全部提供商" : "All Providers" },
      ...providers.map((p) => ({ value: p.id, label: p.name })),
    ],
    [providers, isZh],
  );
  const modelOptions = useMemo(
    () => [
      { value: "", label: isZh ? "全部模型" : "All Models" },
      ...modelStats
        .filter((m) => (m.model ?? "").trim())
        .map((m) => ({ value: m.model, label: m.model })),
    ],
    [modelStats, isZh],
  );
  const statusOptions = useMemo(
    () => [
      { value: "", label: isZh ? "全部状态" : "All Status" },
      { value: "ok", label: isZh ? "仅 2xx" : "2xx Only" },
      { value: "error", label: isZh ? "4xx+ 错误" : "4xx+ Errors" },
    ],
    [isZh],
  );
  const apiKeyOptions = useMemo(
    () => [
      { value: "", label: isZh ? "全部密钥" : "All API Keys" },
      ...apiKeys.map((k) => ({ value: k.id, label: k.name })),
    ],
    [apiKeys, isZh],
  );

  const providerFilterValue = filter.provider ?? ALL_OPTION;
  const apiKeyFilterValue = filter.api_key ?? ALL_OPTION;
  const modelFilterValue = filter.model ?? ALL_OPTION;
  const statusFilterValue =
    (filter.status_min ?? null) === 200 && (filter.status_max ?? null) === 299
      ? "ok"
      : (filter.status_min ?? null) === 400 && (filter.status_max ?? null) == null
        ? "error"
        : ALL_OPTION;

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">{isZh ? "请求日志" : "Request Logs"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh ? `共 ${total} 条记录` : `${total} total records`}
          </p>
        </div>
        <div className="flex gap-2">
          <Select
            value={apiKeyFilterValue}
            onValueChange={(value) => {
              setFilter({ ...filter, api_key: value === ALL_OPTION ? undefined : value });
              setPage(0);
            }}
          >
            <SelectTrigger className="w-48">
              <SelectValue placeholder={isZh ? "密钥过滤" : "API Key Filter"} />
            </SelectTrigger>
            <SelectContent>
              {apiKeyOptions.map((option) => (
                <SelectItem key={`api-key-${option.value || "all"}`} value={option.value || ALL_OPTION}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={providerFilterValue}
            onValueChange={(value) => {
              setFilter({ ...filter, provider: value === ALL_OPTION ? undefined : value });
              setPage(0);
            }}
          >
            <SelectTrigger className="w-48">
              <SelectValue placeholder={isZh ? "提供商过滤" : "Provider Filter"} />
            </SelectTrigger>
            <SelectContent>
              {providerOptions.map((option) => (
                <SelectItem key={`provider-${option.value || "all"}`} value={option.value || ALL_OPTION}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={modelFilterValue}
            onValueChange={(value) => {
              setFilter({ ...filter, model: value === ALL_OPTION ? undefined : value });
              setPage(0);
            }}
          >
            <SelectTrigger className="w-48">
              <SelectValue placeholder={isZh ? "模型过滤" : "Model Filter"} />
            </SelectTrigger>
            <SelectContent>
              {modelOptions.map((option) => (
                <SelectItem key={`model-${option.value || "all"}`} value={option.value || ALL_OPTION}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select
            value={statusFilterValue}
            onValueChange={(next) => {
              if (next === "error") {
                setFilter({ ...filter, status_min: 400, status_max: undefined });
              } else if (next === "ok") {
                setFilter({ ...filter, status_min: 200, status_max: 299 });
              } else {
                setFilter({ ...filter, status_min: undefined, status_max: undefined });
              }
              setPage(0);
            }}
          >
            <SelectTrigger className="w-44">
              <SelectValue placeholder={isZh ? "状态过滤" : "Status Filter"} />
            </SelectTrigger>
            <SelectContent>
              {statusOptions.map((option) => (
                <SelectItem key={`status-${option.value || "all"}`} value={option.value || ALL_OPTION}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button
            variant="outline"
            size="icon"
            className="h-10 w-10"
            title={isZh ? "清空日志" : "Clear Logs"}
            disabled={total === 0}
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>

      {isLoading ? (
        <div className="text-center text-sm text-slate-500 py-12">{isZh ? "加载中..." : "Loading..."}</div>
      ) : items.length === 0 ? (
        <div className="glass rounded-2xl p-12 text-center">
          <ScrollText className="mx-auto h-10 w-10 text-slate-400" />
          <p className="mt-3 text-sm text-slate-500">{isZh ? "暂无日志" : "No logs yet"}</p>
        </div>
      ) : (
        <div className="glass overflow-hidden rounded-2xl">
          <div className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b border-slate-200/80 bg-slate-50/50 text-slate-500">
                <tr>
                  <th className="px-3 py-2.5 text-left font-medium whitespace-nowrap">
                    {isZh ? "时间" : "Time"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">
                    {isZh ? "状态" : "Status"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium whitespace-nowrap">
                    {isZh ? "密钥" : "API Key"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">
                    {isZh ? "模型" : "Model"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">
                    {isZh ? "协议" : "Protocol"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">
                    {isZh ? "耗时" : "Latency"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">Token</th>
                  <th className="px-3 py-2.5 text-left font-medium whitespace-nowrap">
                    {isZh ? "速度" : "TPS"}
                  </th>
                  <th className="px-3 py-2.5 text-left font-medium">
                    {isZh ? "类型" : "Type"}
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((log) => {
                  const routeType = getRouteType(log);
                  const statusOk = (log.client_status_code ?? 0) < 400;
                  const isStream = log.is_stream ?? (log.stream_chunks_count ?? 0) > 0;
                  return (
                    <tr
                      key={log.id}
                      onClick={() => setSelected(log)}
                      className="cursor-pointer border-t border-slate-100 text-slate-700 transition-colors hover:bg-slate-50"
                    >
                      <td className="px-3 py-2 text-xs text-slate-500 whitespace-nowrap">
                        {formatLogTime(log.created_at)}
                      </td>
                      <td className="px-3 py-2">
                        <span
                          className={cn(
                            "inline-block rounded-full px-2 py-0.5 text-xs font-medium",
                            statusOk ? "bg-green-50 text-green-700" : "bg-red-50 text-red-600",
                          )}
                        >
                          {log.client_status_code ?? "–"}
                        </span>
                      </td>
                      <td className="px-3 py-2 whitespace-nowrap">
                        <div className="flex flex-col leading-tight">
                          <span className="text-xs font-medium text-slate-700">
                            {log.api_key_name ?? "–"}
                          </span>
                          {log.api_key_preview && log.api_key_preview !== log.api_key_name ? (
                            <span className="text-[11px] text-slate-400">
                              {log.api_key_preview}
                            </span>
                          ) : null}
                        </div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex flex-col leading-tight">
                          <span className="text-xs font-medium text-slate-800">
                            {log.client_model ?? "–"}
                          </span>
                          <span className="text-[11px] text-slate-500">
                            {log.provider_name ?? log.provider_id ?? "–"}
                            {log.upstream_model ? `: ${log.upstream_model}` : ""}
                          </span>
                        </div>
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex items-center gap-1.5 text-xs text-slate-500">
                          <ProtocolLane
                            ingress={log.client_protocol}
                            egress={log.upstream_protocol}
                          />
                          {routeType === "embedding" ? (
                            <Badge variant="outline" className="text-[10px]">EMB</Badge>
                          ) : null}
                        </div>
                      </td>
                      <td className="px-3 py-2 text-xs text-slate-600 whitespace-nowrap">
                        {formatDuration(log.latency_total_ms)}
                      </td>
                      <td className="px-3 py-2">
                        <div className="flex flex-col items-start leading-tight text-[11px] tabular-nums">
                          <span className="inline-flex items-center gap-1 text-sky-600">
                            <span className="font-semibold tracking-wide">IN</span>
                            <span title={String(log.input_tokens)}>
                              {formatTokenCount(log.input_tokens)}
                            </span>
                          </span>
                          {routeType === "embedding" && log.output_tokens === 0 ? null : (
                            <span className="inline-flex items-center gap-1 text-emerald-600">
                              <span className="font-semibold tracking-wide">OUT</span>
                              <span title={String(log.output_tokens)}>
                                {formatTokenCount(log.output_tokens)}
                              </span>
                            </span>
                          )}
                        </div>
                      </td>
                      <td
                        className="px-3 py-2 text-xs text-slate-600 whitespace-nowrap tabular-nums"
                        title={isZh ? "净生成速度" : "Net generation speed"}
                      >
                        {formatTps(computeTps(log))}
                      </td>
                      <td className="px-3 py-2">
                        {isStream ? (
                          <Badge
                            variant="outline"
                            className="border-green-200 bg-green-50 text-[10px] text-green-700"
                          >
                            SSE
                          </Badge>
                        ) : (
                          <Badge
                            variant="outline"
                            className="border-sky-200 bg-sky-50 text-[10px] text-sky-700"
                          >
                            JSON
                          </Badge>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          <div className="flex items-center justify-between border-t border-slate-200/80 px-4 py-3">
            <span className="text-xs text-slate-500">
              {isZh ? `第 ${page + 1} / ${totalPages} 页` : `Page ${page + 1} of ${totalPages}`}
            </span>
            <div className="flex gap-1">
              <Button
                onClick={() => setPage(Math.max(0, page - 1))}
                disabled={page === 0}
                variant="outline"
                size="icon"
              >
                <ChevronLeft className="h-4 w-4" />
              </Button>
              <Button
                onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                disabled={page >= totalPages - 1}
                variant="outline"
                size="icon"
              >
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </div>
      )}

      <LogDetailDialog
        logId={selected?.id ?? null}
        summary={selected}
        open={!!selected}
        onOpenChange={(open) => {
          if (!open) setSelected(null);
        }}
      />

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={isZh ? "清空所有日志" : "Clear All Logs"}
        description={
          isZh
            ? "确认清空所有请求日志？此操作不可恢复。"
            : "All request logs will be permanently deleted. This action cannot be undone."
        }
        confirmText={isZh ? "清空" : "Clear"}
        cancelText={isZh ? "取消" : "Cancel"}
        onConfirm={() => clearMut.mutate()}
      />
    </div>
  );
}

function ProtocolCell({ value }: { value: string | null | undefined }) {
  const label = prettyName(value);
  if (!label) {
    return <span className="text-slate-400">–</span>;
  }
  return <span className="font-medium text-slate-700">{label}</span>;
}

function ProtocolLane({
  ingress,
  egress,
}: {
  ingress: string | null | undefined;
  egress: string | null | undefined;
}) {
  return (
    <span className="flex items-center gap-1.5">
      <ProtocolCell value={ingress} />
      <span className="text-slate-300">→</span>
      <ProtocolCell value={egress} />
    </span>
  );
}
