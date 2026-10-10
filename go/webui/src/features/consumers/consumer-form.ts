import type {
  ConsumerLimits,
  ConsumerQuota,
  CreateConsumerQuota,
} from "@/lib/types";
import { localizedMessage, type MessageKey } from "@/lib/messages";

/* Pure form logic for the api-keys page (§9.4): validity presets, form<->payload
   conversion for quotas/limits/allowlist, and IP validation. Extracted from the page so it can be unit tested directly. */

export type ExpirePreset = "never" | "1d" | "7d" | "30d" | "90d" | "180d" | "1y";

export const expirePresetOptions: { value: ExpirePreset; label: MessageKey }[] = [
  { value: "never", label: "consumers.expiry.never" },
  { value: "1d", label: "consumers.expiry.1d" },
  { value: "7d", label: "consumers.expiry.7d" },
  { value: "30d", label: "consumers.expiry.30d" },
  { value: "90d", label: "consumers.expiry.90d" },
  { value: "180d", label: "consumers.expiry.180d" },
  { value: "1y", label: "consumers.expiry.1y" },
];

/** Creation-response token shown exactly once (§9.4 note: visible only in the creation response; once closed it can never be fetched again). */
export type RevealedKey = { name: string; token: string };

export function formatExpiresText(value: string | null | undefined, isZh: boolean) {
  if (!value) return localizedMessage(isZh, "v2.api-keys.never");
  return value.replace("T", " ").slice(0, 19);
}

export function isApiKeyExpired(expiresAt: string | null | undefined) {
  if (!expiresAt) return false;
  const normalized = expiresAt.includes("T") ? expiresAt : expiresAt.replace(" ", "T");
  const utcMillis = Date.parse(normalized.endsWith("Z") ? normalized : `${normalized}Z`);
  if (!Number.isNaN(utcMillis)) {
    return utcMillis <= Date.now();
  }
  const fallbackMillis = Date.parse(expiresAt);
  return !Number.isNaN(fallbackMillis) && fallbackMillis <= Date.now();
}

export function formatValidityLabel(expired: boolean, isZh: boolean) {
  return expired ? (localizedMessage(isZh, "v2.api-keys.expired")) : (localizedMessage(isZh, "v2.api-keys.valid"));
}

export function resolveExpiresAt(preset: ExpirePreset) {
  if (preset === "never") return undefined;
  const now = Date.now();
  const day = 24 * 60 * 60 * 1000;
  const map: Record<Exclude<ExpirePreset, "never">, number> = {
    "1d": 1,
    "7d": 7,
    "30d": 30,
    "90d": 90,
    "180d": 180,
    "1y": 365,
  };
  const date = new Date(now + map[preset] * day);
  return date.toISOString().slice(0, 19).replace("T", " ");
}

/** Same preset -> timestamp mapping as `resolveExpiresAt`, but for `UpdateConsumerKey`
 *  where the "not provided" and "clear it" cases are distinct: omitting the field means
 *  "leave unchanged", while an empty string means "clear to never expires". Editing a key's
 *  expiry to "never" must therefore send `""`, not `undefined`. */
export function resolveExpiresAtForUpdate(preset: ExpirePreset) {
  return preset === "never" ? "" : resolveExpiresAt(preset);
}

export function digitsOnly(value: string) {
  return value.replace(/[^\d]/g, "");
}

export type QuotaRuleForm = { limit: string; window: string };

export type QuotaFormState = {
  requests: QuotaRuleForm[];
  tokens: QuotaRuleForm[];
  concurrency: string;
};

const emptyQuotaRow: QuotaRuleForm = { limit: "", window: "" };

/** buildQuotasPayload drops any row with an empty limit, so a default blank
 *  row is purely a UI convenience — it never reaches the submitted payload
 *  unless the user actually fills in a limit. */
export const emptyQuotaForm: QuotaFormState = { requests: [{ ...emptyQuotaRow }], tokens: [{ ...emptyQuotaRow }], concurrency: "" };

export function quotasToForm(quotas: ConsumerQuota[] | undefined): QuotaFormState {
  const form: QuotaFormState = { requests: [], tokens: [], concurrency: "" };
  for (const q of quotas ?? []) {
    if (q.quota_type === "requests") {
      form.requests.push({ limit: String(q.quota_limit), window: q.window ?? "" });
    } else if (q.quota_type === "tokens") {
      form.tokens.push({ limit: String(q.quota_limit), window: q.window ?? "" });
    } else if (q.quota_type === "concurrency") {
      form.concurrency = String(q.quota_limit);
    }
  }
  if (form.requests.length === 0) form.requests.push({ ...emptyQuotaRow });
  if (form.tokens.length === 0) form.tokens.push({ ...emptyQuotaRow });
  return form;
}

