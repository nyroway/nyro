import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { ChevronLeft, ChevronRight, Filter, Trash2 } from "lucide-react";

import { consumersApi } from "@/lib/api/consumers";
import { logsApi } from "@/lib/api/logs";
import { statsApi } from "@/lib/api/stats";
import { upstreamsApi } from "@/lib/api/upstreams";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { Consumer, LogPage, LogQuery, RouteStats, Upstream, RequestLog } from "@/lib/types";
import { getRouteType } from "@/lib/types";
import { computeTps, formatDuration, formatKeyPreview, formatLogTime, formatTokenCount, formatTps } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { useLocale } from "@/lib/i18n";
import { showToast } from "@/lib/toast";
import { NyroSearchSelect } from "@/components/ui/nyro-fields";
import { LogDetailDrawer } from "@/components/log-detail-drawer";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Status } from "@/components/v2/status";
import { applyLogStatusFilter, logStatusFilterValue, type LogStatusFilter } from "@/features/logs/log-filter";
import { localizedMessage } from "@/lib/messages";

const PAGE_SIZE = 11;

interface FilterOption {
  id: string;
  label: string;
}

const optionId = (option: FilterOption | null) => option?.id || "";

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
    mutationFn: () => logsApi.clear(),
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ["logs"] });
      setPage(0);
      setConfirmOpen(false);
      showToast(localizedMessage(isZh, "common.cleared", { count: result.cleared }));
    },
    onError: (error) => {
      showToast(
        `${localizedMessage(isZh, "v2.logs.clearAllLogs")} — ${localizeBackendErrorMessage(error, isZh)}`,
        "error",
        6000,
      );
    },
  });

  const query: LogQuery = { ...filter, limit: PAGE_SIZE, offset: page * PAGE_SIZE };

  const { data, isLoading } = useQuery<LogPage>({
    queryKey: ["logs", query],
    queryFn: () => logsApi.query(query),
    refetchInterval: 5_000,
  });
  const { data: upstreams = [] } = useQuery<Upstream[]>({
    queryKey: ["upstreams"],
    queryFn: () => upstreamsApi.list(),
  });
  const { data: routeStats = [] } = useQuery<RouteStats[]>({
    queryKey: ["stats", "routes", "log-filter"],
    queryFn: () => statsApi.byRoute(),
  });
  const { data: consumers = [] } = useQuery<Consumer[]>({
    queryKey: ["consumers", "log-filter"],
    queryFn: () => consumersApi.list(),
  });

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const focusedLogID = new URLSearchParams(location.search).get("focus");

  const consumerOptions = useMemo<FilterOption[]>(
    () => [
      { id: "", label: localizedMessage(isZh, "v2.logs.allConsumers") },
      ...consumers.map((consumer) => ({ id: consumer.id, label: consumer.name })),
    ],
    [consumers, isZh],
  );
  const modelOptions = useMemo<FilterOption[]>(
    () => [
      { id: "", label: localizedMessage(isZh, "v2.logs.allModels") },
      ...routeStats
        .filter((route) => (route.route_model ?? "").trim())
        .map((route) => ({ id: route.route_id, label: route.route_model })),
    ],
    [routeStats, isZh],
  );
  const upstreamOptions = useMemo<FilterOption[]>(
    () => [
      { id: "", label: localizedMessage(isZh, "v2.logs.allUpstreams") },
      ...upstreams.map((upstream) => ({ id: upstream.id, label: upstream.name })),
    ],
    [upstreams, isZh],
  );
  const statusOptions = useMemo<FilterOption[]>(
    () => [
      { id: "all", label: localizedMessage(isZh, "v2.providers.allStatuses") },
      { id: "ok", label: localizedMessage(isZh, "v2.logs.2xxOnly2") },
      { id: "error", label: localizedMessage(isZh, "v2.logs.4xxErrors2") },
    ],
    [isZh],
  );

  const findOption = (options: FilterOption[], id: string) =>
    options.find((option) => option.id === id) ?? options[0];

  const consumerValue = findOption(consumerOptions, filter.consumer_id ?? "");
  const routeValue = findOption(modelOptions, filter.route_id ?? "");
  const upstreamValue = findOption(upstreamOptions, filter.upstream_id ?? "");
  const statusValue = findOption(statusOptions, logStatusFilterValue(filter));

  const columns: DataTableColumn<RequestLog>[] = [
    { key: "time", header: localizedMessage(isZh, "v2.logs.time"), render: (log) => <code>{formatLogTime(log.created_at)}</code> },
    { key: "status", header: localizedMessage(isZh, "v2.providers.status"), render: (log) => {
      const status = log.response_status_code ?? 0;
      return <Status tone={status < 400 ? "success" : status < 500 ? "warning" : "danger"}>{log.response_status_code ?? "—"}</Status>;
    } },
    { key: "consumer", header: localizedMessage(isZh, "v2.logs.consumerAndKey"), render: (log) => <div className="cell-stack"><strong>{log.consumer_id ?? (localizedMessage(isZh, "v2.logs.anonymous"))}</strong><code>{log.consumer_key_name ? formatKeyPreview(log.consumer_key_name) : "—"}</code></div> },
    { key: "model", header: localizedMessage(isZh, "v2.logs.modelAndUpstream"), render: (log) => <div className="cell-stack"><strong>{log.client_model ?? "—"}</strong><span>{log.upstream_name ?? log.upstream_id ?? "—"}{log.upstream_model ? ` : ${log.upstream_model}` : ""}</span></div> },
    { key: "protocol", header: localizedMessage(isZh, "v2.logs.protocolFlow"), render: (log) => <ProtocolLane ingress={log.client_protocol} egress={log.upstream_protocol} /> },
    { key: "latency", header: localizedMessage(isZh, "v2.logs.latency"), render: (log) => <code>{formatDuration(log.latency_total_ms)}</code> },
    { key: "tokens", header: "Token", render: (log) => <div className="cell-stack"><code>IN {formatTokenCount(log.input_tokens)}</code><code>OUT {formatTokenCount(log.output_tokens)}</code></div> },
    { key: "tps", header: "TPS", render: (log) => <code>{formatTps(computeTps(log))}</code> },
    { key: "type", header: localizedMessage(isZh, "v2.logs.type"), render: (log) => <span className="tag">{(log.is_stream ?? (log.stream_chunks_count ?? 0) > 0) ? "SSE" : getRouteType(log) === "embedding" ? "EMB" : "JSON"}</span> },
  ];

  return (
    <PageLayout header={<PageHeader title={t("page.logs.title")} description={`${t("page.logs.subtitle")} · ${t("page.logs.records", { count: total })}`} />}>
      <FilterBar>
        <NyroSearchSelect<FilterOption>
          options={consumerOptions}
          value={consumerValue}
          onChange={(next) => { setFilter((current) => ({ ...current, consumer_id: optionId(next) || undefined })); setPage(0); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.logs.consumerFilter")}
        />
        <NyroSearchSelect<FilterOption>
          options={modelOptions}
          value={routeValue}
          onChange={(next) => { setFilter((current) => ({ ...current, route_id: optionId(next) || undefined })); setPage(0); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.logs.modelFilter")}
        />
        <NyroSearchSelect<FilterOption>
          options={upstreamOptions}
          value={upstreamValue}
          onChange={(next) => { setFilter((current) => ({ ...current, upstream_id: optionId(next) || undefined })); setPage(0); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.logs.upstreamFilter")}
        />
        <NyroSearchSelect<FilterOption>
          options={statusOptions}
          value={statusValue}
          onChange={(next) => { setFilter((current) => applyLogStatusFilter(current, (optionId(next) || "all") as LogStatusFilter)); setPage(0); }}
          getOptionLabel={(option) => option.label}
          isOptionEqualToValue={(a, b) => a.id === b.id}
          searchable={false}
          fullWidth={false}
          controlClassName="toolbar-filter"
          leadingIcon={<Filter size={14} aria-hidden="true" />}
          ariaLabel={localizedMessage(isZh, "v2.logs.statusFilter")}
        />
        <button
          type="button"
          className="button button-sm toolbar-end"
          title={localizedMessage(isZh, "v2.logs.clearLogs")}
          aria-label={localizedMessage(isZh, "v2.logs.clearLogs")}
          disabled={total === 0}
          onClick={() => setConfirmOpen(true)}
        >
          <Trash2 aria-hidden="true" />
        </button>
      </FilterBar>
      <DataTable
        carded
        className="log-table"
        columns={columns}
        rows={items}
        rowKey={(log) => log.id}
        loading={isLoading}
        onRowClick={setSelected}
        empty={<EmptyState title={total ? (localizedMessage(isZh, "v2.logs.noMatchingRequests")) : (localizedMessage(isZh, "v2.logs.noRequestLogsYet"))} description={total ? (localizedMessage(isZh, "v2.logs.adjustTheActiveFilters")) : (localizedMessage(isZh, "v2.logs.logsAppearHereAfterTheGatewayReceivesRequests"))} />}
        footer={
          <>
            <span className="table-summary">{localizedMessage(isZh, "common.recordsCount", { count: total })}</span>
            {totalPages > 1 && (
              <div className="pagination">
                <span className="table-summary">{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span>
                <button
                  type="button"
                  className="page-button"
                  disabled={page === 0}
                  aria-label={localizedMessage(isZh, "common.prevPage")}
                  onClick={() => setPage((current) => current - 1)}
                >
                  <ChevronLeft aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="page-button"
                  disabled={page >= totalPages - 1}
                  aria-label={localizedMessage(isZh, "common.nextPage")}
                  onClick={() => setPage((current) => current + 1)}
                >
                  <ChevronRight aria-hidden="true" />
                </button>
              </div>
            )}
          </>
        }
      />

      <LogDetailDrawer
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
    return <span>–</span>;
  }
  return <code>{label}</code>;
}

function ProtocolLane({
  ingress,
  egress,
}: {
  ingress: string | null | undefined;
  egress: string | null | undefined;
}) {
  return (
    <span className="protocol-lane">
      <ProtocolCell value={ingress} />
      <span aria-hidden="true">→</span>
      <ProtocolCell value={egress} />
    </span>
  );
}
