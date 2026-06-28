export function formatDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return "–";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(2)} s`;
  if (ms < 3_600_000) return `${(ms / 60_000).toFixed(1)} m`;
  return `${(ms / 3_600_000).toFixed(1)} h`;
}

export function formatLogTime(ts: number | string | null | undefined): string {
  if (ts == null) return "–";
  const date = typeof ts === "number" ? new Date(ts) : (() => {
    const normalized = ts.includes("T") ? ts : ts.replace(" ", "T") + "Z";
    return new Date(normalized);
  })();
  if (Number.isNaN(date.getTime())) return String(ts);
  const mm = String(date.getMonth() + 1).padStart(2, "0");
  const dd = String(date.getDate()).padStart(2, "0");
  const hh = String(date.getHours()).padStart(2, "0");
  const mi = String(date.getMinutes()).padStart(2, "0");
  const ss = String(date.getSeconds()).padStart(2, "0");
  return `${mm}/${dd} ${hh}:${mi}:${ss}`;
}

export function formatTokenCount(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return "0";
  const n = Math.max(0, Math.floor(value));
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`;
  return `${(n / 1_000_000).toFixed(2)}M`;
}

export function formatTps(tps: number | null | undefined): string {
  if (tps == null || !Number.isFinite(tps) || tps <= 0) return "–";
  if (tps < 100) return `${tps.toFixed(1)} tok/s`;
  return `${Math.round(tps)} tok/s`;
}

/** 计算 TPS 所需的最小字段集(结构兼容 `RequestLog`)。 */
export interface TpsInput {
  output_tokens?: number | null;
  is_stream?: boolean | null;
  stream_chunks_count?: number | null;
  latency_upstream_ms?: number | null;
  latency_total_ms?: number | null;
  stream_first_chunk_ms?: number | null;
}

/**
 * 净生成耗时(ms):流式 = 上游耗时 − 首字节延迟;非流式 = 上游往返耗时;
 * 缺失时回退到端到端总耗时。无法确定时返回 null。
 */
export function generationMsOf(log: TpsInput | null | undefined): number | null {
  if (!log) return null;
  const isStream = log.is_stream ?? (log.stream_chunks_count ?? 0) > 0;
  const upstream = log.latency_upstream_ms ?? null;
  const ttfb = log.stream_first_chunk_ms ?? null;
  if (isStream && upstream != null && ttfb != null) {
    const gen = upstream - ttfb;
    return gen > 0 ? gen : null;
  }
  return upstream ?? log.latency_total_ms ?? null;
}

/** 净生成速度(tok/s);output ≤ 0 或净生成耗时无效时返回 null。 */
export function computeTps(log: TpsInput | null | undefined): number | null {
  const gen = generationMsOf(log);
  const out = log?.output_tokens ?? 0;
  if (out > 0 && gen && gen > 0) return out / (gen / 1000);
  return null;
}

export function tryPrettyJson(raw: string | null | undefined): string {
  if (raw == null) return "";
  if (typeof raw !== "string") {
    try {
      return JSON.stringify(raw, null, 2);
    } catch {
      return String(raw);
    }
  }
  const trimmed = raw.trim();
  if (!trimmed) return raw;
  try {
    const parsed = JSON.parse(trimmed);
    return JSON.stringify(parsed, null, 2);
  } catch {
    return raw;
  }
}
