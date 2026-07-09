import { useQuery } from "@tanstack/react-query";
import { Server } from "lucide-react";
import { backend } from "@/lib/backend";
import type { GatewayNode } from "@/lib/types";
import { useLocale } from "@/lib/i18n";

function formatConnectedAt(iso: string, isZh: boolean) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString(isZh ? "zh-CN" : "en-US");
}

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
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "数据面节点" : "Nodes"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh
            ? "当前通过 config-sync 连接到本控制面的数据面网关（实时连接视图，断开即消失，不做持久化）"
            : "Gateways currently connected to this control plane over config-sync (a live view of active connections — a node disappears the moment it disconnects; nothing here is persisted)"}
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
                <th className="px-3 py-2 text-left font-medium">{isZh ? "版本" : "Version"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "地址" : "Address"}</th>
                <th className="px-3 py-2 text-left font-medium">{isZh ? "连接时间" : "Connected At"}</th>
                <th className="px-3 py-2 text-right font-medium">{isZh ? "已应用配置版本" : "Applied Version"}</th>
              </tr>
            </thead>
            <tbody>
              {isLoading && (
                <tr>
                  <td className="px-3 py-6 text-center text-slate-400" colSpan={6}>
                    {isZh ? "加载中…" : "Loading…"}
                  </td>
                </tr>
              )}
              {!isLoading && nodes.length === 0 && (
                <tr>
                  <td className="px-3 py-6 text-center text-slate-400" colSpan={6}>
                    {isZh
                      ? "暂无已连接节点（数据面未连接，或控制面未启用 --grpc-addr）"
                      : "No nodes connected yet (no gateway connected, or --grpc-addr is not enabled on the control plane)"}
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
                  <td className="px-3 py-2">{n.app_version || "-"}</td>
                  <td className="px-3 py-2 font-mono text-xs">{n.remote_addr || "-"}</td>
                  <td className="px-3 py-2">{formatConnectedAt(n.connected_at, isZh)}</td>
                  <td className="px-3 py-2 text-right">{n.applied_version}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
