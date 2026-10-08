import { describe, expect, it } from "vitest";

import {
  buildAccessListPayload,
  buildLimitsPayload,
  buildQuotasPayload,
  digitsOnly,
  emptyQuotaForm,
  formatExpiresText,
  formatQuotaRule,
  formatValidityLabel,
  ipAllowlistToForm,
  isApiKeyExpired,
  isValidIPOrCIDR,
  limitsToForm,
  quotasToForm,
  quotaWindowOptions,
  resolveExpiresAt,
  resolveExpiresAtForUpdate,
} from "./consumer-form";

describe("quota form <-> payload", () => {
  it("pads empty requests/tokens groups with one blank row for editing", () => {
    expect(quotasToForm(undefined)).toEqual({ requests: [{ limit: "", window: "" }], tokens: [{ limit: "", window: "" }], concurrency: "" });
    expect(quotasToForm([{ quota_type: "requests", quota_limit: 100, window: "1m" }])).toEqual({
      requests: [{ limit: "100", window: "1m" }],
      tokens: [{ limit: "", window: "" }],
      concurrency: "",
    });
  });

  it("maps requests/tokens/concurrency quotas into the form", () => {
    expect(quotasToForm([
      { quota_type: "requests", quota_limit: 10, window: "1m" },
      { quota_type: "requests", quota_limit: 20 },
      { quota_type: "tokens", quota_limit: 5000, window: "1h" },
      { quota_type: "concurrency", quota_limit: 4 },
    ])).toEqual({
      requests: [{ limit: "10", window: "1m" }, { limit: "20", window: "" }],
      tokens: [{ limit: "5000", window: "1h" }],
      concurrency: "4",
    });
  });

  it("drops rows with an empty limit, so the default blank row never submits", () => {
    expect(buildQuotasPayload(emptyQuotaForm)).toEqual([]);
    expect(buildQuotasPayload({ requests: [{ limit: "", window: "1m" }], tokens: [], concurrency: "" })).toEqual([]);
  });

  it("builds requests/tokens rows with an omitted window and concurrency without one", () => {
    expect(buildQuotasPayload({
      requests: [{ limit: "100", window: "" }, { limit: "200", window: "1d" }],
      tokens: [{ limit: "5000", window: "1h" }],
      concurrency: "4",
    })).toEqual([
      { quota_type: "requests", quota_limit: 100, window: undefined },
      { quota_type: "requests", quota_limit: 200, window: "1d" },
      { quota_type: "tokens", quota_limit: 5000, window: "1h" },
      { quota_type: "concurrency", quota_limit: 4 },
    ]);
  });

  it("summarizes a quota rule as limit/window (or bare limit without a window)", () => {
    expect(formatQuotaRule({ quota_type: "requests", quota_limit: 100, window: "1m" })).toBe("100/1m");
    expect(formatQuotaRule({ quota_type: "concurrency", quota_limit: 4 })).toBe("4");
  });

  it("offers the fixed window values with the blank one labeled as no-window", () => {
    expect(quotaWindowOptions(false)).toEqual([
      { id: "", label: "No window" },
      { id: "1m", label: "1m" },
      { id: "5m", label: "5m" },
      { id: "15m", label: "15m" },
      { id: "1h", label: "1h" },
      { id: "6h", label: "6h" },
      { id: "12h", label: "12h" },
      { id: "1d", label: "1d" },
    ]);
  });
});

describe("limits form <-> payload", () => {
  it("returns undefined (omit limits entirely) when every field is empty", () => {
    expect(buildLimitsPayload({ maxInputTokens: "", maxOutputTokens: "", maxRequestBodyBytes: "" })).toBeUndefined();
    expect(buildLimitsPayload(limitsToForm(undefined))).toBeUndefined();
  });

  it("maps stored limits into string fields and back", () => {
    const form = limitsToForm({ max_input_tokens: 4000, max_output_tokens: 2000, max_request_body_bytes: 1048576 });
    expect(form).toEqual({ maxInputTokens: "4000", maxOutputTokens: "2000", maxRequestBodyBytes: "1048576" });
    expect(buildLimitsPayload(form)).toEqual({ max_input_tokens: 4000, max_output_tokens: 2000, max_request_body_bytes: 1048576 });
  });

  it("keeps unset dimensions as undefined rather than zero", () => {
    expect(buildLimitsPayload({ maxInputTokens: "4000", maxOutputTokens: "", maxRequestBodyBytes: "" }))
      .toEqual({ max_input_tokens: 4000, max_output_tokens: undefined, max_request_body_bytes: undefined });
  });
});

