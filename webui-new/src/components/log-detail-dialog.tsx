import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Check, Copy, Download } from "lucide-react";

import { backend } from "@/lib/backend";
import { useLocale } from "@/lib/i18n";
import type { RequestLog } from "@/lib/types";
import { computeTps, formatDuration, formatLogTime, formatTokenCount, formatTps, generationMsOf, tryPrettyJson } from "@/lib/format";
import { prettyName } from "@/lib/protocol";
import { Drawer } from "@/components/ui/overlays";
import { copyableTipProps } from "@/lib/copyable";
import { localizedMessage } from "@/lib/messages";


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
  // is_stream is the authoritative client-declared flag. Older logs without it fall back to stream_chunks_count.
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
      `# Client Status: ${log.client_status_code ?? "–"}  Upstream Status: ${log.upstream_status_code ?? "–"}`,
      `# Latency Total: ${formatDuration(log.latency_total_ms)}  Upstream: ${formatDuration(log.latency_upstream_ms)}`,
      `# TPS: ${tps != null ? formatTps(tps) : "–"}  (gen ${formatDuration(generationMs)})`,
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
    <Drawer
      open={open}
      wide
      onClose={() => onOpenChange(false)}
      title={(
        <span className="card-title-row">
          {localizedMessage(isZh, "logDetail.requestDetail")}
          {isLoading ? <span className="spinner" aria-label={localizedMessage(isZh, "logDetail.loading")} /> : null}
        </span>
      )}
      description={log ? formatLogTime(log.created_at) : undefined}
    >
      {!log ? (
        <div className="empty">
          <span className="spinner" aria-label={localizedMessage(isZh, "logDetail.loading")} />
        </div>
      ) : (
        <>
          <div className="drawer-meta">
            <span className="badge">{method}</span>
            <code>{path}</code>
            <span className={statusOk ? "status-tag" : "health danger"}>{clientStatus ?? "–"}</span>
            <span className="badge">{isStream ? "SSE" : "JSON"}</span>
            {isCrossProtocol ? (
              <span className="badge">{localizedMessage(isZh, "logDetail.crossProtocol")}</span>
            ) : null}
            {(log.provider_name ?? log.provider_id) ? (
              <span className="tag" {...copyableTipProps(log.provider_name ?? log.provider_id)}>{log.provider_name ?? log.provider_id}</span>
            ) : null}
            {log.model_name ? <span className="tag" {...copyableTipProps(log.model_name)}>{log.model_name}</span> : null}
            {log.api_key_name ? <span className="tag" {...copyableTipProps(log.api_key_name)}>{log.api_key_name}</span> : null}
            {log.upstream_model ? <code {...copyableTipProps(log.upstream_model)}>{log.upstream_model}</code> : null}
            {log.latency_total_ms != null ? <code>{formatDuration(log.latency_total_ms)}</code> : null}
            {tps != null ? (
              <code title={localizedMessage(isZh, "logDetail.netGenerationSpeedExcludesPrefillWait")}>
                {formatTps(tps)}
              </code>
            ) : null}
            <code title={String(log.input_tokens)}>IN {formatTokenCount(log.input_tokens)}</code>
            <code title={String(log.output_tokens)}>OUT {formatTokenCount(log.output_tokens)}</code>
            <button type="button" className="button button-sm" onClick={handleDownload}>
              {downloaded ? <Check aria-hidden="true" /> : <Download aria-hidden="true" />}
              {downloaded ? (localizedMessage(isZh, "common.saved")) : (localizedMessage(isZh, "logDetail.download"))}
            </button>
          </div>

          <div className="section-grid">
            <div>
              <SectionHeader
                title={localizedMessage(isZh, "logDetail.text1ClientRequest")}
                hint={localizedMessage(isZh, "logDetail.protocol", { a: protocolLabel(log.client_protocol) })}
              />
              <div className="section-grid">
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.clientRequestHeaders")}
                  content={log.client_request_headers}
                  isZh={isZh}
                />
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.clientRequestBody")}
                  content={log.client_request_body}
                  isZh={isZh}
                />
              </div>
            </div>

            <div>
              <SectionHeader
                title={localizedMessage(isZh, "logDetail.text2UpstreamRequest")}
                hint={isCrossProtocol
                  ? (localizedMessage(isZh, "logDetail.nyroConverted", { a: protocolLabel(log.upstream_protocol) }))
                  : undefined}
              />
              <div className="section-grid">
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.upstreamRequestHeaders")}
                  content={log.upstream_request_headers}
                  isZh={isZh}
                />
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.upstreamRequestBody")}
                  content={log.upstream_request_body}
                  isZh={isZh}
                />
              </div>
            </div>

            <div>
              <SectionHeader
                title={localizedMessage(isZh, "logDetail.text3UpstreamResponse")}
                hint={localizedMessage(isZh, "logDetail.protocol", { a: protocolLabel(log.upstream_protocol) })}
              />
              <div className="section-grid">
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.upstreamResponseHeaders")}
                  content={log.upstream_response_headers}
                  isZh={isZh}
                />
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.upstreamResponseBody")}
                  content={log.upstream_response_body}
                  isZh={isZh}
                />
              </div>
            </div>

            <div>
              <SectionHeader
                title={localizedMessage(isZh, "logDetail.text4ClientResponse")}
                hint={isCrossProtocol
                  ? (localizedMessage(isZh, "logDetail.nyroConverted", { a: protocolLabel(log.client_protocol) }))
                  : undefined}
              />
              <div className="section-grid">
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.clientResponseHeaders")}
                  content={log.client_response_headers}
                  isZh={isZh}
                />
                <PayloadBlock
                  title={localizedMessage(isZh, "logDetail.clientResponseBody")}
                  content={log.client_response_body}
                  isZh={isZh}
                />
              </div>
            </div>
          </div>
        </>
      )}
    </Drawer>
  );
}

function SectionHeader({ title, hint }: { title: string; hint?: string }) {
  return (
    <h3 className="form-section-title">
      {title}
      {hint ? <span className="code-output-meta"> {hint}</span> : null}
    </h3>
  );
}

interface PayloadBlockProps {
  title: string;
  content: string | null | undefined;
  isZh: boolean;
}

function PayloadBlock({ title, content, isZh }: PayloadBlockProps) {
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
        <button type="button" className="button button-sm" disabled={!hasContent} onClick={handleCopy}>
          {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
          {copied ? (localizedMessage(isZh, "common.copied")) : (localizedMessage(isZh, "common.copy"))}
        </button>
      </div>
      <pre className="code-output-body">
        {hasContent ? pretty : (localizedMessage(isZh, "logDetail.empty"))}
      </pre>
    </div>
  );
}
