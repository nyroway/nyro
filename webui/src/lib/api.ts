const API_BASE = "/nyro";

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${API_BASE}${path}`, {
    headers: { "Content-Type": "application/json", ...init?.headers },
    ...init,
  });
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.message || `HTTP ${res.status}`);
  }
  return res.json();
}

function ensureArray<T>(value: unknown): T[] {
  if (Array.isArray(value)) return value as T[];
  if (value && typeof value === "object") {
    return Object.values(value as Record<string, T>);
  }
  return [];
}

/* ─── Types ─── */

export interface ApiResponse<T> {
  code: number;
  data: T;
  message?: string;
}

export interface ListResponse<T> {
  total: number;
  items: T[];
}

export interface GatewayStatus {
  version: string;
  store_mode: string;
  config_version: number;
  uptime_seconds: number;
  worker_count: number;
  worker_id: number;
  nginx_version: string;
}

export interface MetricsDimension {
  name: string;
  requests: number;
  latency_avg_ms?: number;
  input_tokens: number;
  output_tokens: number;
  status: { "2xx": number; "4xx": number; "5xx": number };
}

export interface Metrics {
  uptime_seconds: number;
  total_requests: number;
  total_input_tokens: number;
  total_output_tokens: number;
  active_connections: number;
  routes: MetricsDimension[];
  services: MetricsDimension[];
  consumers: MetricsDimension[];
  models: MetricsDimension[];
}

export interface LogEntry {
  timestamp: string;
  client_ip: string;
  method: string;
  uri: string;
  status: number;
  latency_ms: number;
  request_length: number;
  response_length: number;
  upstream_addr: string;
  upstream_status: string;
  request_id: string;
  route: string;
  service: string;
  consumer: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
}

export interface Route {
  name: string;
  service: string;
  paths: string[];
  methods?: string[];
  match_type?: string;
  hosts?: string[];
  headers?: Record<string, string>;
  priority?: number;
  plugins?: PluginConfig[];
}

export interface Service {
  name: string;
  url?: string;
  backend?: string;
  provider?: string;
  scheme?: string;
  plugins?: PluginConfig[];
}

export interface Backend {
  name: string;
  endpoints: Endpoint[];
  algorithm?: string;
  timeout?: { connect?: number; send?: number; read?: number };
  retries?: number;
}

export interface Endpoint {
  address: string;
  port?: number;
  weight?: number;
  headers?: Record<string, string>;
}

export interface Consumer {
  name: string;
  credentials: Record<string, unknown>;
  plugins?: PluginConfig[];
}

export interface PluginConfig {
  id?: string;
  name?: string;
  config?: Record<string, unknown>;
}

export interface Certificate {
  name: string;
  snis: string[];
  cert?: string;
  cert_file?: string;
  key?: string;
  key_file?: string;
}

/* ─── Admin API ─── */

export const api = {
  // Status
  getStatus: () =>
    request<ApiResponse<GatewayStatus>>("/admin/status"),

  getConfigVersion: () =>
    request<ApiResponse<{ version: number }>>("/admin/config/version"),

  reloadConfig: () =>
    request<ApiResponse<{ version: number }>>("/admin/config/reload", { method: "POST" }),

  // CRUD factory
  list: async <T>(resource: string) => {
    const raw = await request<ApiResponse<{ total?: number; items?: unknown }>>(
      `/admin/${resource}`,
    );
    return {
      ...raw,
      data: {
        total: Number(raw?.data?.total || 0),
        items: ensureArray<T>(raw?.data?.items),
      },
    } as ApiResponse<ListResponse<T>>;
  },

  get: <T>(resource: string, name: string) =>
    request<ApiResponse<T>>(`/admin/${resource}/${name}`),

  create: <T>(resource: string, body: T) =>
    request<ApiResponse<T>>(`/admin/${resource}`, {
      method: "POST",
      body: JSON.stringify(body),
    }),

  update: <T>(resource: string, name: string, body: T) =>
    request<ApiResponse<T>>(`/admin/${resource}/${name}`, {
      method: "PUT",
      body: JSON.stringify(body),
    }),

  remove: (resource: string, name: string) =>
    request<ApiResponse<null>>(`/admin/${resource}/${name}`, { method: "DELETE" }),

  // Observability
  getMetrics: async () => {
    const raw = await request<Metrics & Record<string, unknown>>("/local/metrics");
    return {
      ...raw,
      routes: ensureArray<MetricsDimension>(raw.routes),
      services: ensureArray<MetricsDimension>(raw.services),
      consumers: ensureArray<MetricsDimension>(raw.consumers),
      models: ensureArray<MetricsDimension>(raw.models),
    } as Metrics;
  },

  getLogs: async (limit = 50) => {
    const raw = await request<{ total: number; items: unknown }>(`/local/logs?limit=${limit}`);
    return {
      total: raw.total || 0,
      items: ensureArray<LogEntry>(raw.items),
    };
  },
};