describe("ip allowlist form <-> payload", () => {
  it("pads an empty list with one blank row for editing", () => {
    expect(ipAllowlistToForm(undefined)).toEqual([""]);
    expect(ipAllowlistToForm([])).toEqual([""]);
    expect(ipAllowlistToForm(["10.0.0.8"])).toEqual(["10.0.0.8"]);
  });

  it("trims and drops blank rows on submit", () => {
    expect(buildAccessListPayload([" 10.0.0.8 ", "", "   ", "192.168.0.0/24"])).toEqual(["10.0.0.8", "192.168.0.0/24"]);
    expect(buildAccessListPayload([""])).toEqual([]);
  });
});

describe("isValidIPOrCIDR", () => {
  it("accepts bare IPv4 and IPv6 addresses", () => {
    expect(isValidIPOrCIDR("10.0.0.8")).toBe(true);
    expect(isValidIPOrCIDR("127.0.0.1")).toBe(true);
    expect(isValidIPOrCIDR("::1")).toBe(true);
    expect(isValidIPOrCIDR("2001:db8::1")).toBe(true);
    expect(isValidIPOrCIDR("fe80::")).toBe(true);
  });

  it("accepts CIDR blocks with a prefix length in range for the family", () => {
    expect(isValidIPOrCIDR("192.168.0.0/24")).toBe(true);
    expect(isValidIPOrCIDR("10.0.0.0/8")).toBe(true);
    expect(isValidIPOrCIDR("2001:db8::/32")).toBe(true);
    expect(isValidIPOrCIDR("fe80::/10")).toBe(true);
  });

  it("rejects malformed addresses, out-of-range prefixes, and extra slashes", () => {
    expect(isValidIPOrCIDR("256.1.1.1")).toBe(false);
    expect(isValidIPOrCIDR("1.2.3")).toBe(false);
    expect(isValidIPOrCIDR("1.2.3.4.5")).toBe(false);
    expect(isValidIPOrCIDR("192.168.0.0/33")).toBe(false);
    expect(isValidIPOrCIDR("2001:db8::/129")).toBe(false);
    expect(isValidIPOrCIDR("192.168.0.0/24/16")).toBe(false);
    expect(isValidIPOrCIDR("2001:db8:::1")).toBe(false);
    expect(isValidIPOrCIDR(":::1")).toBe(false);
    expect(isValidIPOrCIDR("a:::b")).toBe(false);
    expect(isValidIPOrCIDR("::1:")).toBe(false);
    expect(isValidIPOrCIDR("12345::")).toBe(false);
    expect(isValidIPOrCIDR("1:2:3:4:5:6:7:8:9")).toBe(false);
    expect(isValidIPOrCIDR("abc")).toBe(false);
  });

  it("treats an empty or whitespace-only row as valid (blank rows are dropped at submit)", () => {
    expect(isValidIPOrCIDR("")).toBe(true);
    expect(isValidIPOrCIDR("   ")).toBe(true);
  });
});

describe("expiry presets", () => {
  it("resolves never to undefined and day offsets to a 'YYYY-MM-DD HH:MM:SS' timestamp", () => {
    expect(resolveExpiresAt("never")).toBeUndefined();
    const oneDay = resolveExpiresAt("1d");
    expect(oneDay).toMatch(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$/);
    expect(Date.parse(`${oneDay!.replace(" ", "T")}Z`) - Date.now()).toBeGreaterThan(23 * 60 * 60 * 1000);
    expect(Date.parse(`${oneDay!.replace(" ", "T")}Z`) - Date.now()).toBeLessThan(25 * 60 * 60 * 1000);
  });

  it("sends an empty string (clear to never) on update, not undefined (leave unchanged)", () => {
    expect(resolveExpiresAtForUpdate("never")).toBe("");
    expect(resolveExpiresAtForUpdate("30d")).toBe(resolveExpiresAt("30d"));
  });
});

describe("expiry display", () => {
  it("detects expired keys, normalizing both 'T'-separated and space-separated timestamps", () => {
    expect(isApiKeyExpired(undefined)).toBe(false);
    expect(isApiKeyExpired("2000-01-01T00:00:00")).toBe(true);
    expect(isApiKeyExpired("2000-01-01 00:00:00")).toBe(true);
    expect(isApiKeyExpired("2999-01-01T00:00:00")).toBe(false);
  });

  it("formats the expiry text and validity label", () => {
    expect(formatExpiresText(null, false)).toBe("Never");
    expect(formatExpiresText("2026-01-02T03:04:05Z", false)).toBe("2026-01-02 03:04:05");
    expect(formatValidityLabel(true, false)).toBe("Expired");
    expect(formatValidityLabel(false, false)).toBe("Valid");
  });
});

describe("digitsOnly", () => {
  it("strips every non-digit character", () => {
    expect(digitsOnly("12a3-4.5")).toBe("12345");
    expect(digitsOnly("abc")).toBe("");
  });
});
