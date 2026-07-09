export interface Upstream {
  id: string;
  name: string;
  provider?: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown> | string | null;
  models?: string[] | null;
  models_url?: string;
  proxy_url?: string;
  enabled: boolean;
  created_at?: string;
  updated_at?: string;
}

export interface CreateUpstream {
  name: string;
  provider: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown>;
  models?: string[];
  models_url?: string;
  proxy_url?: string;
  enabled?: boolean;
}

export interface UpdateUpstream {
  name?: string;
  provider?: string;
  protocol?: string;
  base_url?: string;
  credentials?: Record<string, unknown>;
  models?: string[];
  models_url?: string;
  proxy_url?: string;
  enabled?: boolean;
}

export interface RouteUpstream {
  id: string;
  route_id: string;
  upstream_id: string;
  model: string;
  weight: number;
  priority: number;
  enabled: boolean;
  created_at?: string;
}

export type ModelBalance = "weighted" | "priority" | "cooldown" | "latency";

export interface Route {
  id: string;
  model: string;
  balance: ModelBalance;
  enable_auth: boolean;
  enable_payload?: boolean | null;
  enabled: boolean;
  upstreams?: RouteUpstream[];
  created_at?: string;
  updated_at?: string;
}

export interface CreateRouteUpstream {
  upstream_id: string;
  model: string;
  weight?: number;
  priority?: number;
  enabled?: boolean;
}

export interface CreateRoute {
  model: string;
  balance?: ModelBalance;
  enable_auth?: boolean;
  enable_payload?: boolean | null;
  upstreams: CreateRouteUpstream[];
}

export interface UpdateRoute {
  model?: string;
  balance?: ModelBalance;
  enable_auth?: boolean;
  enable_payload?: boolean | null;
  enabled?: boolean;
  upstreams?: CreateRouteUpstream[];
}

export interface ConsumerKey {
  id: string;
  consumer_id: string;
  name: string;
  key_preview: string;
  token?: string;
  enabled: boolean;
  expires_at?: string;
  last_used_at?: string;
  created_at?: string;
  updated_at?: string;
}

export interface ConsumerQuota {
  id?: string;
  consumer_id?: string;
  quota_type: "requests" | "tokens" | "concurrency" | "budget" | string;
  quota_limit: number;
  window?: string;
  /** Set only for "budget" quotas (ISO 4217 code, e.g. "USD"). */
  currency?: string;
}

/** Per-request resource caps; omitted/zero means no limit for that dimension. */
export interface ConsumerLimits {
  max_input_tokens?: number;
  max_output_tokens?: number;
  max_request_body_bytes?: number;
}

export interface Consumer {
  id: string;
  name: string;
  enabled: boolean;
  keys?: ConsumerKey[];
  routes?: string[];
  quotas?: ConsumerQuota[];
  metadata?: Record<string, string>;
  protocols?: string[];
  ip_allowlist?: string[];
  limits?: ConsumerLimits;
  created_at?: string;
  updated_at?: string;
}

export interface CreateConsumerKey {
  name: string;
  token?: string;
  expires_at?: string;
  enabled?: boolean;
}

/** Partial-update DTO for a single consumer key (`PUT /consumers/{id}/keys/{keyId}`); omitted fields mean unchanged. */
export interface UpdateConsumerKey {
  name?: string;
  enabled?: boolean;
  expires_at?: string;
}

export interface CreateConsumerQuota {
  quota_type: "requests" | "tokens" | "concurrency" | "budget" | string;
  quota_limit: number;
  window?: string;
  currency?: string;
}

export interface CreateConsumer {
  name: string;
  enabled?: boolean;
  keys?: CreateConsumerKey[];
  routes?: string[];
  quotas?: CreateConsumerQuota[];
  metadata?: Record<string, string>;
  protocols?: string[];
  ip_allowlist?: string[];
  limits?: ConsumerLimits;
}

