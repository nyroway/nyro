import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { ChevronLeft, ChevronRight, Trash2 } from "lucide-react";

import { backend } from "@/lib/backend";
import type { Consumer, LogPage, LogQuery, RouteStats, Upstream, RequestLog } from "@/lib/types";
import { getRouteType } from "@/lib/types";
import { computeTps, formatDuration, formatKeyPreview, formatLogTime, formatTokenCount, formatTps } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { useLocale } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { LogDetailDialog } from "@/components/log-detail-dialog";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Status } from "@/components/v2/status";
import { Surface } from "@/components/v2/surface";
import { applyLogStatusFilter, logStatusFilterValue, type LogStatusFilter } from "@/features/logs/log-filter";
import { localizedMessage } from "@/lib/messages";

const PAGE_SIZE = 11;
const ALL_OPTION = "__all__";

export default function LogsPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
  const location = useLocation();
  const navigate = useNavigate();

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
  const { data: upstreams = [] } = useQuery<Upstream[]>({
    queryKey: ["upstreams"],
    queryFn: () => backend("list_upstreams"),
  });
  const { data: routeStats = [] } = useQuery<RouteStats[]>({
    queryKey: ["stats", "routes", "log-filter"],
    queryFn: () => backend("get_stats_by_route"),
  });
  const { data: consumers = [] } = useQuery<Consumer[]>({
    queryKey: ["consumers", "log-filter"],
    queryFn: () => backend("list_consumers"),
  });

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const focusedLogID = new URLSearchParams(location.search).get("focus");

  const upstreamOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "v2.logs.allUpstreams") },
      ...upstreams.map((upstream) => ({ value: upstream.id, label: upstream.name })),
    ],
    [upstreams, isZh],
  );
  const modelOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "v2.logs.allModels") },
      ...routeStats
        .filter((route) => (route.route_model ?? "").trim())
        .map((route) => ({ value: route.route_id, label: route.route_model })),
    ],
    [routeStats, isZh],
  );
  const consumerOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "v2.logs.allConsumers") },
      ...consumers.map((consumer) => ({ value: consumer.id, label: consumer.name })),
    ],
    [consumers, isZh],
  );

  const upstreamFilterValue = filter.upstream_id ?? ALL_OPTION;
  const consumerFilterValue = filter.consumer_id ?? ALL_OPTION;
  const routeFilterValue = filter.route_id ?? ALL_OPTION;
  const statusFilterValue = logStatusFilterValue(filter);

  const columns: DataTableColumn<RequestLog>[] = [
    { key: "time", header: localizedMessage(isZh, "v2.logs.time"), className: "v2-log-time", render: (log) => <code>{formatLogTime(log.created_at)}</code> },
    { key: "status", header: localizedMessage(isZh, "v2.providers.status"), render: (log) => {
      const status = log.response_status_code ?? 0;
      return <Status tone={status < 400 ? "success" : status < 500 ? "warning" : "danger"}>{log.response_status_code ?? "—"}</Status>;
    } },
    { key: "consumer", header: localizedMessage(isZh, "v2.logs.consumerAndKey"), render: (log) => <div className="v2-log-stack"><strong>{log.consumer_id ?? (localizedMessage(isZh, "v2.logs.anonymous"))}</strong><code>{log.consumer_key_name ? formatKeyPreview(log.consumer_key_name) : "—"}</code></div> },
    { key: "model", header: localizedMessage(isZh, "v2.logs.modelAndUpstream"), render: (log) => <div className="v2-log-stack"><strong className="v2-mono">{log.client_model ?? "—"}</strong><span>{log.upstream_name ?? log.upstream_id ?? "—"}{log.upstream_model ? ` : ${log.upstream_model}` : ""}</span></div> },
    { key: "protocol", header: localizedMessage(isZh, "v2.logs.protocolFlow"), render: (log) => <ProtocolLane ingress={log.client_protocol} egress={log.upstream_protocol} /> },
    { key: "latency", header: localizedMessage(isZh, "v2.logs.latency"), render: (log) => <code className="v2-log-value">{formatDuration(log.latency_total_ms)}</code> },
    { key: "tokens", header: "Token", render: (log) => <div className="v2-log-tokens"><span>IN {formatTokenCount(log.input_tokens)}</span><span>OUT {formatTokenCount(log.output_tokens)}</span></div> },
    { key: "tps", header: "TPS", render: (log) => <code className="v2-log-value">{formatTps(computeTps(log))}</code> },
    { key: "type", header: localizedMessage(isZh, "v2.logs.type"), render: (log) => <span className="v2-code-pill">{(log.is_stream ?? (log.stream_chunks_count ?? 0) > 0) ? "SSE" : getRouteType(log) === "embedding" ? "EMB" : "JSON"}</span> },
  ];

  return (
    <PageLayout header={<PageHeader title={t("page.logs.title")} description={`${t("page.logs.subtitle")} · ${t("page.logs.records", { count: total })}`} />}>
      <FilterBar summary={localizedMessage(isZh, "common.recordsCount", { count: total })}>
        <Select value={consumerFilterValue} onValueChange={(value) => { setFilter((current) => ({ ...current, consumer_id: value === ALL_OPTION ? undefined : value })); setPage(0); }}><SelectTrigger aria-label={localizedMessage(isZh, "v2.logs.consumerFilter")}><SelectValue /></SelectTrigger><SelectContent>{consumerOptions.map((option) => <SelectItem key={`consumer-v2-${option.value || "all"}`} value={option.value || ALL_OPTION}>{option.label}</SelectItem>)}</SelectContent></Select>
        <Select value={routeFilterValue} onValueChange={(value) => { setFilter((current) => ({ ...current, route_id: value === ALL_OPTION ? undefined : value })); setPage(0); }}><SelectTrigger aria-label={localizedMessage(isZh, "v2.logs.modelFilter")}><SelectValue /></SelectTrigger><SelectContent>{modelOptions.map((option) => <SelectItem key={`model-v2-${option.value || "all"}`} value={option.value || ALL_OPTION}>{option.label}</SelectItem>)}</SelectContent></Select>
        <Select value={upstreamFilterValue} onValueChange={(value) => { setFilter((current) => ({ ...current, upstream_id: value === ALL_OPTION ? undefined : value })); setPage(0); }}><SelectTrigger aria-label={localizedMessage(isZh, "v2.logs.upstreamFilter")}><SelectValue /></SelectTrigger><SelectContent>{upstreamOptions.map((option) => <SelectItem key={`upstream-v2-${option.value || "all"}`} value={option.value || ALL_OPTION}>{option.label}</SelectItem>)}</SelectContent></Select>
        <Select value={statusFilterValue} onValueChange={(status: LogStatusFilter) => { setFilter((current) => applyLogStatusFilter(current, status)); setPage(0); }}><SelectTrigger aria-label={localizedMessage(isZh, "v2.logs.statusFilter")}><SelectValue /></SelectTrigger><SelectContent><SelectItem value="all">{localizedMessage(isZh, "v2.providers.allStatuses")}</SelectItem><SelectItem value="ok">{localizedMessage(isZh, "v2.logs.2xxOnly2")}</SelectItem><SelectItem value="error">{localizedMessage(isZh, "v2.logs.4xxErrors2")}</SelectItem></SelectContent></Select>
        <Button variant="outline" size="icon" title={localizedMessage(isZh, "v2.logs.clearLogs")} disabled={total === 0} onClick={() => setConfirmOpen(true)}><Trash2 /></Button>
      </FilterBar>
      <Surface className="v2-table-surface v2-log-table" title={localizedMessage(isZh, "v2.logs.requestLogs")} description={localizedMessage(isZh, "v2.logs.inspectConsumerModelProtocolFlowAndUpstreamResult")}>
        <DataTable columns={columns} rows={items} rowKey={(log) => log.id} loading={isLoading} onRowClick={setSelected} empty={<EmptyState title={total ? (localizedMessage(isZh, "v2.logs.noMatchingRequests")) : (localizedMessage(isZh, "v2.logs.noRequestLogsYet"))} description={total ? (localizedMessage(isZh, "v2.logs.adjustTheActiveFilters")) : (localizedMessage(isZh, "v2.logs.logsAppearHereAfterTheGatewayReceivesRequests"))} />} />
        {totalPages > 1 && <div className="v2-pagination"><span>{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span><div><Button variant="outline" size="icon" disabled={page === 0} onClick={() => setPage((current) => current - 1)}><ChevronLeft /></Button><Button variant="outline" size="icon" disabled={page >= totalPages - 1} onClick={() => setPage((current) => current + 1)}><ChevronRight /></Button></div></div>}
      </Surface>

      <LogDetailDialog
        logId={selected?.id ?? focusedLogID}
        summary={selected}
        open={!!selected || !!focusedLogID}
        onOpenChange={(open) => {
          if (!open) {
            setSelected(null);
            if (focusedLogID) navigate(location.pathname, { replace: true });
          }
        }}
      />

      <ConfirmDialog
        open={confirmOpen}
        onOpenChange={setConfirmOpen}
        title={localizedMessage(isZh, "v2.logs.clearAllLogs")}
        description={
          localizedMessage(isZh, "v2.logs.allRequestLogsWillBePermanentlyDeletedThis")
        }
        confirmText={localizedMessage(isZh, "v2.api-keys.clear")}
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        onConfirm={() => clearMut.mutate()}
      />
    </PageLayout>
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
