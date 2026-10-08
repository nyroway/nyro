import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Check, Copy, Download } from "lucide-react";

import { logsApi } from "@/lib/api/logs";
import { useLocale } from "@/lib/i18n";
import type { RequestLog } from "@/lib/types";
import { computeTps, formatDuration, formatLogTime, formatTokenCount, formatTps, generationMsOf, tryPrettyJson } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { Inspector } from "@/components/v2/inspector";
import { Status } from "@/components/v2/status";
import { localizedMessage } from "@/lib/messages";

function protocolLabel(raw: string | null | undefined): string {
  return prettyName(raw) ?? raw ?? "–";
}

interface LogDetailDrawerProps {
  logId: string | null;
  summary?: RequestLog | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}

export function LogDetailDrawer({ logId, summary, open, onOpenChange }: LogDetailDrawerProps) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const { data } = useQuery<RequestLog | null>({
    queryKey: ["log-detail", logId],
    queryFn: () => logsApi.get(logId!),
    enabled: open && !!logId,
  });

  const [downloaded, setDownloaded] = useState(false);

  const log = data ?? summary ?? null;

  const method = log?.method ?? "–";
  const path = log?.path ?? "–";
  const responseStatus = log?.response_status_code;
  const statusOk = (responseStatus ?? 0) < 400;
  // is_stream is the canonical flag (declared by the client). Fall back to
  // stream_chunks_count for older log rows that pre-date the field.
  const isStream = log?.is_stream ?? (log?.stream_chunks_count ?? 0) > 0;

  const generationMs = generationMsOf(log);
  const tps = computeTps(log);
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
      `# Response Status: ${log.response_status_code ?? "–"}  Upstream Status: ${log.upstream_status_code ?? "–"}`,
      `# Latency Total: ${formatDuration(log.latency_total_ms)}  Upstream: ${formatDuration(log.latency_upstream_ms)}`,
      `# TPS: ${tps != null ? formatTps(tps) : "–"}  (gen ${formatDuration(generationMs)})`,
      `# Upstream: ${log.upstream_name ?? log.upstream_id ?? "–"}  Route: ${log.route_model ?? log.route_id ?? "–"}  Consumer: ${log.consumer_key_name ?? log.consumer_id ?? "–"}`,
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
    <Inspector
      open={open}
      onClose={() => onOpenChange(false)}
      title={localizedMessage(isZh, "v2.log-detail-dialog.requestDetail")}
      description={log ? formatLogTime(log.created_at) : "…"}
      className="drawer-wide"
    >
      {!log ? (
        <div className="empty">
          <span className="spinner" aria-label={localizedMessage(isZh, "common.loading")} />
        </div>
      ) : (
        <>
          <div className="drawer-meta">
            <code>{method}</code>
            <code>{path}</code>
            <Status tone={statusOk ? "success" : "danger"}>{responseStatus ?? "–"}</Status>
            <span className="tag">{isStream ? "SSE" : "JSON"}</span>
            {isCrossProtocol ? (
              <span className="tag">{localizedMessage(isZh, "v2.log-detail-dialog.crossProtocol")}</span>
            ) : null}
            {(log.upstream_name ?? log.upstream_id) ? (
              <span className="tag">{log.upstream_name ?? log.upstream_id}</span>
            ) : null}
            {log.route_model ? <span className="tag">{log.route_model}</span> : null}
            {log.consumer_key_name ? <span className="tag">{log.consumer_key_name}</span> : null}
            {log.upstream_model ? <code>{log.upstream_model}</code> : null}
            {log.latency_total_ms != null ? <code>{formatDuration(log.latency_total_ms)}</code> : null}
            {tps != null ? (
              <code title={localizedMessage(isZh, "v2.log-detail-dialog.netGenerationSpeedExcludesPrefillWait")}>
                {formatTps(tps)}
              </code>
            ) : null}
            <code title={String(log.input_tokens)}>IN {formatTokenCount(log.input_tokens)}</code>
            <code title={String(log.output_tokens)}>OUT {formatTokenCount(log.output_tokens)}</code>
            <button type="button" className="button button-sm" onClick={handleDownload}>
              {downloaded ? <Check aria-hidden="true" /> : <Download aria-hidden="true" />}
              {downloaded
                ? localizedMessage(isZh, "v2.log-detail-dialog.saved")
                : localizedMessage(isZh, "v2.log-detail-dialog.download")}
            </button>
          </div>

          <div className="section-grid">
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.clientRequestHeaders")}
              meta={localizedMessage(isZh, "logDetail.protocol", { protocol: protocolLabel(log.client_protocol) })}
              content={log.client_request_headers}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.clientRequestBody")}
              content={log.client_request_body}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.upstreamRequestHeaders")}
              meta={isCrossProtocol
                ? localizedMessage(isZh, "logDetail.convertedTo", { protocol: protocolLabel(log.upstream_protocol) })
                : undefined}
              content={log.upstream_request_headers}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.upstreamRequestBody")}
              meta={isCrossProtocol
                ? localizedMessage(isZh, "logDetail.convertedTo", { protocol: protocolLabel(log.upstream_protocol) })
                : undefined}
              content={log.upstream_request_body}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.upstreamResponseHeaders")}
              meta={localizedMessage(isZh, "logDetail.protocol", { protocol: protocolLabel(log.upstream_protocol) })}
              content={log.upstream_response_headers}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.upstreamResponseBody")}
              content={log.upstream_response_body}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.clientResponseHeaders")}
              meta={isCrossProtocol
                ? localizedMessage(isZh, "logDetail.convertedTo", { protocol: protocolLabel(log.client_protocol) })
                : undefined}
              content={log.client_response_headers}
              isZh={isZh}
            />
            <PayloadBlock
              title={localizedMessage(isZh, "v2.log-detail-dialog.clientResponseBody")}
              meta={isCrossProtocol
                ? localizedMessage(isZh, "logDetail.convertedTo", { protocol: protocolLabel(log.client_protocol) })
                : undefined}
              content={log.client_response_body}
              isZh={isZh}
            />
          </div>
        </>
      )}
    </Inspector>
  );
}

interface PayloadBlockProps {
  title: string;
  meta?: string;
  content: string | null | undefined;
  isZh: boolean;
}

function PayloadBlock({ title, meta, content, isZh }: PayloadBlockProps) {
  const [copied, setCopied] = useState(false);
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
    <div className="code-output">
      <div className="code-output-bar">
        <span className="code-output-lang">{title}</span>
        {meta ? <span className="code-output-meta">{meta}</span> : null}
        <button type="button" className="button button-sm" disabled={!hasContent} onClick={handleCopy}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          {copied
            ? localizedMessage(isZh, "v2.api-keys.copied")
            : localizedMessage(isZh, "v2.api-keys.copy")}
        </button>
      </div>
      <pre className="code-output-body">
        {hasContent ? pretty : localizedMessage(isZh, "v2.log-detail-dialog.empty")}
      </pre>
    </div>
  );
}