export interface UpdateConsumer {
  name?: string;
  enabled?: boolean;
  quotas?: CreateConsumerQuota[];
  routes?: string[];
  metadata?: Record<string, string>;
  protocols?: string[];
  ip_allowlist?: string[];
  limits?: ConsumerLimits;
}

/** Raw Go backend response shape for a provider preset (`GET /api/v1/provider-presets`), snake_case fields. */
export interface ProviderPresetDTO {
  id: string;
  name: string;
  priority: number;
  default_protocol: string;
  default_model?: string;
  protocols: Array<{ id: string; base_url?: string }>;
  credentials: {
    fields: Array<{
      name: string;
      type: string;
      required: boolean;
      default?: string;
      values?: string[];
      env?: string;
      required_when?: Record<string, unknown>;
    }>;
  };
  models_url?: string;
}

export interface RequestLog {
  id: string;
  /** Unix 毫秒时间戳 */
  created_at: number;
  api_key_id?: string;
  api_key_name?: string;

  client_protocol?: string;
  upstream_protocol?: string;
  provider_id?: string;
  provider_name?: string;
  model_id?: string;
  model_name?: string;
  upstream_url?: string;
  client_model?: string;
  upstream_model?: string;

  method?: string;
  path?: string;

  client_request_headers?: string;
  client_request_body?: string;
  client_response_headers?: string;
  client_response_body?: string;

  upstream_request_headers?: string;
  upstream_request_body?: string;
  upstream_response_headers?: string;
  upstream_response_body?: string;

  upstream_status_code?: number;
  client_status_code?: number;

  latency_total_ms?: number;
  latency_upstream_ms?: number;
  input_tokens: number;
  output_tokens: number;

  is_stream: boolean;
  stream_chunks_count: number;
  stream_first_chunk_ms?: number;
}

export function getRouteType(log: Pick<RequestLog, "path">): "chat" | "embedding" {
  return log.path === "/v1/embeddings" ? "embedding" : "chat";
}

export interface LogPage {
  items: RequestLog[];
  total: number;
}

export interface GatewayStatus {
  status: string;
  proxy_port?: number;
  upstream_count?: number;
  route_count?: number;
  consumer_count?: number;
  backend?: string;
  writable?: boolean;
}

/**
 * One gateway (data plane) currently connected to this admin over config-sync.
 * Best-effort, in-memory view (`GET /api/v1/nodes`) — a node disappears the
 * moment its stream drops; nothing here is persisted.
 */
export interface GatewayNode {
  node_id: string;
  hostname: string;
  app_version: string;
  remote_addr: string;
  connected_at: string;
  applied_version: number;
}

export interface StatsOverview {
  total_requests: number;
  total_input_tokens: number;
  total_output_tokens: number;
  avg_duration_ms: number;
  error_count: number;
}

export interface StatsHourly {
  hour: string;
  request_count: number;
  error_count: number;
  total_input_tokens: number;
  total_output_tokens: number;
  avg_duration_ms: number;
}

export interface ModelStats {
  model: string;
  request_count: number;
  total_input_tokens: number;
  total_output_tokens: number;
  avg_duration_ms: number;
}

export interface ProviderStats {
  provider: string;
  request_count: number;
  error_count: number;
  avg_duration_ms: number;
}

export interface ApiKeyStats {
  api_key_id: string;
  api_key_name: string;
  request_count: number;
  total_input_tokens: number;
  total_output_tokens: number;
  cache_read_tokens: number;
  last_used_at: number;
}

export interface TestResult {
  success: boolean;
  latency_ms: number;
  model?: string;
  error?: string;
}

export interface ProviderHealthEvent {
  type: "check" | "complete";
  check?: "config" | "credentials" | "models" | "model_request";
  status?: "running" | "passed" | "failed";
  message?: string;
  model?: string;
  latency_ms?: number;
  status_code?: number;
  error?: string;
  success?: boolean;
}

