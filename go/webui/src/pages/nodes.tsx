import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Info, Lock, LockOpen, RefreshCw, ShieldAlert } from "lucide-react";

import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import {
  buildNodeTopology,
  isNodeConnectionVerified,
  NODE_CONNECTION_MODES,
  normalizeNodeConnectionMode,
} from "@/features/nodes/node-topology";
import { backend } from "@/lib/backend";
import { formatUptime } from "@/lib/format";
import { useLocale } from "@/lib/i18n";
import { formatServiceAddress } from "@/lib/service-address";
import type { GatewayNode, RuntimeService } from "@/lib/types";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

function connectedAt(iso: string, locale: string) {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "medium" }).format(date);
}

export default function NodesPage() {
  const { locale, t } = useLocale();
  const queryClient = useQueryClient();
  const nodesQuery = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => backend("list_nodes"),
    refetchInterval: 5_000,
  });
  const servicesQuery = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => backend("list_runtime_services"),
  });

  const nodes = nodesQuery.data ?? [];
  const versions = [...new Set(nodes.map((node) => node.applied_version))];
  const version = versions.length === 1 ? `rev-${versions[0]}` : versions.length > 1 ? "mixed" : "—";
  const controlAddress = servicesQuery.data?.find((service) => service.id === "control-plane")?.listen ?? "—";
  const remoteCount = nodes.filter((node) => normalizeNodeConnectionMode(node) !== "inprocess").length;
  const embeddedCount = nodes.length - remoteCount;
  const topology = buildNodeTopology(nodes);

  return (
    <PageLayout header={<PageHeader title={t("page.nodes.title")} description={t("page.nodes.subtitle")} />}>
      <section className="v2-node-ledger">
        <NodeMetric label={t("nodes.connected")} value={`${nodes.length}`} detail={nodes.length > 0 ? t("nodes.allOnline") : t("nodes.noNodes")} />
        <NodeMetric label={t("nodes.inProcess")} value={String(embeddedCount)} detail={t("nodes.embeddedDetail")} />
        <NodeMetric label={t("nodes.remoteMTLS")} value={String(remoteCount)} detail={t("nodes.remoteDetail")} />
        <NodeMetric label={t("nodes.configVersion")} value={version} detail={versions.length <= 1 ? t("nodes.versionConsistent") : t("nodes.mixedVersions")} mono />
      </section>

      <section className="v2-surface">
        <div className="v2-surface-head">
          <div><h2>{t("nodes.connectionMap")}</h2><p>{t("nodes.connectionMapDetail")}</p></div>
          {nodes.every(isNodeConnectionVerified) && nodes.length > 0 && <span className="v2-status status-success"><i />{t("nodes.identityHealthy")}</span>}
        </div>
        <div className={`v2-topology is-${topology.layout}`}>
          <div className="v2-topology-admin">
            <span>{t("nodes.configSource")}</span><strong>Nyro Admin</strong><code>{controlAddress}</code><small>{t("nodes.currentVersion", { version })}</small>
          </div>
          <div className="v2-topology-connections">
            {topology.connections.map(({ node, mode }) => (
              <div className="v2-topology-connection" key={node.node_id}>
                <div className="v2-topology-edge">
                  <span className="v2-topology-edge-label">
                    <small>{t("nodes.connectionMode")}</small>
                    <strong>{t(mode.label)}</strong>
                    <span>{t(mode.detail)}</span>
                  </span>
                </div>
                <div className="v2-topology-node">
                  <strong>{node.hostname || node.node_id || t("common.unknown")}</strong>
                  <code>{formatServiceAddress(node.remote_addr, node.service_port)}</code>
                  <span><i />{t("common.online")} · {formatUptime(node.connected_at)}</span>
                </div>
              </div>
            ))}
            {topology.layout === "empty" && <div className="v2-topology-empty">{t("nodes.noNodes")}</div>}
          </div>
        </div>
      </section>

      <section className="v2-surface v2-node-table">
        <div className="v2-surface-head">
          <div><h2>{t("nodes.tableTitle")}</h2><p>{t("nodes.tableDetail")}</p></div>
          <button type="button" className="v2-button v2-button-icon" onClick={() => void queryClient.invalidateQueries({ queryKey: ["nodes"] })}><RefreshCw />{t("common.refresh")}</button>
        </div>
        <div className="v2-table-wrap">
          <table className="v2-data-table">
            <thead><tr><th>{t("nodes.node")}</th><th>{t("nodes.address")}</th><th>{t("nodes.connection")}</th><th>{t("nodes.identity")}</th><th>{t("nodes.version")}</th><th>{t("nodes.configVersion")}</th><th>{t("nodes.connectedFor")}</th></tr></thead>
            <tbody>
              {nodes.map((node) => {
                const mode = NODE_CONNECTION_MODES[normalizeNodeConnectionMode(node)];
                const isVerified = isNodeConnectionVerified(node);
                const address = formatServiceAddress(node.remote_addr, node.service_port);
                const ModeIcon = isVerified ? Lock : LockOpen;
                return (
                  <tr key={node.node_id}>
                    <td><strong className="v2-mono">{node.hostname || node.node_id || t("common.unknown")}</strong><small className="v2-mono">{node.node_id || "—"}</small></td>
                    <td><span className="v2-copy-value"><code>{address}</code><CopyButton value={address} /></span></td>
                    <td><span className="v2-connection-mode"><ModeIcon />{t(mode.label)}</span></td>
                    <td>
                      <TooltipProvider delayDuration={120}><Tooltip><TooltipTrigger asChild>
                        <span className={`v2-identity ${isVerified ? "verified" : "unverified"}`}>{isVerified ? <Lock /> : <ShieldAlert />}{isVerified ? t("nodes.verified") : t("nodes.unverified")}</span>
                      </TooltipTrigger>{!isVerified && <TooltipContent>{t("nodes.unverifiedHelp")}</TooltipContent>}</Tooltip></TooltipProvider>
                    </td>
                    <td>{node.app_version || "—"}</td><td className="v2-mono">rev-{node.applied_version}</td>
                    <td><TooltipProvider delayDuration={120}><Tooltip><TooltipTrigger asChild><span className="v2-help-value">{formatUptime(node.connected_at)}<Info /></span></TooltipTrigger><TooltipContent>{connectedAt(node.connected_at, locale)}</TooltipContent></Tooltip></TooltipProvider></td>
                  </tr>
                );
              })}
              {!nodesQuery.isLoading && nodes.length === 0 && <tr><td colSpan={7}><div className="v2-inline-empty">{t("nodes.noNodes")}</div></td></tr>}
              {nodesQuery.isLoading && <tr><td colSpan={7}><div className="v2-inline-empty">{t("common.loading")}</div></td></tr>}
            </tbody>
          </table>
        </div>
        <div className="v2-surface-foot"><span>{t("nodes.autoRefresh")}</span><span>{t("nodes.addressHelp")}</span></div>
      </section>
    </PageLayout>
  );
}

function NodeMetric({ label, value, detail, mono = false }: { label: string; value: string; detail: string; mono?: boolean }) {
  return <div><span>{label}</span><strong className={mono ? "v2-mono" : undefined}>{value}</strong><small>{detail}</small></div>;
}

function CopyButton({ value }: { value: string }) {
  const { t } = useLocale();
  const [copied, setCopied] = useState(false);
  return <button type="button" title={t("common.copy")} onClick={async () => { await navigator.clipboard.writeText(value); setCopied(true); window.setTimeout(() => setCopied(false), 1200); }}>{copied ? <Check /> : <Copy />}</button>;
}
