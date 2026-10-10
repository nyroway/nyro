import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Check, Copy, Info, RefreshCw } from "lucide-react";

import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Status } from "@/components/v2/status";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import {
  isNodeConnectionVerified,
  NODE_CONNECTION_MODES,
  normalizeNodeConnectionMode,
} from "@/features/nodes/node-topology";
import { systemApi } from "@/lib/api/system";
import { formatUptime } from "@/lib/format";
import { useLocale } from "@/lib/i18n";
import { formatServiceAddress } from "@/lib/service-address";
import type { GatewayNode } from "@/lib/types";

function connectedAt(iso: string, locale: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(date);
}

export default function NodesPage() {
  const { locale, t } = useLocale();
  const navigate = useNavigate();
  const { data: nodes = [], isLoading, isError, isFetching, refetch } = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => systemApi.nodes(),
    refetchInterval: 5_000,
  });

  const versions = [...new Set(nodes.map((node) => node.applied_version))];
  const version = versions.length === 1 ? `rev-${versions[0]}` : versions.length > 1 ? "mixed" : "—";
  const remoteCount = nodes.filter((node) => normalizeNodeConnectionMode(node) !== "inprocess").length;
  const embeddedCount = nodes.length - remoteCount;
  const unverifiedCount = nodes.filter((node) => !isNodeConnectionVerified(node)).length;
  // ISO timestamps compare lexicographically: take the earliest-connected node as the "Longest connection".
  const longest = nodes.reduce<GatewayNode | undefined>(
    (best, node) => (!best || node.connected_at < best.connected_at ? node : best),
    undefined,
  );
  const isEmpty = !isLoading && !isError && nodes.length === 0;

  const columns: DataTableColumn<GatewayNode>[] = [
    {
      key: "node",
      header: t("nodes.node"),
      render: (node) => (
        <div className="cell-stack">
          <strong>{node.hostname || node.node_id || t("common.unknown")}</strong>
          <small>{node.node_id || "—"}</small>
        </div>
      ),
    },
    {
      key: "address",
      header: t("nodes.address"),
      render: (node) => {
        const address = formatServiceAddress(node.remote_addr, node.service_port);
        return (
          <span className="cell-copy">
            <code>{address}</code>
            <CopyValue value={address} label={t("common.copy")} />
          </span>
        );
      },
    },
    {
      key: "connection",
      header: t("nodes.connection"),
      render: (node) => t(NODE_CONNECTION_MODES[normalizeNodeConnectionMode(node)].label),
    },
    {
      key: "identity",
      header: t("nodes.identity"),
      render: (node) => {
        const verified = isNodeConnectionVerified(node);
        return (
          <TooltipProvider delayDuration={120}>
            <Tooltip>
              <TooltipTrigger asChild>
                <Status tone={verified ? "success" : "warning"}>
                  {verified ? t("nodes.verified") : t("nodes.unverified")}
                </Status>
              </TooltipTrigger>
              {!verified && <TooltipContent>{t("nodes.unverifiedHelp")}</TooltipContent>}
            </Tooltip>
          </TooltipProvider>
        );
      },
    },
    { key: "version", header: t("nodes.version"), render: (node) => node.app_version || "—" },
    {
      key: "configVersion",
      header: t("nodes.configVersion"),
      render: (node) => <code className="code-pill">rev-{node.applied_version}</code>,
    },
    {
      key: "uptime",
      header: t("nodes.connectedFor"),
      render: (node) => (
        <TooltipProvider delayDuration={120}>
          <Tooltip>
            <TooltipTrigger asChild>
              <span className="help-value">{formatUptime(node.connected_at)}</span>
            </TooltipTrigger>
            <TooltipContent>{connectedAt(node.connected_at, locale)}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ),
    },
  ];

  return (
    <PageLayout header={<PageHeader title={t("page.nodes.title")} description={t("page.nodes.subtitle")} />}>
      <MetricLedger items={[
        { key: "connected", label: t("nodes.connected"), value: `${nodes.length}`, detail: nodes.length > 0 ? t("nodes.allOnline") : t("nodes.noNodes") },
        { key: "embedded", label: t("nodes.inProcess"), value: String(embeddedCount), detail: t("nodes.embeddedDetail") },
        { key: "remote", label: t("nodes.remote"), value: String(remoteCount), detail: t("nodes.remoteDetail") },
        { key: "uptime", label: t("nodes.uptime"), value: longest ? formatUptime(longest.connected_at) : "—", detail: longest ? t("nodes.uptimeHint", { host: longest.hostname || longest.node_id }) : undefined },
        { key: "version", label: t("nodes.configVersion"), value: version, detail: versions.length > 1 ? t("nodes.mixedVersions") : t("nodes.versionConsistent"), tone: versions.length > 1 ? "warning" : "default" },
      ]} />

      {isEmpty && (
        <div className="info-callout">
          <Info aria-hidden="true" />
          <span>{t("nodes.emptyCallout")}</span>
        </div>
      )}

      <DataTable
        carded
        columns={columns}
        rows={nodes}
        rowKey={(node) => node.node_id}
        loading={isLoading}
        empty={(
          <EmptyState
            title={isError ? t("common.error") : t("nodes.noNodes")}
            action={isError ? undefined : (
              <button type="button" className="button button-sm" onClick={() => navigate("/settings")}>
                {t("nodes.openSettings")}
              </button>
            )}
          />
        )}
        toolbar={(
          <>
            {isError && <Status tone="danger">{t("common.error")}</Status>}
            {nodes.length > 0 && (unverifiedCount === 0
              ? <Status tone="success">{t("nodes.identityHealthy")}</Status>
              : <Status tone="warning">{t("nodes.someUnverified", { count: unverifiedCount })}</Status>)}
            <button
              type="button"
              className="button button-sm toolbar-end"
              disabled={isFetching}
              onClick={() => void refetch()}
            >
              <RefreshCw aria-hidden="true" />
              {t("common.refresh")}
            </button>
          </>
        )}
        footer={(
          <>
            <span className="table-summary">{t("nodes.autoRefresh")}</span>
            <span className="table-summary">{t("nodes.addressHelp")}</span>
          </>
        )}
      />
    </PageLayout>
  );
}

function CopyValue({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="icon-button"
      title={label}
      aria-label={label}
      onClick={() => {
        void navigator.clipboard.writeText(value);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1200);
      }}
    >
      {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
    </button>
  );
}
