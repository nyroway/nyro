import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { backend } from "@/lib/backend";
import type { GatewayNode } from "@/lib/types";
import { useLocale } from "@/lib/i18n";

// Locale-specific formatting: zh-CN reads as 24-hour "YYYY/MM/DD HH:mm:ss",
// en-US as 12-hour "MM/DD/YYYY, hh:mm:ss AM/PM" — Intl.DateTimeFormat with
// explicit "2-digit" parts (rather than the bare toLocaleString default)
// guarantees single-digit month/day/hour are always zero-padded.
function formatConnectedAt(iso: string, isZh: boolean) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return new Intl.DateTimeFormat(isZh ? "zh-CN" : "en-US", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: !isZh,
  }).format(d);
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

const COLUMN_COUNT = 8;

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
                <th className="px-3 py-2 text-left font-medium">{isZh ? "主机地址" : "Host Address"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "服务端口" : "Service Port"}</th>
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
                  <td className="px-3 py-2 font-mono text-xs">{formatAddressHost(n.remote_addr) || "-"}</td>
                  <td className="px-3 py-2 font-mono text-xs">{n.service_port || "-"}</td>
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
