import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { ChevronLeft, ChevronRight, Trash2 } from "lucide-react";

import { backend } from "@/lib/backend";
import type { ApiKey, LogPage, LogQuery, ModelStats, Provider, RequestLog } from "@/lib/types";
import { getRouteType } from "@/lib/types";
import { computeTps, formatDuration, formatLogTime, formatTokenCount, formatTps } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { useLocale } from "@/lib/i18n";
import { LogDetailDialog } from "@/components/log-detail-dialog";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { showToast } from "@/lib/toast";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { NyroSelect } from "@/components/ui/nyro-select";
import { copyableTipProps } from "@/lib/copyable";
import { localizedMessage } from "@/lib/messages";


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
      showToast(localizedMessage(isZh, "logs.logsCleared"));
    },
  });

  const query: LogQuery = { ...filter, limit: PAGE_SIZE, offset: page * PAGE_SIZE };

  const { data, isLoading } = useQuery<LogPage>({
    queryKey: ["logs", query],
    queryFn: () => backend("query_logs", { query }),
    refetchInterval: 5_000,
  });
  const { data: providers = [] } = useQuery<Provider[]>({
    queryKey: ["providers"],
    queryFn: () => backend("get_providers"),
  });
  const { data: modelStats = [] } = useQuery<ModelStats[]>({
    queryKey: ["stats", "models", "log-filter"],
    queryFn: () => backend("get_stats_by_model"),
  });
  const { data: apiKeys = [] } = useQuery<ApiKey[]>({
    queryKey: ["api-keys", "log-filter"],
    queryFn: () => backend("list_api_keys"),
  });

  const items = data?.items ?? [];
  const total = data?.total ?? 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));

  const providerOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "logs.allProviders") },
      ...providers.map((p) => ({ value: p.id, label: p.name })),
    ],
    [providers, isZh],
  );
  const modelOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "logs.allModels") },
      ...modelStats
        .filter((m) => (m.model ?? "").trim())
        .map((m) => ({ value: m.model, label: m.model })),
    ],
    [modelStats, isZh],
  );
  const statusOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "logs.allStatus") },
      { value: "ok", label: localizedMessage(isZh, "logs.text2xxOnly") },
      { value: "error", label: localizedMessage(isZh, "logs.text4xxErrors") },
    ],
    [isZh],
  );
  const apiKeyOptions = useMemo(
    () => [
      { value: "", label: localizedMessage(isZh, "logs.allAPIKeys") },
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
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "logs.requestLogs")}
          description={localizedMessage(isZh, "logs.totalRecords", { a: total })}
        />
      )}
    >
      <div className="table-toolbar">
        <NyroSelect
          filter
          controlClassName="toolbar-filter toolbar-filter-adaptive"
          ariaLabel={localizedMessage(isZh, "logs.apiKeyFilter")}
          value={apiKeyFilterValue}
          options={apiKeyOptions.map((option) => ({
            value: option.value || ALL_OPTION,
            label: option.label,
          }))}
          onChange={(value) => {
            setFilter({ ...filter, api_key: value === ALL_OPTION ? undefined : value });
            setPage(0);
          }}
        />
        <NyroSelect
          filter
          controlClassName="toolbar-filter toolbar-filter-adaptive"
          ariaLabel={localizedMessage(isZh, "logs.providerFilter")}
          value={providerFilterValue}
          options={providerOptions.map((option) => ({
            value: option.value || ALL_OPTION,
            label: option.label,
          }))}
          onChange={(value) => {
            setFilter({ ...filter, provider: value === ALL_OPTION ? undefined : value });
            setPage(0);
          }}
        />
        <NyroSelect
          filter
          controlClassName="toolbar-filter toolbar-filter-adaptive"
          ariaLabel={localizedMessage(isZh, "logs.modelFilter")}
          value={modelFilterValue}
          options={modelOptions.map((option) => ({
            value: option.value || ALL_OPTION,
            label: option.label,
          }))}
          onChange={(value) => {
            setFilter({ ...filter, model: value === ALL_OPTION ? undefined : value });
            setPage(0);
          }}
        />
        <NyroSelect
          filter
          controlClassName="toolbar-filter toolbar-filter-adaptive"
          ariaLabel={localizedMessage(isZh, "logs.statusFilter")}
          value={statusFilterValue}
          options={statusOptions.map((option) => ({
            value: option.value || ALL_OPTION,
            label: option.label,
          }))}
          onChange={(next) => {
            if (next === "error") {
              setFilter({ ...filter, status_min: 400, status_max: undefined });
            } else if (next === "ok") {
              setFilter({ ...filter, status_min: 200, status_max: 299 });
            } else {
              setFilter({ ...filter, status_min: undefined, status_max: undefined });
            }
            setPage(0);
          }}
        />
        <div className="toolbar-end">
          <button
            type="button"
            className="icon-action danger"
            data-tip={localizedMessage(isZh, "logs.clearLogs")}
            aria-label={localizedMessage(isZh, "logs.clearLogs")}
            disabled={total === 0}
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 aria-hidden="true" />
          </button>
        </div>
      </div>

      {isLoading ? (
        <div className="card">
          <div className="empty">
            <span className="spinner" aria-hidden="true" />
            <strong>{localizedMessage(isZh, "common.loading")}</strong>
          </div>
        </div>
      ) : items.length === 0 ? (
        <div className="card">
          <div className="empty">
            <strong>{localizedMessage(isZh, "logs.noLogsYet")}</strong>
          </div>
        </div>
      ) : (
        <div className="card table-card log-table">
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.time")}</th>
                  <th>{localizedMessage(isZh, "common.status")}</th>
                  <th>{localizedMessage(isZh, "logs.apiKey")}</th>
                  <th>{localizedMessage(isZh, "common.model")}</th>
                  <th>{localizedMessage(isZh, "common.protocol")}</th>
                  <th>{localizedMessage(isZh, "common.latency")}</th>
                  <th>Token</th>
                  <th>{localizedMessage(isZh, "logs.tps")}</th>
                  <th>{localizedMessage(isZh, "logs.type")}</th>
                </tr>
              </thead>
              <tbody>
                {items.map((log) => {
                  const routeType = getRouteType(log);
                  const statusOk = (log.client_status_code ?? 0) < 400;
                  const isStream = log.is_stream ?? (log.stream_chunks_count ?? 0) > 0;
                  return (
                    <tr key={log.id} className="clickable" onClick={() => setSelected(log)}>
                      <td><code>{formatLogTime(log.created_at)}</code></td>
                      <td>
                        <span className={statusOk ? "status-tag" : "health danger"}>
                          {log.client_status_code ?? "–"}
                        </span>
                      </td>
                      <td>
                        <span className="name-ellipsis" {...copyableTipProps(log.api_key_name)}>{log.api_key_name ?? "–"}</span>
                      </td>
                      <td>
                        <div className="cell-stack">
                          <strong className="name-ellipsis" {...copyableTipProps(log.client_model)}>{log.client_model ?? "–"}</strong>
                          <span className="name-ellipsis" {...copyableTipProps([log.provider_name ?? log.provider_id, log.upstream_model].filter(Boolean).join(": ") || undefined)}>
                            {log.provider_name ?? log.provider_id ?? "–"}
                            {log.upstream_model ? `: ${log.upstream_model}` : ""}
                          </span>
                        </div>
                      </td>
                      <td>
                        <span className="card-title-row">
                          <ProtocolLane
                            ingress={log.client_protocol}
                            egress={log.upstream_protocol}
                          />
                          {routeType === "embedding" ? <span className="badge">EMB</span> : null}
                        </span>
                      </td>
                      <td><code>{formatDuration(log.latency_total_ms)}</code></td>
                      <td>
                        <div className="cell-stack">
                          <code title={String(log.input_tokens)}>IN {formatTokenCount(log.input_tokens)}</code>
                          {routeType === "embedding" && log.output_tokens === 0 ? null : (
                            <code title={String(log.output_tokens)}>OUT {formatTokenCount(log.output_tokens)}</code>
                          )}
                        </div>
                      </td>
                      <td title={localizedMessage(isZh, "logs.netGenerationSpeed")}>
                        <code>{formatTps(computeTps(log))}</code>
                      </td>
                      <td>
                        <span className="badge">{isStream ? "SSE" : "JSON"}</span>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>

          <div className="table-footer pager">
            <span className="table-summary">
              {localizedMessage(isZh, "common.pageOf", { a: page + 1, b: totalPages })}
            </span>
            <div className="pagination">
              <button
                type="button"
                className="page-button"
                onClick={() => setPage(Math.max(0, page - 1))}
                disabled={page === 0}
                data-tip={localizedMessage(isZh, "common.previousPage")}
                aria-label={localizedMessage(isZh, "common.previousPage")}
              >
                <ChevronLeft aria-hidden="true" />
              </button>
              <button
                type="button"
                className="page-button"
                onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                disabled={page >= totalPages - 1}
                data-tip={localizedMessage(isZh, "common.nextPage")}
                aria-label={localizedMessage(isZh, "common.nextPage")}
              >
                <ChevronRight aria-hidden="true" />
              </button>
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
        title={localizedMessage(isZh, "logs.clearAllLogs")}
        description={
          localizedMessage(isZh, "logs.allRequestLogsWillBePermanentlyDeletedThisActionCannotBe")
        }
        confirmText={localizedMessage(isZh, "logs.clear")}
        cancelText={localizedMessage(isZh, "common.cancel")}
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
  return <span>{label}</span>;
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
      <span>→</span>
      <ProtocolCell value={egress} />
    </span>
  );
}
