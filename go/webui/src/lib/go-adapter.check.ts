import {
  apiKeyFromConsumer,
  createConsumerFromApiKey,
  createRouteFromModel,
  createUpstreamFromProvider,
  modelFromRoute,
  providerFromUpstream,
  updateConsumerFromApiKey,
  updateRouteFromModel,
  updateUpstreamFromProvider,
} from "./go-adapter";
import type { CreateApiKey, CreateModel, CreateProvider, UpdateApiKey, UpdateModel, UpdateProvider } from "./types";
import type { GoConsumer, GoRoute, GoUpstream } from "./go-schema";

const upstream: GoUpstream = {
  id: "up_1",
  name: "OpenAI",
  provider: "openai",
  protocol: "openai-chatcompletions",
  base_url: "https://api.openai.com/v1",
  credentials: { api_key: "sk-test" },
  models: { values: ["gpt-4o"] },
  proxy_url: "",
  enabled: true,
};

const route: GoRoute = {
  id: "route_1",
  model: "gpt-4o",
  balance: "weighted",
  enable_auth: true,
  enabled: true,
  upstreams: [{ id: "target_1", route_id: "route_1", upstream_id: "up_1", model: "gpt-4o", weight: 1, priority: 0, enabled: true }],
};

const consumer: GoConsumer = {
  id: "consumer_1",
  name: "Alice",
  enabled: true,
  keys: [{ id: "key_1", consumer_id: "consumer_1", name: "primary", key_prefix: "nyro", enabled: true }],
  routes: ["gpt-4o"],
  quotas: [{ quota_type: "requests", quota_limit: 60, window: "1m" }],
};

export const __goAdapterCheck = {
  provider: providerFromUpstream(upstream),
  createUpstream: createUpstreamFromProvider({ name: "OpenAI", protocol: "openai-chatcompletions", base_url: "https://api.openai.com/v1", api_key: "sk" } satisfies CreateProvider),
  updateUpstream: updateUpstreamFromProvider({ is_enabled: false } satisfies UpdateProvider),
  model: modelFromRoute(route),
  createRoute: createRouteFromModel({ name: "gpt-4o", target_provider: "up_1", target_model: "gpt-4o" } satisfies CreateModel),
  updateRoute: updateRouteFromModel({ is_enabled: false } satisfies UpdateModel),
  apiKey: apiKeyFromConsumer(consumer),
  createConsumer: createConsumerFromApiKey({ name: "Alice", model_ids: ["gpt-4o"] } satisfies CreateApiKey),
  updateConsumer: updateConsumerFromApiKey({ is_enabled: false } satisfies UpdateApiKey),
};
