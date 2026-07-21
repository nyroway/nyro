import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Info, Lock, LockOpen, Server } from "lucide-react";
import { backend } from "@/lib/backend";
import type { GatewayNode } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { formatUptime } from "@/lib/format";
import { formatServiceAddress } from "@/lib/service-address";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";

// The absolute connect timestamp is only shown on hover (the cell itself shows
// uptime), so it uses one fixed, locale-independent format — "YYYY/MM/DD
// HH:mm:ss", 24-hour, zero-padded — rather than branching on the UI language.
function formatConnectedAt(iso: string) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}/${p(d.getMonth() + 1)}/${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// connModeBadge maps the config-sync stream's transport security to an icon +
// label. mTLS (mutually authenticated) reads as the secure baseline (green);
// server-only TLS is amber; plaintext / unknown shows an open lock.
function connModeBadge(mode: string | undefined, isZh: boolean) {
  switch (mode) {
    case "mtls":
      return { Icon: Lock, label: "mTLS", className: "text-emerald-600" };
    case "tls":
      return { Icon: Lock, label: "TLS", className: "text-amber-600" };
    default:
      return { Icon: LockOpen, label: isZh ? "明文" : "Plaintext", className: "text-slate-400" };
  }
}

// CopyButton copies `value` to the clipboard and briefly swaps its icon to a
// check as feedback. Self-contained so each row manages its own copied state.
function CopyButton({ value, isZh }: { value: string; isZh: boolean }) {
  const [copied, setCopied] = useState(false);
  if (!value || value === "-") return null;
  return (
    <button
      type="button"
      aria-label={isZh ? "复制" : "Copy"}
      title={copied ? (isZh ? "已复制" : "Copied") : isZh ? "复制" : "Copy"}
      className="inline-flex text-slate-400 transition-colors hover:text-slate-600"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value);
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        } catch {
          // Clipboard may be unavailable (insecure context); ignore silently.
        }
      }}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-emerald-600" /> : <Copy className="h-3.5 w-3.5" />}
    </button>
  );
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
                <th className="px-3 py-2 text-left font-medium">{isZh ? "连接" : "Connection"}</th>
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
                    <span className="inline-flex items-center gap-1.5">
                      {formatServiceAddress(n.remote_addr, n.service_port)}
                      <CopyButton value={formatServiceAddress(n.remote_addr, n.service_port)} isZh={isZh} />
                    </span>
                  </td>
                  <td className="px-3 py-2">{n.app_version || "-"}</td>
                  <td className="px-3 py-2">{n.applied_version}</td>
                  <td className="px-3 py-2">
                    {(() => {
                      const { Icon, label, className } = connModeBadge(n.conn_mode, isZh);
                      return (
                        <TooltipProvider delayDuration={120}>
                          <span className="inline-flex items-center gap-1.5">
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className={`inline-flex cursor-help ${className}`} aria-label={label}>
                                  <Icon className="h-3.5 w-3.5" />
                                </span>
                              </TooltipTrigger>
                              <TooltipContent>{label}</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <span className="cursor-help text-slate-700">
                                  {formatUptime(n.connected_at)}
                                </span>
                              </TooltipTrigger>
                              <TooltipContent>{formatConnectedAt(n.connected_at)}</TooltipContent>
                            </Tooltip>
                          </span>
                        </TooltipProvider>
                      );
                    })()}
                  </td>
                  <td className="px-3 py-2">
                    <span className="inline-flex items-center gap-1.5 text-xs font-bold text-emerald-700">
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
