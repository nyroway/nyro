import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Check, Copy, Download, Loader2 } from "lucide-react";

import { backend } from "@/lib/backend";
import { useLocale } from "@/lib/i18n";
import type { RequestLog } from "@/lib/types";
import { formatDuration, formatLogTime, formatTokenCount, tryPrettyJson } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { cn } from "@/lib/utils";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

function protocolLabel(raw: string | null | undefined): string {
  return prettyName(raw) ?? raw ?? "–";
}

interface LogDetailDialogProps {
  logId: string | null;
  summary?: RequestLog | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function LogDetailDialog({ logId, summary, open, onOpenChange }: LogDetailDialogProps) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const { data, isLoading } = useQuery<RequestLog | null>({
    queryKey: ["log-detail", logId],
    queryFn: () => backend("get_log", { id: logId! }),
    enabled: open && !!logId,
  });

  const [downloaded, setDownloaded] = useState(false);

  const log = data ?? summary ?? null;

  const method = log?.method ?? "–";
  const path = log?.path ?? "–";
  const clientStatus = log?.client_status_code;
  const statusOk = (clientStatus ?? 0) < 400;
  // is_stream is the canonical flag (declared by the client). Fall back to
  // stream_chunks_count for older log rows that pre-date the field.
  const isStream = log?.is_stream ?? (log?.stream_chunks_count ?? 0) > 0;
  const isCrossProtocol =
    log?.client_protocol &&
    log?.upstream_protocol &&
    log.client_protocol !== log.upstream_protocol;

  useEffect(() => {
    if (!downloaded) return;
    const t = window.setTimeout(() => setDownloaded(false), 1500);
    return () => window.clearTimeout(t);
  }, [downloaded]);

