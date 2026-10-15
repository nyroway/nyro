/**
 * Protocol utilities — mirrors the backend three-layer identity model.
 *
 * Three orthogonal concepts:
 *   Protocol  — suite / wire-format family  (e.g. "openai-compatible")
 *   Endpoint  — specific API path           (e.g. "chat-completions")
 *   Vendor    — provider organisation       (e.g. "openai")
 *
 * UI only surfaces the Protocol display name; endpoints and versions are
 * internal implementation details not shown to users.
 *
 * Keep the alias table in sync with the Rust side:
 *   crates/nyro-core/src/protocol/registry.rs::default_protocol_aliases
 */

// ── Protocol enum (canonical identifiers) ──────────────────────────────────

export type Protocol =
  | "openai-compatible"
  | "openai-responses"
  | "anthropic-messages"
  | "google-gemini";

export interface ProtocolMeta {
  id: Protocol;
  /** Human-readable display name shown in the UI. */
  displayName: string;
  /** Default base URL shown as placeholder in the provider form. */
  defaultBaseUrl: string;
}

export const PROTOCOL_TABLE: ProtocolMeta[] = [
  {
    id: "openai-compatible",
    displayName: "OpenAI Compatible",
    defaultBaseUrl: "https://api.openai.com/v1",
  },
  {
    id: "openai-responses",
    displayName: "OpenAI Responses",
    defaultBaseUrl: "https://api.openai.com/v1",
  },
  {
    id: "anthropic-messages",
    displayName: "Anthropic Messages",
    defaultBaseUrl: "https://api.anthropic.com",
  },
  {
    id: "google-gemini",
    displayName: "Google Gemini",
    defaultBaseUrl: "https://generativelanguage.googleapis.com",
  },
];

// ── Alias resolution ───────────────────────────────────────────────────────

/** Maps any known string (old canonical, short alias, legacy brand) → Protocol. */
const PROTOCOL_ALIASES: Record<string, Protocol> = {
  // Canonical (new)
  "openai-compatible": "openai-compatible",
  "openai-responses": "openai-responses",
  "anthropic-messages": "anthropic-messages",
  "google-gemini": "google-gemini",

  // Short names
  openai: "openai-compatible",
  openai_responses: "openai-responses",
  responses: "openai-responses",
  anthropic: "anthropic-messages",
  claude: "anthropic-messages",
  gemini: "google-gemini",
  google: "google-gemini",

  // Deprecated aliases (old canonical slugs)
  "openai-compat": "openai-compatible",
  "openai-resps": "openai-responses",
  "anthropic-msgs": "anthropic-messages",
  "google-genai": "google-gemini",
  "google-generative-ai": "google-gemini",

  // Old canonical endpoint strings (Tier-1 backward compat)
  "openai/chat/v1": "openai-compatible",
  "openai/embeddings/v1": "openai-compatible",
  "openai/responses/v1": "openai-responses",
  "anthropic/messages/2023-06-01": "anthropic-messages",
  "google/generate/v1beta": "google-gemini",

  // Deprecated canonical endpoint strings
  "openai-compat/chat-completions/v1": "openai-compatible",
  "openai-compat/embeddings/v1": "openai-compatible",
  "openai-resps/responses/v1": "openai-responses",
  "anthropic-msgs/messages/2023-06-01": "anthropic-messages",
  "google-genai/generate-content/v1beta": "google-gemini",

  // New canonical endpoint strings
  "openai-compatible/chat-completions/v1": "openai-compatible",
  "openai-compatible/embeddings/v1": "openai-compatible",
  "openai-responses/responses/v1": "openai-responses",
  "anthropic-messages/messages/2023-06-01": "anthropic-messages",
  "google-gemini/generate-content/v1beta": "google-gemini",
};

/**
 * Resolve any raw protocol string to a canonical `Protocol`, or `null` if unknown.
 *
 * Accepts: new canonical keys (`"openai-compatible"`), legacy aliases (`"openai"`),
 * old endpoint canonical strings (`"openai/chat/v1"`), and new endpoint
 * canonical strings (`"openai-compatible/chat-completions/v1"`).
 */
export function resolveProtocol(raw: string | null | undefined): Protocol | null {
  if (!raw) return null;
  const key = raw.trim().toLowerCase();
  return PROTOCOL_ALIASES[key] ?? null;
}

/** Return the display name for a protocol string, or `null` if unknown. */
export function protocolDisplayName(raw: string | null | undefined): string | null {
  const protocol = resolveProtocol(raw);
  if (!protocol) return null;
  return PROTOCOL_TABLE.find((p) => p.id === protocol)?.displayName ?? null;
}

/**
 * Legacy shim — resolves a raw string and returns just the display name.
 *
 * Returns `null` when the input is unrecognised so callers can fall back
 * to showing the raw string.
 *
 * @deprecated prefer `protocolDisplayName` for new code.
 */
export function prettyName(raw: string | null | undefined): string | null {
  return protocolDisplayName(raw);
}

// ── ProtocolEndpoint (internal, not shown in UI) ───────────────────────────

export interface ProtocolEndpoint {
  protocol: Protocol;
  name: string;
  version: string;
}

/** Parse a canonical `protocol/name/version` string into a `ProtocolEndpoint`. */
export function parseProtocolEndpoint(raw: string | null | undefined): ProtocolEndpoint | null {
  if (!raw) return null;
  const parts = raw.trim().split("/");
  if (parts.length !== 3 || parts.some((p) => !p)) return null;
  const protocol = resolveProtocol(parts[0]);
  if (!protocol) return null;
  return { protocol, name: parts[1], version: parts[2] };
}

// ── Backward-compat shims for routes.tsx ──────────────────────────────────

/** Returns true when the raw string resolves to an OpenAI-family protocol. */
export function isOpenAiProtocol(raw: string | null | undefined): boolean {
  const p = resolveProtocol(raw);
  return p === "openai-compatible" || p === "openai-responses";
}

/**
 * @deprecated — kept for legacy call-sites, use `parseProtocolEndpoint` instead.
 */
export function parseProtocolId(raw: string | null | undefined): { family: string; dialect: string; version: string } | null {
  const ep = parseProtocolEndpoint(raw);
  if (ep) return { family: ep.protocol, dialect: ep.name, version: ep.version };
  // Fallback: try to parse old `family/dialect/version` form verbatim.
  if (!raw) return null;
  const parts = raw.trim().split("/");
  if (parts.length === 3 && parts.every((p) => p.length > 0)) {
    return { family: parts[0], dialect: parts[1], version: parts[2] };
  }
  return null;
}