export interface RouteImportEvent {
  type: "stage" | "route" | "complete";
  stage?: "models" | "creating";
  status?: "running" | "passed" | "failed" | "created" | "skipped";
  message?: string;
  model?: string;
  reason?: string;
  error?: string;
  count?: number;
  success?: boolean;
  discovered?: number;
  created?: number;
  skipped?: number;
  failed?: number;
}

export interface RouteImportPreview {
  discovered: number;
  create: string[];
  skip: string[];
}

export interface ModelCapabilities {
  provider: string;
  model_id: string;
  context_window: number;
  embedding_length?: number | null;
  tool_call: boolean;
  reasoning: boolean;
  input_modalities: string[];
  output_modalities: string[];
}

export type ProviderProtocol =
  | "anthropic-messages"
  | "openai-chat"
  | "openai-responses"
  | "google-gemini";

export interface ProviderChannelPreset {
  id: string;
  baseUrls: Record<string, string>;
  modelsSource?: string;
  apiKey?: string;
  modelsEndpoint?: string;
  staticModels?: string[];
}

/**
 * One field in a provider's credential schema, mirrored from the Go backend's
 * `provider.CredentialField` (see `GET /api/v1/provider-presets`). `type` is
 * one of "string" | "secret" | "enum". `required_when` gates conditional
 * requiredness on the value of another field in the same credentials object
 * (the referenced value may be a single string or a list of acceptable
 * strings).
 */
export interface ProviderCredentialField {
  name: string;
  type: string;
  required: boolean;
  default?: string;
  values?: string[];
  env?: string;
  required_when?: Record<string, unknown>;
}

export interface ProviderPreset {
  id: string;
  name: string;
  icon?: string;
  /** Display order in the vendor quickselect (lower sorts first). */
  priority?: number;
  defaultProtocol: string;
  channels?: ProviderChannelPreset[];
  /** The preset's full credential schema (`credentials.fields[]` from the Go backend). */
  credentialFields?: ProviderCredentialField[];
}

export interface LogQuery {
  limit?: number;
  offset?: number;
  provider?: string;
  model?: string;
  status_min?: number;
  status_max?: number;
  api_key?: string;
}

export interface ExportData {
  version: number;
  providers: ExportProvider[];
  models: ExportModel[];
  settings: [string, string][];
}

export interface ExportProvider {
  name: string;
  vendor?: string | null;
  protocol: string;
  base_url: string;
  use_proxy: boolean;
  preset_key?: string | null;
  channel?: string | null;
  models_source?: string | null;
  static_models?: string | null;
  api_key: string;
  is_enabled: boolean;
}

export interface ExportModel {
  name: string;
  target_model: string;
  enable_auth: boolean;
  enable_payload?: boolean | null;
  is_enabled: boolean;
}

export interface ImportResult {
  providers_imported: number;
  models_imported: number;
  settings_imported: number;
}


export interface OAuthSessionInitData {
  session_id: string;
  vendor: string;
  scheme: string;
  auth_url: string;
  requires_manual_code: boolean;
  user_code: string;
  verification_uri: string;
  verification_uri_complete: string;
  expires_in: number;
  interval: number;
}

export type OAuthSessionStatusData =
  | {
      status: "pending";
      scheme: string;
      auth_url: string;
      requires_manual_code: boolean;
      expires_in: number;
      interval: number;
      user_code: string;
      verification_uri_complete: string;
    }
  | {
      status: "ready";
      expires_in: number;
      resource_url?: string | null;
    }
  | {
      status: "error";
      code: string;
      message: string;
    };

export type ProviderOAuthStatus =
  | "not_connected"
  | "pending"
  | "connected"
  | "unavailable"
  | "quota_exhausted"
  | "error"
  | "disconnected";

export interface ProviderOAuthStatusData {
  provider_id: string;
  provider_name: string;
  driver_key: string;
  status: ProviderOAuthStatus;
  expires_at?: string | null;
  resource_url?: string | null;
  subject_id?: string | null;
  last_error?: string | null;
  updated_at?: string | null;
  has_refresh_token: boolean;
}