  const handleDownload = () => {
    if (!log) return;
    const ts = formatLogTime(log.created_at);
    const proto = isCrossProtocol
      ? `${log.client_protocol ?? "–"} → ${log.upstream_protocol ?? "–"} (cross-protocol)`
      : (log.client_protocol ?? "–");
    const lines: string[] = [
      `# Nyro Request Log`,
      `# ID: ${log.id}`,
      `# Time: ${ts}`,
      `# Method: ${method}  Path: ${path}`,
      `# Client Status: ${log.client_status_code ?? "–"}  Upstream Status: ${log.upstream_status_code ?? "–"}`,
      `# Latency Total: ${formatDuration(log.latency_total_ms)}  Upstream: ${formatDuration(log.latency_upstream_ms)}`,
      `# Provider: ${log.provider_name ?? log.provider_id ?? "–"}  Model: ${log.model_name ?? log.model_id ?? "–"}  ApiKey: ${log.api_key_name ?? log.api_key_id ?? "–"}`,
      `# Client Model: ${log.client_model ?? "–"}  Upstream Model: ${log.upstream_model ?? "–"}`,
      `# Protocol: ${proto}`,
      `# Tokens: IN=${log.input_tokens} OUT=${log.output_tokens}`,
      isStream ? `# Stream: chunks=${log.stream_chunks_count} ttfb=${log.stream_first_chunk_ms ?? "–"}ms` : `# Stream: false`,
      "",
      "## 1. CLIENT REQUEST HEADERS",
      log.client_request_headers ?? "(empty)",
      "",
      "## 1. CLIENT REQUEST BODY",
      log.client_request_body ?? "(empty)",
      "",
      "## 2. UPSTREAM REQUEST HEADERS",
      log.upstream_request_headers ?? "(empty)",
      "",
      "## 2. UPSTREAM REQUEST BODY",
      log.upstream_request_body ?? "(empty)",
      "",
      "## 3. UPSTREAM RESPONSE HEADERS",
      log.upstream_response_headers ?? "(empty)",
      "",
      "## 3. UPSTREAM RESPONSE BODY",
      log.upstream_response_body ?? "(empty)",
      "",
      "## 4. CLIENT RESPONSE HEADERS",
      log.client_response_headers ?? "(empty)",
      "",
      "## 4. CLIENT RESPONSE BODY",
      log.client_response_body ?? "(empty)",
    ];
    const blob = new Blob([lines.join("\n")], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `nyro-log-${log.id}.log`;
    a.click();
    URL.revokeObjectURL(url);
    setDownloaded(true);
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        className="w-[min(92vw,960px)] max-h-[88vh] overflow-hidden flex flex-col gap-4"
        onOpenAutoFocus={(e) => e.preventDefault()}
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <span>{isZh ? "请求详情" : "Request Detail"}</span>
            {isLoading ? <Loader2 className="h-3.5 w-3.5 animate-spin text-slate-400" /> : null}
          </DialogTitle>
          <DialogDescription>
            {log ? formatLogTime(log.created_at) : ""}
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="outline" className="font-mono">{method}</Badge>
          <span className="font-mono text-slate-600 break-all">{path}</span>
          <span
            className={cn(
              "inline-flex rounded-full px-2 py-0.5 font-medium",
              statusOk ? "bg-green-50 text-green-700" : "bg-red-50 text-red-600",
            )}
          >
            {clientStatus ?? "–"}
          </span>
          {isStream ? (
            <Badge variant="outline" className="border-green-200 bg-green-50 text-green-700">SSE</Badge>
          ) : (
            <Badge variant="outline" className="border-sky-200 bg-sky-50 text-sky-700">JSON</Badge>
          )}
          {isCrossProtocol ? (
            <Badge variant="outline" className="border-purple-200 bg-purple-50 text-purple-700">
              {isZh ? "跨协议" : "Cross-Protocol"}
            </Badge>
          ) : null}
          {(log?.provider_name ?? log?.provider_id) ? (
            <Badge variant="outline">{log.provider_name ?? log.provider_id}</Badge>
          ) : null}
          {log?.model_name ? (
            <Badge variant="outline" className="border-slate-200 bg-slate-50 text-slate-500">{log.model_name}</Badge>
          ) : null}
          {log?.api_key_name ? (
            <Badge variant="outline" className="border-amber-200 bg-amber-50 text-amber-700">{log.api_key_name}</Badge>
          ) : null}
          {log?.upstream_model ? (
            <span className="text-slate-500 font-mono">{log.upstream_model}</span>
          ) : null}
          {log?.latency_total_ms != null ? (
            <span className="text-slate-500">{formatDuration(log.latency_total_ms)}</span>
          ) : null}
          {log ? (
            <span className="inline-flex items-center gap-2">
              <span className="inline-flex items-center gap-1 text-sky-600">
                <span className="text-[10px] font-semibold tracking-wide">IN</span>
                <span title={String(log.input_tokens)}>{formatTokenCount(log.input_tokens)}</span>
              </span>
              <span className="inline-flex items-center gap-1 text-emerald-600">
                <span className="text-[10px] font-semibold tracking-wide">OUT</span>
                <span title={String(log.output_tokens)}>{formatTokenCount(log.output_tokens)}</span>
              </span>
              <Button
                type="button"
                size="sm"
                variant="ghost"
                onClick={handleDownload}
                className="h-7 gap-1 px-2 text-xs"
              >
                {downloaded ? (
                  <><Check className="h-3.5 w-3.5 text-green-600" /><span className="text-green-600">{isZh ? "已保存" : "Saved"}</span></>
                ) : (
                  <><Download className="h-3.5 w-3.5" />{isZh ? "下载" : "Download"}</>
                )}
              </Button>
            </span>
          ) : null}
        </div>

        <div className="flex-1 space-y-3 overflow-y-auto pr-1">
          <SectionHeader
            title={isZh ? "1. 客户端请求" : "1. Client Request"}
            hint={isZh ? `协议：${protocolLabel(log?.client_protocol)}` : `Protocol: ${protocolLabel(log?.client_protocol)}`}
          />
          <PayloadBlock
            title={isZh ? "客户端请求头" : "Client Request Headers"}
            content={log?.client_request_headers}
            isZh={isZh}
          />
          <PayloadBlock
            title={isZh ? "客户端请求体" : "Client Request Body"}
            content={log?.client_request_body}
            isZh={isZh}
          />

          <SectionHeader
            title={isZh ? "2. 上游请求" : "2. Upstream Request"}
            hint={isCrossProtocol
              ? (isZh
                  ? `Nyro 转换输出 → ${protocolLabel(log?.upstream_protocol)}`
                  : `Nyro converted → ${protocolLabel(log?.upstream_protocol)}`)
              : undefined}
          />
          <PayloadBlock
            title={isZh ? "上游请求头" : "Upstream Request Headers"}
            content={log?.upstream_request_headers}
            isZh={isZh}
          />
          <PayloadBlock
            title={isZh ? "上游请求体" : "Upstream Request Body"}
            content={log?.upstream_request_body}
            isZh={isZh}
          />

          <SectionHeader
            title={isZh ? "3. 上游响应" : "3. Upstream Response"}
            hint={isZh ? `协议：${protocolLabel(log?.upstream_protocol)}` : `Protocol: ${protocolLabel(log?.upstream_protocol)}`}
          />
          <PayloadBlock
            title={isZh ? "上游响应头" : "Upstream Response Headers"}
            content={log?.upstream_response_headers}
            isZh={isZh}
          />
          <PayloadBlock
            title={isZh ? "上游响应体" : "Upstream Response Body"}
            content={log?.upstream_response_body}
            isZh={isZh}
          />

          <SectionHeader
            title={isZh ? "4. 客户端响应" : "4. Client Response"}
            hint={isCrossProtocol
              ? (isZh
                  ? `Nyro 转换输出 → ${protocolLabel(log?.client_protocol)}`
                  : `Nyro converted → ${protocolLabel(log?.client_protocol)}`)
              : undefined}
          />
          <PayloadBlock
            title={isZh ? "客户端响应头" : "Client Response Headers"}
            content={log?.client_response_headers}
            isZh={isZh}
          />
          <PayloadBlock
            title={isZh ? "客户端响应体" : "Client Response Body"}
            content={log?.client_response_body}
            isZh={isZh}
          />
        </div>
      </DialogContent>
    </Dialog>
  );
}

function SectionHeader({ title, hint }: { title: string; hint?: string }) {
  return (
    <div className="flex items-center gap-2 pt-1">
      <span className="text-xs font-semibold text-slate-500 uppercase tracking-wider">{title}</span>
      {hint ? (
        <span className="text-[10px] text-slate-400 font-normal normal-case tracking-normal shrink-0">{hint}</span>
      ) : null}
      <div className="flex-1 border-t border-slate-200" />
    </div>
  );
}

interface PayloadBlockProps {
  title: string;
  content: string | null | undefined;
  isZh: boolean;
}

function PayloadBlock({ title, content, isZh }: PayloadBlockProps) {
  const [copied, setCopied] = useState(false);
  const [collapsed, setCollapsed] = useState(true);
  const pretty = tryPrettyJson(content);
  const hasContent = !!(content && content.trim());

  useEffect(() => {
    if (!copied) return;
    const t = window.setTimeout(() => setCopied(false), 1500);
    return () => window.clearTimeout(t);
  }, [copied]);

  const handleCopy = async () => {
    if (!hasContent) return;
    try {
      await navigator.clipboard.writeText(pretty);
      setCopied(true);
    } catch {
      setCopied(false);
    }
  };

  return (
    <div className="rounded-lg border border-slate-200 bg-slate-50/60">
      <div
        className="flex cursor-pointer items-center justify-between border-b border-slate-200 px-3 py-1.5"
        onClick={() => setCollapsed((v) => !v)}
      >
        <span className="text-xs font-medium text-slate-600">{title}</span>
        <div className="flex items-center gap-1" onClick={(e) => e.stopPropagation()}>
          <Button
            type="button"
            size="sm"
            variant="ghost"
            disabled={!hasContent}
            onClick={handleCopy}
            className="h-7 gap-1 px-2 text-xs"
          >
            {copied ? (
              <>
                <Check className="h-3.5 w-3.5" />
                {isZh ? "已复制" : "Copied"}
              </>
            ) : (
              <>
                <Copy className="h-3.5 w-3.5" />
                {isZh ? "复制" : "Copy"}
              </>
            )}
          </Button>
        </div>
      </div>
      {!collapsed && (
        <pre className="max-h-80 overflow-auto whitespace-pre-wrap break-all px-3 py-2 font-mono text-[11px] leading-relaxed text-slate-700">
          {hasContent ? pretty : <span className="text-slate-400">{isZh ? "（无内容）" : "(empty)"}</span>}
        </pre>
      )}
      {collapsed && (
        <div
          className="cursor-pointer px-3 py-1.5 text-[11px] text-slate-400 hover:text-slate-600"
          onClick={() => setCollapsed(false)}
        >
          {hasContent ? (isZh ? "点击展开" : "click to expand") : (isZh ? "（无内容）" : "(empty)")}
        </div>
      )}
    </div>
  );
}

