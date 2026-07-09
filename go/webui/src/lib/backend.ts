import { getAdminToken, clearAdminToken } from "./auth";
import type {
  CreateConsumer,
  CreateConsumerKey,
  CreateRoute,
  CreateUpstream,
  ProviderHealthEvent,
  RouteImportEvent,
  RouteImportPreview,
  UpdateConsumer,
  UpdateConsumerKey,
  UpdateRoute,
  UpdateUpstream,
} from "./types";

const IS_TAURI = typeof window !== "undefined" && "__TAURI_INTERNALS__" in window;

async function invokeIPC<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const { invoke } = await import("@tauri-apps/api/core");
  return invoke<T>(cmd, args);
}

async function invokeHTTP<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
  const mapping = resolveHTTP(cmd, args);
  const init: RequestInit = { method: mapping.method };

  const headers: Record<string, string> = {};
  const token = getAdminToken();
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  if (mapping.body) {
    headers["Content-Type"] = "application/json";
    init.body = JSON.stringify(mapping.body);
  }
  if (Object.keys(headers).length > 0) {
    init.headers = headers;
  }

  const resp = await fetch(mapping.url, init);

  if (resp.status === 401 && window.location.pathname !== "/login") {
    clearAdminToken();
    window.location.replace("/login");
    throw new Error("Authentication required");
  }

  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${resp.status}`);
  }
  const text = await resp.text();
  if (!text) return {} as T;
  const json = JSON.parse(text);
  if (json && typeof json === "object" && "error" in json) {
    const errorMessage =
      typeof json.error === "string" && json.error.trim()
        ? json.error
        : `Request failed: ${cmd}`;
    throw new Error(errorMessage);
  }
  // Use explicit key check instead of ??, so that { "data": null } correctly
  // returns null rather than falling back to the full response object.
  const value = json && typeof json === "object" && "data" in json ? json.data : json;
  return mapping.transform ? (mapping.transform(value) as T) : (value as T);
}

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

async function streamProviderHealthEvents(
  url: string,
  init: RequestInit,
  onEvent: (event: ProviderHealthEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = {
    ...(init.headers as Record<string, string> | undefined),
  };
  const token = getAdminToken();
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  const resp = await fetch(url, {
    method: "POST",
    ...init,
    headers,
    signal,
  });

  if (resp.status === 401 && window.location.pathname !== "/login") {
    clearAdminToken();
    window.location.replace("/login");
    throw new Error("Authentication required");
  }
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${resp.status}`);
  }
  if (!resp.body) {
    throw new Error("Streaming response body is not available");
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
      const event = decodeProviderHealthSSEFrame(frame);
      if (event) onEvent(event);
      boundary = buffer.indexOf("\n\n");
    }
  }
  buffer += decoder.decode();
  const tail = buffer.trim();
  if (tail) {
    const event = decodeProviderHealthSSEFrame(tail);
    if (event) onEvent(event);
  }
}

async function streamRouteImportEvents(
  url: string,
  onEvent: (event: RouteImportEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = {};
  const token = getAdminToken();
  if (token) {
    headers["Authorization"] = `Bearer ${token}`;
  }
  const resp = await fetch(url, {
    method: "POST",
    headers,
    signal,
  });

  if (resp.status === 401 && window.location.pathname !== "/login") {
    clearAdminToken();
    window.location.replace("/login");
    throw new Error("Authentication required");
  }
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw new Error(body.error || `HTTP ${resp.status}`);
  }
  if (!resp.body) {
    throw new Error("Streaming response body is not available");
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
      const event = decodeRouteImportSSEFrame(frame);
      if (event) onEvent(event);
      boundary = buffer.indexOf("\n\n");
    }
  }
  buffer += decoder.decode();
  const tail = buffer.trim();
  if (tail) {
    const event = decodeRouteImportSSEFrame(tail);
    if (event) onEvent(event);
  }
}