export function buildQuotasPayload(form: QuotaFormState): CreateConsumerQuota[] {
  const quotas: CreateConsumerQuota[] = [];
  for (const row of form.requests) {
    if (!row.limit) continue;
    quotas.push({
      quota_type: "requests",
      quota_limit: Number.parseInt(row.limit, 10),
      window: row.window || undefined,
    });
  }
  for (const row of form.tokens) {
    if (!row.limit) continue;
    quotas.push({
      quota_type: "tokens",
      quota_limit: Number.parseInt(row.limit, 10),
      window: row.window || undefined,
    });
  }
  if (form.concurrency) {
    quotas.push({ quota_type: "concurrency", quota_limit: Number.parseInt(form.concurrency, 10) });
  }
  return quotas;
}

export type LimitsFormState = { maxInputTokens: string; maxOutputTokens: string; maxRequestBodyBytes: string };

export const emptyLimitsForm: LimitsFormState = { maxInputTokens: "", maxOutputTokens: "", maxRequestBodyBytes: "" };

export function limitsToForm(limits: ConsumerLimits | undefined): LimitsFormState {
  return {
    maxInputTokens: limits?.max_input_tokens ? String(limits.max_input_tokens) : "",
    maxOutputTokens: limits?.max_output_tokens ? String(limits.max_output_tokens) : "",
    maxRequestBodyBytes: limits?.max_request_body_bytes ? String(limits.max_request_body_bytes) : "",
  };
}

/** Returns undefined (omit `limits` entirely) when every field is empty, rather
 *  than sending an all-zero object — zero on a single field already means "no
 *  limit" for that dimension, so an empty form should not touch the others. */
export function buildLimitsPayload(form: LimitsFormState): ConsumerLimits | undefined {
  if (!form.maxInputTokens && !form.maxOutputTokens && !form.maxRequestBodyBytes) return undefined;
  return {
    max_input_tokens: form.maxInputTokens ? Number.parseInt(form.maxInputTokens, 10) : undefined,
    max_output_tokens: form.maxOutputTokens ? Number.parseInt(form.maxOutputTokens, 10) : undefined,
    max_request_body_bytes: form.maxRequestBodyBytes ? Number.parseInt(form.maxRequestBodyBytes, 10) : undefined,
  };
}

/** access.ip_allowlist is edited as one input row per entry (like the
 *  requests/tokens quota rows), each holding a single IP or CIDR block. */
export function ipAllowlistToForm(list: string[] | undefined): string[] {
  return list && list.length > 0 ? [...list] : [""];
}

/** buildAccessListPayload drops blank rows, mirroring buildQuotasPayload's
 *  skip-empty-limit behavior — an untouched blank row never reaches the
 *  submitted payload. */
export function buildAccessListPayload(rows: string[]): string[] {
  return rows.map((r) => r.trim()).filter(Boolean);
}

export function isValidIPv4(addr: string): boolean {
  const parts = addr.split(".");
  if (parts.length !== 4) return false;
  return parts.every((p) => /^\d{1,3}$/.test(p) && Number(p) >= 0 && Number(p) <= 255);
}

/** Structural IPv6 check: fully-expanded form is 8 hex groups; a single "::"
 *  compresses one or more zero groups (its two halves hold at most 7 groups
 *  together). The pre-extraction page version rejected every compressed
 *  literal (::1, fe80::, 2001:db8::1 …) while accepting bare hex like "abc". */
export function isValidIPv6(addr: string): boolean {
  if (!/^[0-9a-fA-F:]+$/.test(addr)) return false;
  if ((addr.match(/::/g) ?? []).length > 1) return false;
  const hexGroup = /^[0-9a-fA-F]{1,4}$/;
  if (addr.includes("::")) {
    const [left, right] = addr.split("::");
    const groups = [
      ...(left ? left.split(":") : []),
      ...(right ? right.split(":") : []),
    ];
    return groups.every((g) => hexGroup.test(g)) && groups.length <= 7;
  }
  if (!addr.includes(":")) return false;
  const groups = addr.split(":");
  return groups.length === 8 && groups.every((g) => hexGroup.test(g));
}

/** Accepts a bare IP (v4 or v6) or a CIDR block (IP + "/" + prefix length).
 *  An empty string is treated as valid — blank rows are filtered out at
 *  submit time by buildAccessListPayload, not flagged as errors while typing. */
export function isValidIPOrCIDR(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true;
  const [addr, prefix, ...rest] = trimmed.split("/");
  if (rest.length > 0) return false;
  if (isValidIPv4(addr)) {
    return prefix === undefined || (/^\d{1,2}$/.test(prefix) && Number(prefix) <= 32);
  }
  if (isValidIPv6(addr)) {
    return prefix === undefined || (/^\d{1,3}$/.test(prefix) && Number(prefix) <= 128);
  }
  return false;
}

export function formatQuotaRule(q: ConsumerQuota) {
  return q.window ? `${q.quota_limit}/${q.window}` : `${q.quota_limit}`;
}

/** Quota window options ("" = no window distinction), for use by NyroSearchSelect. */
export const QUOTA_WINDOW_VALUES = ["", "1m", "5m", "15m", "1h", "6h", "12h", "1d"] as const;

export function quotaWindowOptions(isZh: boolean): { id: string; label: string }[] {
  return QUOTA_WINDOW_VALUES.map((value) => ({
    id: value,
    label: value === "" ? localizedMessage(isZh, "v2.api-keys.noWindow") : value,
  }));
}
