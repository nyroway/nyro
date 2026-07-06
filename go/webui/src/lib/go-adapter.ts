import type {
  ApiKey,
  CreateApiKey,
  CreateModel,
  CreateModelBackend,
  CreateProvider,
  Model,
  ModelBackend,
  Provider,
  ProviderChannelPreset,
  ProviderPreset,
  UpdateApiKey,
  UpdateModel,
  UpdateProvider,
  UpsertModelBackend,
} from "./types";
import type {
  GoConsumer,
  GoConsumerQuota,
  GoCreateConsumer,
  GoCreateConsumerQuota,
  GoCreateRoute,
  GoCreateRouteUpstream,
  GoCreateUpstream,
  GoProviderPreset,
  GoRoute,
  GoRouteUpstream,
  GoUpdateConsumer,
  GoUpdateRoute,
  GoUpdateUpstream,
  GoUpstream,
} from "./go-schema";

function parseJSONRecord(value: unknown): Record<string, unknown> {
  if (!value) return {};
  if (typeof value === "object") return value as Record<string, unknown>;
  if (typeof value !== "string") return {};
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function stringValue(value: unknown): string | undefined {
  return typeof value === "string" && value.trim() ? value : undefined;
}

function apiKeyFromCredentials(value: unknown): string {
  return stringValue(parseJSONRecord(value).api_key) ?? "";
}

// credentialsRecord flattens an upstream's opaque credentials JSON blob into a
// string-keyed record for editing in the WebUI's dynamic credential-field
// form. Non-string values (should not normally occur) are stringified rather
// than dropped, so round-tripping through the form never silently loses data.
function credentialsRecord(value: unknown): Record<string, string> {
  const parsed = parseJSONRecord(value);
  const out: Record<string, string> = {};
  for (const [key, raw] of Object.entries(parsed)) {
    if (typeof raw === "string") out[key] = raw;
    else if (raw != null) out[key] = String(raw);
  }
  return out;
}

function routeTargetFromModelBackend(target: CreateModelBackend | UpsertModelBackend): GoCreateRouteUpstream {
  return {
    upstream_id: target.provider_id,
    model: target.model,
    weight: target.weight,
    priority: target.priority,
    enabled: true,
  };
}

function quota(
  type: "requests" | "tokens" | "concurrency",
  limit: number | undefined,
  window?: string,
): GoCreateConsumerQuota | undefined {
  if (limit == null || Number.isNaN(limit)) return undefined;
  return { quota_type: type, quota_limit: limit, window };
}

function quotasFromApiKey(
  input: Pick<CreateApiKey | UpdateApiKey, "rpm" | "rpd" | "tpm" | "tpd" | "max_requests">,
): GoCreateConsumerQuota[] {
  return [
    quota("requests", input.rpm, "1m"),
    quota("requests", input.rpd, "24h"),
    quota("tokens", input.tpm, "1m"),
    quota("tokens", input.tpd, "24h"),
    quota("concurrency", input.max_requests, undefined),
  ].filter((item): item is GoCreateConsumerQuota => Boolean(item));
}

// quotaValue looks up a quota's limit by (type, window). `window` is
// normalized through `?? undefined` on both sides so that a concurrency
// quota's NULL/absent window (serialized as either `undefined` or JSON
// `null`) matches the lookup key `undefined` consistently.
function quotaValue(quotas: GoConsumerQuota[], type: string, window: string | undefined): number | null {
  return quotas.find((item) => item.quota_type === type && (item.window ?? undefined) === window)?.quota_limit ?? null;
}

export function providerFromUpstream(upstream: GoUpstream): Provider {
  return {
    id: upstream.id,
    name: upstream.name,
    provider: upstream.provider ?? null,
    protocol: upstream.protocol ?? "",
    base_url: upstream.base_url ?? "",
    api_key: apiKeyFromCredentials(upstream.credentials),
    credentials: credentialsRecord(upstream.credentials),
    proxy_url: upstream.proxy_url ?? "",
    use_proxy: Boolean(upstream.proxy_url),
    models_url: upstream.models_url ?? null,
    models: upstream.models?.length ? upstream.models.join("\n") : null,
    is_enabled: upstream.enabled,
    created_at: upstream.created_at ?? "",
    updated_at: upstream.updated_at ?? "",
  };
}

function modelsArrayFromText(text?: string): string[] | undefined {
  if (!text) return undefined;
  const lines = text.split("\n").map((line) => line.trim()).filter(Boolean);
  return lines.length ? lines : undefined;
}

export function createUpstreamFromProvider(input: CreateProvider): GoCreateUpstream {
  const credentials =
    input.credentials && Object.keys(input.credentials).length > 0
      ? input.credentials
      : { api_key: input.api_key };
  return {
    name: input.name,
    provider: input.provider ?? "custom",
    protocol: input.protocol,
    base_url: input.base_url,
    credentials,
    models: modelsArrayFromText(input.models ?? undefined),
    models_url: input.models_url || undefined,
    proxy_url: input.proxy_url?.trim() ?? "",
    enabled: true,
  };
}

export function updateUpstreamFromProvider(input: UpdateProvider): GoUpdateUpstream {
  const out: GoUpdateUpstream = {};
  if (input.name !== undefined) out.name = input.name;
  if (input.provider !== undefined) out.provider = input.provider ?? undefined;
  if (input.protocol !== undefined) out.protocol = input.protocol;
  if (input.base_url !== undefined) out.base_url = input.base_url;
  if (input.credentials !== undefined) {
    out.credentials = input.credentials;
  } else if (input.api_key !== undefined) {
    out.credentials = { api_key: input.api_key };
  }
  if (input.proxy_url !== undefined) out.proxy_url = input.proxy_url.trim();
  if (input.is_enabled !== undefined) out.enabled = input.is_enabled;
  if (input.models !== undefined) out.models = modelsArrayFromText(input.models ?? undefined) ?? [];
  if (input.models_url !== undefined) out.models_url = input.models_url ?? "";
  return out;
}

export function modelFromRoute(route: GoRoute): Model {
  const targets = (route.upstreams ?? []).map(modelBackendFromRouteUpstream);
  return {
    id: route.id,
    name: route.model,
    balance: route.balance,
    target_provider: targets[0]?.provider_id ?? "",
    target_model: targets[0]?.model ?? "",
    enable_auth: route.enable_auth,
    enable_payload: route.enable_payload,
    is_enabled: route.enabled,
    created_at: route.created_at ?? "",
    targets,
  };
}

function modelBackendFromRouteUpstream(target: GoRouteUpstream): ModelBackend {
  return {
    id: target.id,
    model_id: target.route_id,
    provider_id: target.upstream_id,
    model: target.model,
    weight: target.weight,
    priority: target.priority,
    created_at: target.created_at ?? "",
  };
}

export function createRouteFromModel(input: CreateModel): GoCreateRoute {
  const targets = input.targets?.length
    ? input.targets.map(routeTargetFromModelBackend)
    : [{ upstream_id: input.target_provider, model: input.target_model, enabled: true }];
  return {
    model: input.name,
    balance: input.balance,
    enable_auth: input.enable_auth,
    enable_payload: input.enable_payload,
    upstreams: targets,
  };
}

export function updateRouteFromModel(input: UpdateModel): GoUpdateRoute {
  const out: GoUpdateRoute = {};
  if (input.name !== undefined) out.model = input.name;
  if (input.balance !== undefined) out.balance = input.balance;
  if (input.enable_auth !== undefined) out.enable_auth = input.enable_auth;
  if (input.enable_payload !== undefined) out.enable_payload = input.enable_payload;
  if (input.is_enabled !== undefined) out.enabled = input.is_enabled;
  if (input.targets !== undefined) {
    out.upstreams = input.targets.map(routeTargetFromModelBackend);
  } else if (input.target_provider !== undefined || input.target_model !== undefined) {
    out.upstreams = [
      {
        upstream_id: input.target_provider ?? "",
        model: input.target_model ?? "",
        enabled: true,
      },
    ];
  }
  return out;
}

export function apiKeyFromConsumer(consumer: GoConsumer): ApiKey {
  const keys = consumer.keys ?? [];
  const firstKey = keys[0];
  const quotas = consumer.quotas ?? [];
  return {
    id: consumer.id,
    key: firstKey?.token ?? firstKey?.key_prefix ?? "",
    name: consumer.name,
    rpm: quotaValue(quotas, "requests", "1m"),
    rpd: quotaValue(quotas, "requests", "24h"),
    tpm: quotaValue(quotas, "tokens", "1m"),
    tpd: quotaValue(quotas, "tokens", "24h"),
    max_requests: quotaValue(quotas, "concurrency", undefined),
    is_enabled: consumer.enabled,
    expires_at: firstKey?.expires_at ?? null,
    created_at: consumer.created_at ?? firstKey?.created_at ?? "",
    updated_at: consumer.updated_at ?? firstKey?.updated_at ?? "",
    model_ids: consumer.routes ?? [],
  };
}

export function createConsumerFromApiKey(input: CreateApiKey): GoCreateConsumer {
  return {
    name: input.name,
    enabled: true,
    keys: [{ name: input.name || "primary", expires_at: input.expires_at }],
    routes: input.model_ids,
    quotas: quotasFromApiKey(input),
  };
}

export function updateConsumerFromApiKey(input: UpdateApiKey): GoUpdateConsumer {
  const out: GoUpdateConsumer = {};
  if (input.name !== undefined) out.name = input.name;
  if (input.is_enabled !== undefined) out.enabled = input.is_enabled;
  return out;
}

export function providerPresetFromGoPreset(preset: GoProviderPreset): ProviderPreset {
  // Presets no longer carry a static model list — only an optional default
  // discovery URL. `staticModels` is intentionally left unset on the
  // synthesized channel.
  const channels: ProviderChannelPreset[] = preset.protocols.map((protocol) => ({
    id: protocol.id,
    label: { en: protocol.id, zh: protocol.id },
    authMode: "apikey",
    baseUrls: { [protocol.id]: protocol.base_url ?? "" },
    modelsSource: preset.models_url,
    modelsEndpoint: preset.models_url,
  }));
  return {
    id: preset.id,
    label: { en: preset.name, zh: preset.name },
    icon: preset.id,
    priority: preset.priority,
    defaultProtocol: preset.default_protocol,
    channels,
    credentialFields: preset.credentials?.fields ?? [],
  };
}
