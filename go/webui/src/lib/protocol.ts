/**
 * Protocol utilities — mirrors the backend protocol identity model
 * (go/internal/protocol/ids).
 *
 * A Protocol identifies one concrete wire-format API surface (an
 * "interface"), vendor-prefixed but vendor-orthogonal in use. The canonical
 * endpoint form is `{protocol}/{version}` (e.g. "openai-chatcompletions/v1")
 * — there is no separate "endpoint name" layer.
 *
 * UI only surfaces the Protocol display name; the version is an internal
 * implementation detail not shown to users.
 *
 * Each protocol has exactly one short alias — keep this table in sync with
 * go/internal/protocol/ids/ids.go's ParseProtocol. There is no legacy/
 * back-compat alias set: this schema has no released consumers yet.
 */

// ── Protocol enum (canonical identifiers) ──────────────────────────────────

export type Protocol =
  | "anthropic-messages"
  | "openai-chatcompletions"
  | "openai-responses"
  | "gemini-generatecontent"
  | "gemini-interactions"
  | "bedrock-converse"
  | "azure-modelinference";

export interface ProtocolMeta {
  id: Protocol;
  /** Short, vendor-agnostic label (e.g. "Messages API") — mirrors Go's Protocol.Name(). */
  name: string;
  /** Vendor-qualified label (e.g. "Anthropic Messages API") — mirrors Go's Protocol.FullName(). */
  fullName: string;
  /** Default base URL shown as placeholder in the provider form. */
  defaultBaseUrl: string;
}

// gemini-interactions, bedrock-converse, and azure-modelinference are
// declared only — no codec is registered for them on the backend yet
// (go/internal/protocol/ids/ids.go), so defaultBaseUrl is left empty.
export const PROTOCOL_TABLE: ProtocolMeta[] = [
  {
    id: "anthropic-messages",
    name: "Messages API",
    fullName: "Anthropic Messages API",
    defaultBaseUrl: "https://api.anthropic.com",
  },
  {
    id: "openai-chatcompletions",
    name: "ChatCompletions API",
    fullName: "OpenAI Chat Completions API",
    defaultBaseUrl: "https://api.openai.com/v1",
  },
  {
    id: "openai-responses",
    name: "Responses API",
    fullName: "OpenAI Responses API",
    defaultBaseUrl: "https://api.openai.com/v1",
  },
  {
    id: "gemini-generatecontent",
    name: "GenerateContent API",
    fullName: "Gemini GenerateContent API",
    defaultBaseUrl: "https://generativelanguage.googleapis.com",
  },
  {
    id: "gemini-interactions",
    name: "Interactions API",
    fullName: "Gemini Interactions API",
    defaultBaseUrl: "",
  },
  {
    id: "bedrock-converse",
    name: "Converse API",
    fullName: "Bedrock Converse API",
    defaultBaseUrl: "",
  },
  {
    id: "azure-modelinference",
    name: "ModelInference API",
    fullName: "Azure AI Model Inference API",
    defaultBaseUrl: "",
  },
];

// ── Alias resolution ───────────────────────────────────────────────────────

/** Maps a canonical identifier or its single alias → Protocol. */
const PROTOCOL_ALIASES: Record<string, Protocol> = {
  "anthropic-messages": "anthropic-messages",
  claude: "anthropic-messages",

  "openai-chatcompletions": "openai-chatcompletions",
  openai: "openai-chatcompletions",

  "openai-responses": "openai-responses",
  responses: "openai-responses",

  "gemini-generatecontent": "gemini-generatecontent",
  gemini: "gemini-generatecontent",

  "gemini-interactions": "gemini-interactions",
  interactions: "gemini-interactions",

  "bedrock-converse": "bedrock-converse",
  bedrock: "bedrock-converse",

  "azure-modelinference": "azure-modelinference",
  azure: "azure-modelinference",
};

/**
 * Resolve any raw protocol string to a canonical `Protocol`, or `null` if unknown.
 *
 * Accepts the canonical identifier (`"openai-chatcompletions"`) or its single
 * short alias (`"openai"`).
 */
export function resolveProtocol(raw: string | null | undefined): Protocol | null {
  if (!raw) return null;
  const key = raw.trim().toLowerCase();
  return PROTOCOL_ALIASES[key] ?? null;
}

/** Return the vendor-qualified display name for a protocol string (mirrors Go's FullName()), or `null` if unknown. */
export function protocolDisplayName(raw: string | null | undefined): string | null {
  const protocol = resolveProtocol(raw);
  if (!protocol) return null;
  return PROTOCOL_TABLE.find((p) => p.id === protocol)?.fullName ?? null;
}

/** Return the short, vendor-agnostic display name for a protocol string (mirrors Go's Name()), or `null` if unknown. */
export function protocolShortName(raw: string | null | undefined): string | null {
  const protocol = resolveProtocol(raw);
  if (!protocol) return null;
  return PROTOCOL_TABLE.find((p) => p.id === protocol)?.name ?? null;
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
  return p === "openai-chatcompletions" || p === "openai-responses";
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
