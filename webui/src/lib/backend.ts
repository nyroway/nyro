import { getAdminToken, clearAdminToken } from "./auth";

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
  return json && typeof json === "object" && "data" in json ? json.data : json;
}

interface HTTPMapping {
  method: string;
  url: string;
  body?: Record<string, unknown>;
}

function resolveHTTP(cmd: string, args?: Record<string, unknown>): HTTPMapping {
  const base = "/api/v1";
  switch (cmd) {
    case "get_providers":
      return { method: "GET", url: `${base}/providers` };
    case "get_provider_presets":
      return { method: "GET", url: `${base}/providers/presets` };
    case "create_provider":
      return { method: "POST", url: `${base}/providers`, body: args?.input as Record<string, unknown> };
    case "copy_provider":
      return { method: "POST", url: `${base}/providers/${args?.id}/copy` };
    case "update_provider":
      return { method: "PUT", url: `${base}/providers/${args?.id}`, body: args?.input as Record<string, unknown> };
    case "delete_provider":
      return { method: "DELETE", url: `${base}/providers/${args?.id}` };
    case "test_provider":
      return { method: "GET", url: `${base}/providers/${args?.id}/test` };
    case "test_provider_models":
      return { method: "GET", url: `${base}/providers/${args?.id}/test-models` };
    case "get_provider_models":
      return { method: "GET", url: `${base}/providers/${args?.id}/models` };
    case "get_model_capabilities":
      return {
        method: "GET",
        url: `${base}/providers/${args?.providerId}/model-capabilities?model=${encodeURIComponent(String(args?.model ?? ""))}`,
      };
    case "get_provider_oauth_status":
      return { method: "GET", url: `${base}/providers/${args?.id}/oauth/status` };
    case "reconnect_provider_oauth":
      return { method: "POST", url: `${base}/providers/${args?.id}/oauth/reconnect` };
    case "logout_provider_oauth":
      return { method: "POST", url: `${base}/providers/${args?.id}/oauth/logout` };
    case "init_oauth_session":
      return {
        method: "POST",
        url: `${base}/oauth/sessions/init`,
        body: {
          vendor: args?.vendor,
          use_proxy: args?.useProxy ?? args?.use_proxy,
        },
      };
    case "get_oauth_session_status":
      return { method: "GET", url: `${base}/oauth/sessions/${args?.sessionId ?? args?.session_id}/status` };
    case "cancel_oauth_session":
      return { method: "POST", url: `${base}/oauth/sessions/${args?.sessionId ?? args?.session_id}/cancel` };
    case "complete_oauth_session":
      return {
        method: "POST",
        url: `${base}/oauth/sessions/${args?.sessionId ?? args?.session_id}/complete`,
        body: {
          code: args?.code,
          callback_url: args?.callbackUrl ?? args?.callback_url,
          metadata: args?.metadata ?? {},
        },
      };
    case "create_oauth_provider":
      return {
        method: "POST",
        url: `${base}/providers/oauth`,
        body: {
          session_id: args?.sessionId ?? args?.session_id,
          input: args?.input,
        },
      };
    case "list_routes":
      return { method: "GET", url: `${base}/routes` };
    case "create_route":
      return { method: "POST", url: `${base}/routes`, body: args?.input as Record<string, unknown> };
    case "update_route":
      return { method: "PUT", url: `${base}/routes/${args?.id}`, body: args?.input as Record<string, unknown> };
    case "delete_route":
      return { method: "DELETE", url: `${base}/routes/${args?.id}` };

    case "list_api_keys":
      return { method: "GET", url: `${base}/api-keys` };
    case "create_api_key":
      return { method: "POST", url: `${base}/api-keys`, body: args?.input as Record<string, unknown> };
    case "update_api_key":
      return { method: "PUT", url: `${base}/api-keys/${args?.id}`, body: args?.input as Record<string, unknown> };
    case "delete_api_key":
      return { method: "DELETE", url: `${base}/api-keys/${args?.id}` };

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

    case "get_setting":
      return { method: "GET", url: `${base}/settings/${args?.key}` };
    case "set_setting":
      return { method: "PUT", url: `${base}/settings/${args?.key}`, body: { value: args?.value } };

    case "get_cache_settings":
      return { method: "GET", url: `${base}/cache/settings` };
    case "update_cache_settings":
      return { method: "PUT", url: `${base}/cache/settings`, body: args?.input as Record<string, unknown> };
    case "detect_embedding_dimensions":
      return {
        method: "GET",
        url: `${base}/cache/embedding-dimensions?embedding_route=${encodeURIComponent(String(args?.embeddingRoute ?? ""))}`,
      };
    case "flush_cache":
      return { method: "POST", url: `${base}/cache/flush` };
    case "delete_cache_key":
      return { method: "DELETE", url: `${base}/cache/${encodeURIComponent(String(args?.key ?? ""))}` };
    case "get_cache_stats":
      return { method: "GET", url: `${base}/cache/stats` };

    case "get_gateway_status":
      return { method: "GET", url: `${base}/status` };

    case "export_config":
      return { method: "GET", url: `${base}/config/export` };
    case "import_config":
      return { method: "POST", url: `${base}/config/import`, body: args?.data as Record<string, unknown> };

    default:
      return { method: "POST", url: `${base}/${cmd}`, body: args };
  }
}

export const backend = IS_TAURI ? invokeIPC : invokeHTTP;
export { IS_TAURI };