export async function streamProviderDraftHealth(
  input: CreateUpstream,
  onEvent: (event: ProviderHealthEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  await streamProviderHealthEvents(
    "/api/v1/upstreams/test-draft/stream",
    {
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
    onEvent,
    signal,
  );
}

export async function streamProviderEditDraftHealth(
  id: string,
  input: CreateUpstream,
  onEvent: (event: ProviderHealthEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  await streamProviderHealthEvents(
    `/api/v1/upstreams/${id}/test-draft/stream`,
    {
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    },
    onEvent,
    signal,
  );
}

export async function streamProviderHealth(
  id: string,
  onEvent: (event: ProviderHealthEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  await streamProviderHealthEvents(
    `/api/v1/upstreams/${id}/test`,
    {},
    onEvent,
    signal,
  );
}

export async function streamProviderRouteImport(
  id: string,
  onEvent: (event: RouteImportEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  await streamRouteImportEvents(
    `/api/v1/upstreams/${id}/routes/import/stream`,
    onEvent,
    signal,
  );
}

interface HTTPMapping {
  method: string;
  url: string;
  body?: unknown;
  transform?: (value: unknown) => unknown;
}

function resolveHTTP(cmd: string, args?: Record<string, unknown>): HTTPMapping {
  const base = "/api/v1";
  switch (cmd) {
    case "list_upstreams":
      return { method: "GET", url: `${base}/upstreams` };
    case "get_provider_presets":
      return { method: "GET", url: `${base}/provider-presets` };
    case "create_upstream":
      return {
        method: "POST",
        url: `${base}/upstreams`,
        body: args?.input as CreateUpstream,
      };
    case "update_upstream":
      return {
        method: "PUT",
        url: `${base}/upstreams/${args?.id}`,
        body: args?.input as UpdateUpstream,
      };
    case "delete_upstream":
      return { method: "DELETE", url: `${base}/upstreams/${args?.id}` };
    case "test_provider_models":
    case "get_model_capabilities":
    case "get_provider_oauth_status":
    case "reconnect_provider_oauth":
    case "logout_provider_oauth":
      throw new Error("This provider workflow is not available in the Go WebUI yet.");
    case "get_provider_models":
      return {
        method: "GET",
        url: `${base}/upstreams/${args?.id}/models`,
        transform: (value) => (value as { models?: string[] }).models ?? [],
      };
    case "preview_provider_route_import":
      return {
        method: "GET",
        url: `${base}/upstreams/${args?.id}/routes/import/preview`,
        transform: (value) => value as RouteImportPreview,
      };
    case "init_oauth_session":
    case "get_oauth_session_status":
    case "cancel_oauth_session":
    case "complete_oauth_session":
    case "create_oauth_provider":
      throw new Error("OAuth workflows are not available in the Go WebUI yet.");
    case "list_routes":
      return { method: "GET", url: `${base}/routes` };
    case "create_route":
      return {
        method: "POST",
        url: `${base}/routes`,
        body: args?.input as CreateRoute,
      };
    case "update_route":
      return {
        method: "PUT",
        url: `${base}/routes/${args?.id}`,
        body: args?.input as UpdateRoute,
      };
    case "delete_route":
      return { method: "DELETE", url: `${base}/routes/${args?.id}` };

    case "list_consumers":
      return { method: "GET", url: `${base}/consumers` };
    case "create_consumer":
      return {
        method: "POST",
        url: `${base}/consumers`,
        body: args?.input as CreateConsumer,
      };
    case "update_consumer":
      return {
        method: "PUT",
        url: `${base}/consumers/${args?.id}`,
        body: args?.input as UpdateConsumer,
      };
    case "delete_consumer":
      return { method: "DELETE", url: `${base}/consumers/${args?.id}` };
    case "add_consumer_key":
      return {
        method: "POST",
        url: `${base}/consumers/${args?.id}/keys`,
        body: args?.input as CreateConsumerKey,
      };
    case "update_consumer_key":
      return {
        method: "PUT",
        url: `${base}/consumers/${args?.id}/keys/${args?.keyId}`,
        body: args?.input as UpdateConsumerKey,
      };
    case "delete_consumer_key":
      return { method: "DELETE", url: `${base}/consumers/${args?.id}/keys/${args?.keyId}` };

    case "query_logs": {
      const q = (args?.query ?? {}) as Record<string, unknown>;
      const params = new URLSearchParams();
      for (const [k, v] of Object.entries(q)) {
        if (v != null) params.set(k, String(v));
      }
      const qs = params.toString();
      return { method: "GET", url: `${base}/logs${qs ? "?" + qs : ""}` };
    }
    case "get_log":
      return { method: "GET", url: `${base}/logs/${args?.id}` };
    case "clear_logs":
      return { method: "DELETE", url: `${base}/logs` };

    case "get_stats_overview": {
      const hours = args?.hours;
      return {
        method: "GET",
        url: `${base}/stats/overview${hours != null ? `?hours=${hours}` : ""}`,
      };
    }
    case "get_stats_hourly": {
      const hours = args?.hours ?? 24;
      return { method: "GET", url: `${base}/stats/hourly?hours=${hours}` };
    }
    case "get_stats_by_model": {
      const hours = args?.hours;
      return {
        method: "GET",
        url: `${base}/stats/models${hours != null ? `?hours=${hours}` : ""}`,
      };
    }
    case "get_stats_by_provider": {
      const hours = args?.hours;
      return {
        method: "GET",
        url: `${base}/stats/providers${hours != null ? `?hours=${hours}` : ""}`,
      };
    }
    case "get_stats_by_api_key": {
      const hours = args?.hours;
      return {
        method: "GET",
        url: `${base}/stats/api-keys${hours != null ? `?hours=${hours}` : ""}`,
      };
    }

    case "get_setting":
      return {
        method: "GET",
        url: `${base}/settings/${args?.key}`,
        transform: (value) => (value as { value?: string | null }).value ?? null,
      };
    case "set_setting":
      return {
        method: "PUT",
        url: `${base}/settings/${args?.key}`,
        body: { value: args?.value },
        transform: (value) => (value as { value?: string | null }).value ?? null,
      };

    case "get_gateway_status":
      return { method: "GET", url: `${base}/status` };

    case "list_nodes":
      return { method: "GET", url: `${base}/nodes` };

    case "export_config":
    case "import_config":
      throw new Error("Config import/export is not available in the Go WebUI yet.");

    default:
      return { method: "POST", url: `${base}/${cmd}`, body: args };
  }
}

export const backend = IS_TAURI ? invokeIPC : invokeHTTP;
export { IS_TAURI };
