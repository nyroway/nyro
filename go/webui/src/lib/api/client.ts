// Typed HTTP client for the Go admin API (/api/v1).
// Phase 0 of the nyro transformation
// The legacy command dispatcher (lib/backend.ts) was removed in Phase 3,
// after the last callers moved to the typed api modules. Conventions:
//   - success: raw JSON, or a {"data": ...} envelope unwrapped transparently
//   - failure: {"error": "..."} or {"error": {"message": "...", "type": "CODE"}}

import type { ProviderHealthEvent, RouteImportEvent } from "@/lib/types";

export type QueryParams = Record<string, string | number | boolean | null | undefined>;

export class BackendError extends Error {
  readonly status: number;
  readonly code?: string;

  constructor(message: string, status: number, code?: string) {
    super(message);
    this.name = "BackendError";
    this.status = status;
    this.code = code;
  }
}

const BASE = "/api/v1";

type RequestOptions = {
  body?: unknown;
  params?: QueryParams;
  signal?: AbortSignal;
};

function buildURL(path: string, params?: QueryParams): string {
  const search = new URLSearchParams();
  if (params) {
    for (const [key, value] of Object.entries(params)) {
      if (value != null) search.set(key, String(value));
    }
  }
  const qs = search.toString();
  return `${BASE}${path}${qs ? `?${qs}` : ""}`;
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

// The admin API writes two error shapes: inline string errors
// ({"error": "upstream not found"}) and webutil.Error envelopes
// ({"error": {"message": "...", "type": "NAME_CONFLICT"}}).
function toBackendError(payload: unknown, status: number): BackendError {
  if (payload && typeof payload === "object") {
    const error = (payload as { error?: unknown }).error;
    if (typeof error === "string" && error.trim()) {
      return new BackendError(error, status);
    }
    if (error && typeof error === "object") {
      const { message, type } = error as { message?: unknown; type?: unknown };
      const text = typeof message === "string" && message.trim() ? message : `HTTP ${status}`;
      return new BackendError(text, status, typeof type === "string" ? type : undefined);
    }
  }
  return new BackendError(`HTTP ${status}`, status);
}

export async function request<T>(
  method: string,
  path: string,
  options: RequestOptions = {},
): Promise<T> {
  const init: RequestInit = { method, signal: options.signal };
  if (options.body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(options.body);
  }

  const resp = await fetch(buildURL(path, options.params), init);
  const text = await resp.text();
  if (!resp.ok) {
    throw toBackendError(safeJSON(text), resp.status);
  }
  if (!text) return {} as T;
  const json: unknown = JSON.parse(text);
  if (json && typeof json === "object" && "error" in json) {
    throw toBackendError(json, resp.status);
  }
  // Explicit key check so that {"data": null} resolves to null instead of the envelope.
  if (json && typeof json === "object" && "data" in json) {
    return (json as { data: T }).data;
  }
  return json as T;
}

export function get<T>(path: string, params?: QueryParams, signal?: AbortSignal): Promise<T> {
  return request<T>("GET", path, { params, signal });
}

export function post<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return request<T>("POST", path, { body, signal });
}

export function put<T>(path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  return request<T>("PUT", path, { body, signal });
}

export function del<T>(path: string, signal?: AbortSignal): Promise<T> {
  return request<T>("DELETE", path, { signal });
}

// --- Server-sent events -----------------------------------------------------

function decodeSSEDataFrame<T>(frame: string): T | null {
  const data = frame
    .split(/\r?\n/)
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice(5).trimStart())
    .join("\n")
    .trim();
  if (!data) return null;
  return JSON.parse(data) as T;
}

export function decodeProviderHealthSSEFrame(frame: string): ProviderHealthEvent | null {
  return decodeSSEDataFrame<ProviderHealthEvent>(frame);
}

export function decodeRouteImportSSEFrame(frame: string): RouteImportEvent | null {
  return decodeSSEDataFrame<RouteImportEvent>(frame);
}

export type SSEDecoder<T> = (frame: string) => T | null;
export type StreamOptions = {
  body?: unknown;
  signal?: AbortSignal;
};

export async function streamSSE<T>(
  path: string,
  options: StreamOptions,
  decode: SSEDecoder<T>,
  onEvent: (event: T) => void,
): Promise<void> {
  const init: RequestInit = { method: "POST", signal: options.signal };
  if (options.body !== undefined) {
    init.headers = { "Content-Type": "application/json" };
    init.body = JSON.stringify(options.body);
  }

  const resp = await fetch(`${BASE}${path}`, init);
  if (!resp.ok) {
    const text = await resp.text().catch(() => "");
    throw toBackendError(safeJSON(text), resp.status);
  }
  if (!resp.body) {
    throw new BackendError("Streaming response body is not available", resp.status);
  }

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  for (;;) {
    const { value, done } = await reader.read();
    if (done) break;
    buffer += decoder.decode(value, { stream: true });
    let boundary = buffer.indexOf("\n\n");
    while (boundary >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const event = decode(frame);
      if (event) onEvent(event);
      boundary = buffer.indexOf("\n\n");
    }
  }
  buffer += decoder.decode();
  const tail = buffer.trim();
  if (tail) {
    const event = decode(tail);
    if (event) onEvent(event);
  }
}
