export interface Provider {
  id: string;
  name: string;
  vendor?: string | null;
  protocol: string;
  base_url: string;
  api_key?: string;
  use_proxy: boolean;
  auth_mode?: "apikey" | "oauth";
  oauth_status?: ProviderOAuthStatus;
  oauth_expires_at?: string | null;
  oauth_last_error?: string | null;
  oauth_updated_at?: string | null;
  preset_key?: string | null;
  channel?: string | null;
  models_source?: string | null;
  static_models?: string | null;
  is_enabled: boolean;
  created_at: string;
  updated_at: string;
}

export interface Model {
  id: string;
  name: string;
  virtual_model: string;
  balance: ModelBalance;
  target_provider: string;
  target_model: string;
  access_control: boolean;
  is_enabled: boolean;
  created_at: string;
  targets: ModelBackend[];
}

export type ModelBalance = "weighted" | "priority";

export interface ModelBackend {
  id: string;
  model_id: string;
  provider_id: string;
  model: string;
  weight: number;
  priority: number;
  created_at: string;
}

export interface ApiKey {
  id: string;
  key: string;
  name: string;
  rpm?: number | null;
  rpd?: number | null;
  tpm?: number | null;
  tpd?: number | null;
  is_enabled: boolean;
  expires_at?: string | null;
  created_at: string;
  updated_at: string;
  model_ids: string[];
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
  proxy_port: number;
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

export interface TestResult {
  success: boolean;
  latency_ms: number;
  model?: string;
  error?: string;
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
  | "openai-compatible"
  | "openai-responses"
  | "anthropic-messages"
  | "google-gemini";

export interface ProviderChannelPreset {
  id: string;
  label: {
    zh: string;
    en: string;
  };
  authMode?: "apikey" | "oauth";
  baseUrls: Record<string, string>;
  modelsSource?: string;
  apiKey?: string;
  modelsEndpoint?: string;
  staticModels?: string[];
}

export interface ProviderPreset {
  id: string;
  label: {
    zh: string;
    en: string;
  };
  icon?: string;
  defaultProtocol: string;
  channels?: ProviderChannelPreset[];
}

export interface CreateProvider {
  name: string;
  vendor?: string;
  protocol: string;
  base_url: string;
  use_proxy?: boolean;
  auth_mode?: "apikey" | "oauth";
  preset_key?: string;
  channel?: string;
  models_source?: string;
  static_models?: string;
  api_key: string;
}

export interface UpdateProvider {
  name?: string;
  vendor?: string;
  protocol?: string;
  base_url?: string;
  use_proxy?: boolean;
  auth_mode?: "apikey" | "oauth";
  preset_key?: string;
  channel?: string;
  models_source?: string;
  static_models?: string;
  api_key?: string;
  is_enabled?: boolean;
}

export interface CreateModel {
  name: string;
  virtual_model: string;
  balance?: ModelBalance;
  target_provider: string;
  target_model: string;
  targets?: CreateModelBackend[];
  access_control?: boolean;
}

export interface UpdateModel {
  name?: string;
  virtual_model?: string;
  balance?: ModelBalance;
  target_provider?: string;
  target_model?: string;
  targets?: UpsertModelBackend[];
  access_control?: boolean;
  is_enabled?: boolean;
}

export interface CreateModelBackend {
  provider_id: string;
  model: string;
  weight?: number;
  priority?: number;
}

export interface UpsertModelBackend {
  id?: string;
  provider_id: string;
  model: string;
  weight?: number;
  priority?: number;
}

export interface CreateApiKey {
  name: string;
  rpm?: number;
  rpd?: number;
  tpm?: number;
  tpd?: number;
  expires_at?: string;
  model_ids: string[];
}

export interface UpdateApiKey {
  name?: string;
  rpm?: number;
  rpd?: number;
  tpm?: number;
  tpd?: number;
  is_enabled?: boolean;
  expires_at?: string;
  model_ids?: string[];
}

export interface LogQuery {
  limit?: number;
  offset?: number;
  provider?: string;
  status_min?: number;
  status_max?: number;
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
  virtual_model: string;
  target_model: string;
  access_control: boolean;
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
