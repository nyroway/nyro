import { describe, expect, it } from "vitest";

import {
  apiKeyFromConsumer,
  createConsumerFromApiKey,
  createUpstreamFromProvider,
  providerFromUpstream,
  providerPresetFromGoPreset,
  updateConsumerFromApiKey,
} from "./go-adapter";
import type { CreateApiKey, CreateProvider, UpdateApiKey } from "./types";
import type { GoProviderPreset, GoUpstream } from "./go-schema";

// These tests cover the highest-risk adapter round-trips called out in the
// Task 12 review: quota (rpm/rpd/tpm/tpd/concurrency) mapping between the
// legacy ApiKey shape and the Go consumer-quota shape, and the
// upstream<->provider credentials/models JSON blob parsing. The quota
// functions (`quotasFromApiKey`/`quotaValue`) are not exported, so they are
// exercised indirectly through `createConsumerFromApiKey`/`apiKeyFromConsumer`.

describe("quota round-trip (rpm/rpd/tpm/tpd/concurrency)", () => {
  it("maps all quota fields from CreateApiKey into GoCreateConsumerQuota[], dropping unset ones", () => {
    const input: CreateApiKey = {
      name: "Alice",
      model_ids: ["gpt-4o"],
      rpm: 60,
      rpd: 1000,
      tpm: 10000,
      tpd: 100000,
      max_requests: 5,
    };
    const out = createConsumerFromApiKey(input);
    expect(out.quotas).toEqual(
      expect.arrayContaining([
        { quota_type: "requests", quota_limit: 60, window: "1m" },
        { quota_type: "requests", quota_limit: 1000, window: "24h" },
        { quota_type: "tokens", quota_limit: 10000, window: "1m" },
        { quota_type: "tokens", quota_limit: 100000, window: "24h" },
        { quota_type: "concurrency", quota_limit: 5, window: undefined },
      ]),
    );
    expect(out.quotas).toHaveLength(5);
  });

  it("omits quotas whose input value is undefined", () => {
    const input: CreateApiKey = { name: "Bob", model_ids: [] };
    const out = createConsumerFromApiKey(input);
    expect(out.quotas).toEqual([]);
  });

  it("round-trips rpm/rpd/tpm/tpd back out of a GoConsumer via apiKeyFromConsumer", () => {
    const apiKey = apiKeyFromConsumer({
      id: "consumer_1",
      name: "Alice",
      enabled: true,
      keys: [{ id: "key_1", consumer_id: "consumer_1", name: "primary", key_prefix: "nyro", enabled: true }],
      routes: ["gpt-4o"],
      quotas: [
        { quota_type: "requests", quota_limit: 60, window: "1m" },
        { quota_type: "requests", quota_limit: 1000, window: "24h" },
        { quota_type: "tokens", quota_limit: 10000, window: "1m" },
        { quota_type: "tokens", quota_limit: 100000, window: "24h" },
        { quota_type: "concurrency", quota_limit: 5, window: undefined },
      ],
    });
    expect(apiKey.rpm).toBe(60);
    expect(apiKey.rpd).toBe(1000);
    expect(apiKey.tpm).toBe(10000);
    expect(apiKey.tpd).toBe(100000);
    expect(apiKey.max_requests).toBe(5);
  });

  it("round-trips the concurrency quota's undefined window whether it arrives as undefined or JSON null", () => {
    // window omitted entirely (undefined)
    const withUndefinedWindow = apiKeyFromConsumer({
      id: "c1",
      name: "A",
      enabled: true,
      quotas: [{ quota_type: "concurrency", quota_limit: 8, window: undefined }],
    });
    expect(withUndefinedWindow.max_requests).toBe(8);

    // window explicitly null (as it can arrive after a JSON round-trip)
    const withNullWindow = apiKeyFromConsumer({
      id: "c2",
      name: "B",
      enabled: true,
      quotas: [{ quota_type: "concurrency", quota_limit: 8, window: null as unknown as undefined }],
    });
    expect(withNullWindow.max_requests).toBe(8);
  });

  it("does not touch route grants (model_ids) on update, since the Go backend only supports name/enabled", () => {
    const input: UpdateApiKey = { name: "New name", is_enabled: false, model_ids: ["gpt-4o", "claude-3"] };
    const out = updateConsumerFromApiKey(input);
    expect(out).toEqual({ name: "New name", enabled: false });
    expect((out as Record<string, unknown>).routes).toBeUndefined();
  });
});

