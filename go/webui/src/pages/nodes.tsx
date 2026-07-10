import { useQuery } from "@tanstack/react-query";
import { Info, Server } from "lucide-react";
import { backend } from "@/lib/backend";
import type { GatewayNode } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

// Locale-specific formatting: zh-CN reads as 24-hour "YYYY/MM/DD HH:mm:ss",
// en-US as 12-hour "MM/DD/YYYY hh:mm:ss AM/PM" — Intl.DateTimeFormat with
// explicit "2-digit" parts (rather than the bare toLocaleString default)
// guarantees single-digit month/day/hour are always zero-padded. Formats via
// formatToParts (not .format) so the en-US locale's date/time separator
// comma can be dropped in favor of a plain space.
function formatConnectedAt(iso: string, isZh: boolean) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const parts = new Intl.DateTimeFormat(isZh ? "zh-CN" : "en-US", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: !isZh,
  }).formatToParts(d);
  return parts.map((p) => (p.type === "literal" ? p.value.replace(",", "") : p.value)).join("");
}

// remote_addr is the config-sync gRPC connection's peer address (host +
// ephemeral source port) — the port has no relation to the gateway's own
// service port, so it's stripped here to avoid misleading readers.
function formatAddressHost(addr: string) {
  if (!addr) return "";
  if (addr.startsWith("[")) {
    // Bracketed IPv6, e.g. "[::1]:54321" -> "[::1]".
    const bracketEnd = addr.indexOf("]");
    return bracketEnd >= 0 ? addr.slice(0, bracketEnd + 1) : addr;
  }
  const lastColon = addr.lastIndexOf(":");
  if (lastColon <= 0) return addr;
  return addr.slice(0, lastColon);
}

// formatServiceAddress combines the two independently-sourced pieces that
// together make up "where this gateway actually serves traffic": the host
// from remote_addr (the real gRPC peer IP, observed by admin) and the port
// from service_port (self-reported by the gateway from its own --listen).
// Either can be missing on its own (an older gateway build, or a connection
// still mid-handshake), so each falls back independently rather than the
// whole cell collapsing to "-" when only one side is present.
function formatServiceAddress(remoteAddr: string, servicePort: string) {
  const host = formatAddressHost(remoteAddr);
  if (!host && !servicePort) return "-";
  if (!host) return `:${servicePort}`;
  if (!servicePort) return host;
  return `${host}:${servicePort}`;
}

const COLUMN_COUNT = 7;

export default function NodesPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const { data: nodes = [], isLoading } = useQuery<GatewayNode[]>({
    queryKey: ["nodes"],
    queryFn: () => backend("list_nodes"),
    refetchInterval: 5_000,
  });

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "节点" : "Nodes"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh
            ? "当前已连接的网关节点，实时更新；"
            : "Gateway nodes currently connected here, updated in real time"}
        </p>
      </div>

      <div className="glass rounded-2xl p-6">
        <div className="mb-4 flex items-center justify-between">
          <h3 className="text-sm font-semibold text-slate-800">
            {isZh ? `已连接节点 (${nodes.length})` : `Connected Nodes (${nodes.length})`}
          </h3>
        </div>
        <div className="overflow-hidden rounded-xl border border-white/70 bg-white/50">
          <table className="w-full text-sm">
            <thead className="bg-white/70 text-slate-500">
              <tr>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "节点" : "Node"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "主机名" : "Hostname"}</th>
                <th className="px-3 py-2 text-left font-medium">
                  <span className="inline-flex items-center gap-1">
                    {isZh ? "服务地址" : "Service Address"}
                    <TooltipProvider delayDuration={120}>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <span
                            className="inline-flex cursor-help text-slate-400 hover:text-slate-600"
                            aria-label={
                              isZh
                                ? "主机来自与该网关的实际连接地址；端口为网关自行上报的服务端口，两者拼接而成"
                                : "Host is the real connection address to this gateway; port is self-reported by the gateway"
                            }
                          >
                            <Info className="h-3.5 w-3.5" />
                          </span>
                        </TooltipTrigger>
                        <TooltipContent>
                          {isZh
                            ? "主机来自与该网关的实际连接地址；端口为网关自行上报的服务端口，两者拼接而成"
                            : "Host is the real connection address to this gateway; port is self-reported by the gateway"}
                        </TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  </span>
                </th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "网关版本" : "Gateway Version"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "配置版本" : "Config Version"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "连接时间" : "Connected At"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "状态" : "Status"}</th>
              </tr>
            </thead>
            <tbody>
              {isLoading && (
                <tr>
                  <td className="px-3 py-6 text-center text-slate-400" colSpan={COLUMN_COUNT}>
                    {isZh ? "加载中…" : "Loading…"}
                  </td>
                </tr>
              )}
              {!isLoading && nodes.length === 0 && (
                <tr>
                  <td className="px-3 py-6 text-center text-slate-400" colSpan={COLUMN_COUNT}>
                    {isZh
                      ? "暂无已连接的网关节点"
                      : "No gateway nodes connected yet"}
                  </td>
                </tr>
              )}
              {nodes.map((n) => (
                <tr key={n.node_id} className="border-t border-white/70 text-slate-700">
                  <td className="px-3 py-2 font-medium">
                    <span className="inline-flex items-center gap-2">
                      <Server className="h-3.5 w-3.5 text-purple-600" />
                      {n.node_id || (isZh ? "（未知）" : "(unknown)")}
                    </span>
                  </td>
                  <td className="px-3 py-2">{n.hostname || "-"}</td>
                  <td className="px-3 py-2 font-mono text-xs">
                    {formatServiceAddress(n.remote_addr, n.service_port)}
                  </td>
                  <td className="px-3 py-2">{n.app_version || "-"}</td>
                  <td className="px-3 py-2">{n.applied_version}</td>
                  <td className="px-3 py-2">{formatConnectedAt(n.connected_at, isZh)}</td>
                  <td className="px-3 py-2">
                    <span className="inline-flex items-center gap-1.5 text-xs text-emerald-700">
                      <span className="h-2 w-2 rounded-full bg-emerald-500" />
                      {isZh ? "已连接" : "Connected"}
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