describe("provider <-> upstream credentials/models round-trip", () => {
  it("parses a stringified credentials JSON blob and a models array back into a Provider", () => {
    const upstream: GoUpstream = {
      id: "up_1",
      name: "OpenAI",
      provider: "openai",
      protocol: "openai-chatcompletions",
      base_url: "https://api.openai.com/v1",
      credentials: JSON.stringify({ api_key: "sk-test" }),
      models: ["gpt-4o"],
      proxy_url: "",
      enabled: true,
    };
    const provider = providerFromUpstream(upstream);
    expect(provider.api_key).toBe("sk-test");
    expect(provider.credentials).toEqual({ api_key: "sk-test" });
    expect(provider.provider).toBe("openai");
    expect(provider.models).toBe("gpt-4o");
  });

  it("newline-joins a multi-entry models array", () => {
    const upstream: GoUpstream = {
      id: "up_1b",
      name: "OpenAI",
      models: ["m1", "m2"],
      enabled: true,
    };
    const provider = providerFromUpstream(upstream);
    expect(provider.models).toBe("m1\nm2");
  });

  it("parses an already-object credentials blob (no double JSON.parse)", () => {
    const upstream: GoUpstream = {
      id: "up_2",
      name: "Anthropic",
      provider: "anthropic",
      credentials: { api_key: "sk-ant" },
      enabled: true,
    };
    const provider = providerFromUpstream(upstream);
    expect(provider.api_key).toBe("sk-ant");
    expect(provider.provider).toBe("anthropic");
  });

  it("falls back to an empty record for malformed/non-JSON credentials, never throwing", () => {
    const upstream: GoUpstream = {
      id: "up_3",
      name: "Broken",
      credentials: "{not json",
      enabled: true,
    };
    const provider = providerFromUpstream(upstream);
    expect(provider.api_key).toBe("");
    expect(provider.credentials).toEqual({});
  });

  it("builds a GoCreateUpstream credentials blob and models/models_url from CreateProvider, preferring the structured credentials map over the single api_key field", () => {
    const input: CreateProvider = {
      name: "OpenAI",
      provider: "openai",
      protocol: "openai-chatcompletions",
      base_url: "https://api.openai.com/v1",
      api_key: "sk-should-be-ignored",
      credentials: { api_key: "sk-real", org_id: "org-1" },
    };
    const out = createUpstreamFromProvider(input);
    expect(out.credentials).toEqual({ api_key: "sk-real", org_id: "org-1" });
    expect(out.provider).toBe("openai");
    expect(out.models).toBeUndefined();
    expect(out.models_url).toBeUndefined();
  });

  it("falls back to { api_key } when CreateProvider has no structured credentials map", () => {
    const input: CreateProvider = {
      name: "OpenAI",
      provider: "custom",
      protocol: "openai-chatcompletions",
      base_url: "https://api.openai.com/v1",
      api_key: "sk-plain",
    };
    const out = createUpstreamFromProvider(input);
    expect(out.credentials).toEqual({ api_key: "sk-plain" });
  });
});

describe("providerPresetFromGoPreset model discovery mapping", () => {
  it("carries the real discovery URL into modelsSource for a preset with a default discovery URL", () => {
    const preset: GoProviderPreset = {
      id: "openai",
      name: "OpenAI",
      priority: 2,
      default_protocol: "openai-chatcompletions",
      protocols: [{ id: "openai-chatcompletions", base_url: "https://api.openai.com/v1" }],
      credentials: { fields: [] },
      models_url: "https://api.openai.com/v1/models",
    };
    const out = providerPresetFromGoPreset(preset);
    expect(out.channels?.[0]?.modelsSource).toBe("https://api.openai.com/v1/models");
    expect(out.channels?.[0]?.staticModels).toBeUndefined();
  });

  it("leaves modelsSource unset for a preset with no default discovery URL", () => {
    const preset: GoProviderPreset = {
      id: "custom-static",
      name: "Custom Static",
      priority: 9,
      default_protocol: "openai-chatcompletions",
      protocols: [{ id: "openai-chatcompletions", base_url: "https://example.com/v1" }],
      credentials: { fields: [] },
    };
    const out = providerPresetFromGoPreset(preset);
    expect(out.channels?.[0]?.modelsSource).toBeUndefined();
    expect(out.channels?.[0]?.staticModels).toBeUndefined();
  });
});
