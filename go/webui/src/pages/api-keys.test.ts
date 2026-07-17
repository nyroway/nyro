import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { formatKeyPreview } from "@/lib/format";

const source = readFileSync(resolve(__dirname, "api-keys.tsx"), "utf8");

describe("consumer route binding (P0 regression)", () => {
  it("builds routeOptions from route.model, not route.id", () => {
    const start = source.indexOf("const routeOptions = useMemo(");
    const end = source.indexOf("\n  );", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate routeOptions definition");
    }
    const body = source.slice(start, end);

    expect(body).toContain("value: route.model");
    expect(body).toContain("label: route.model");
    expect(body).not.toContain("value: route.id");
    expect(body).not.toContain("route.name");
  });

  it("submits routes as the model-name array from routeOptions, both on create and edit", () => {
    expect(source).toContain("routes: createForm.routes");
    expect(source).toContain("routes: editForm.routes");
  });
});

describe("access.protocols and access.ip_allowlist", () => {
  it("submits protocols from a fixed protocol option set, both on create and edit", () => {
    expect(source).toContain("import { PROTOCOL_TABLE } from \"@/lib/protocol\";");
    expect(source).toContain("protocols: createForm.protocols");
    expect(source).toContain("protocols: editForm.protocols");
  });

  it("edits ip_allowlist as one input row per entry, dropping blank rows on submit", () => {
    expect(source).toContain("function IPAllowlistEditor({");
    expect(source).toContain("ip_allowlist: buildAccessListPayload(createForm.ipAllowlist)");
    expect(source).toContain("ip_allowlist: buildAccessListPayload(editForm.ipAllowlist)");

    const start = source.indexOf("function buildAccessListPayload");
    const end = source.indexOf("\n}", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate buildAccessListPayload");
    }
    expect(source.slice(start, end)).toContain(".filter(Boolean)");
  });

  it("validates each ip_allowlist row as a bare IP or a CIDR block, and blocks submit when invalid", () => {
    expect(source).toContain("function isValidIPOrCIDR(value: string)");
    expect(source).toContain("createForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))");
    expect(source).toContain("editForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))");
  });
});

describe("consumer limits", () => {
  it("omits the limits payload entirely when every field is empty", () => {
    const start = source.indexOf("function buildLimitsPayload");
    const end = source.indexOf("\n}", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate buildLimitsPayload");
    }
    const body = source.slice(start, end);

    expect(body).toContain("if (!form.maxInputTokens && !form.maxOutputTokens && !form.maxRequestBodyBytes) return undefined;");
  });

  it("submits limits built from the form, both on create and edit", () => {
    expect(source).toContain("limits: buildLimitsPayload(createForm.limits)");
    expect(source).toContain("limits: buildLimitsPayload(editForm.limits)");
  });
});

describe("dynamic quota editor", () => {
  it("supports adding/removing arbitrary requests and tokens quota rows", () => {
    expect(source).toContain('function renderGroup(kind: "requests" | "tokens", title: string)');
    expect(source).toContain('updateRows(kind, [...rows, { limit: "", window: "" }])');
    expect(source).toContain("updateRows(kind, rows.filter((_, i) => i !== idx))");
  });

  it("only shows a delete button once there are 2+ rows; a lone row gets a clear button instead", () => {
    const start = source.indexOf("function renderGroup(kind:");
    const end = source.indexOf("\n  return (\n    <div className=\"grid grid-cols-2", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate renderGroup");
    }
    const body = source.slice(start, end);

    expect(body).toContain("rows.length > 1 ? (");
    expect(body).toContain('updateRows(kind, [{ limit: "", window: "" }])');
  });

  it("treats concurrency as a single value with no window", () => {
    const start = source.indexOf("function buildQuotasPayload");
    const end = source.indexOf("\nconst quotaBadgeColors", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate buildQuotasPayload");
    }
    const body = source.slice(start, end);

    expect(body).toContain('quotas.push({ quota_type: "concurrency", quota_limit: Number.parseInt(form.concurrency, 10) });');
  });

  it("renders exactly one badge per quota type, folding extra rules into a +N suffix instead of adding badges", () => {
    const start = source.indexOf("function renderQuotaBadges(quotas: ConsumerQuota[] | undefined)");
    const end = source.indexOf("\n  }", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate renderQuotaBadges");
    }
    const body = source.slice(start, end);

    expect(body).toContain("quotaSummaryOrder.flatMap(({ type, zh, en }, idx) => {");
    expect(body).toContain('const suffix = rows.length > 1 ? ` +${rows.length - 1}` : "";');
  });
});

describe("access permission summary badges", () => {
  it("shows one count badge per access dimension instead of one badge per bound item", () => {
    const start = source.indexOf("function renderAccessBadges(consumer: Consumer)");
    const end = source.indexOf("\n  }", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate renderAccessBadges");
    }
    const body = source.slice(start, end);

    expect(body).toContain("consumer.routes?.length ?? 0");
    expect(body).toContain("consumer.protocols?.length ?? 0");
    expect(body).toContain("consumer.ip_allowlist?.length ?? 0");
    expect(body).toContain(".filter((d) => d.count > 0)");
  });
});

describe("multi-key management", () => {
  it("adds a key via a dialog carrying name + validity, reveals the one-time token", () => {
    const start = source.indexOf("const addKeyMut = useMutation({");
    const end = source.indexOf("\n\n  const updateKeyMut", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate addKeyMut");
    }
    const body = source.slice(start, end);

    expect(body).toContain('backend<ConsumerKey>("add_consumer_key", { id: consumerId, input })');
    expect(body).toContain("openRevealDialog({ name: created.name, token: created.token })");
    expect(source).toContain("function openAddKeyDialog(consumer: Consumer)");
  });

  it("updates a key's name/expiry via update_consumer_key with the {id, keyId, input} shape", () => {
    expect(source).toContain('backend<ConsumerKey>("update_consumer_key", { id: consumerId, keyId, input })');
  });

  it("only submits expires_at from the edit dialog when the user actually touched the validity preset", () => {
    expect(source).toContain("expiresTouched: boolean");
    expect(source).toContain("if (editKeyForm.expiresTouched) {");
    expect(source).toContain("input.expires_at = resolveExpiresAtForUpdate(editKeyForm.expiresPreset);");
  });

  it("deletes a key via delete_consumer_key with the {id, keyId} shape", () => {
    expect(source).toContain('backend("delete_consumer_key", { id: consumerId, keyId })');
  });

  it("warns when deleting a consumer's only key", () => {
    const start = source.indexOf("title={isZh ? \"确认删除 Key\"");
    const end = source.indexOf("\n      />", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate the delete-key ConfirmDialog");
    }
    expect(source.slice(start, end)).toContain("keyToDelete.consumer.keys?.length ?? 0) <= 1");
  });

  it("regenerates a key under a throwaway temp name, then renames it back after the old key is deleted (consumer_keys has a UNIQUE(consumer_id, name) constraint, so adding under the same name while the old row still exists would violate it)", () => {
    const start = source.indexOf("const regenerateKeyMut = useMutation({");
    const end = source.indexOf("\n\n  const deleteKeyMut", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate regenerateKeyMut");
    }
    const body = source.slice(start, end);

    expect(body).toContain("const tempName = `${key.name}~regen~${crypto.randomUUID()}`;");
    expect(body).toContain('input: { name: tempName, expires_at: key.expires_at },');
    expect(body).toContain('await backend("delete_consumer_key", { id: consumerId, keyId: key.id });');
    expect(body).toContain('backend<ConsumerKey>("update_consumer_key", {');
    expect(body).toContain("input: { name: key.name },");
    expect(body).toContain("openRevealDialog({ name: created.name, token: created.token })");
  });

  it("displays the key_preview as plain text with no copy affordance (copying a masked preview has no real use)", () => {
    expect(source).not.toContain("copyKeyPreview");
    expect(source).not.toContain("copiedKeyId");
    expect(source).toContain("{formatKeyPreview(key.key_preview)}");
  });

  it("renders every key in consumer.keys, not just the first", () => {
    expect(source).toContain("keys.map((key) => renderKeyRow(consumer, key))");
  });

  it("has a + button in the list row to add a key, before the edit button", () => {
    const start = source.indexOf("onClick={() => toggleConsumerEnabledMut.mutate({ id: item.id");
    const editIdx = source.indexOf("onClick={() => startEdit(item)}", start);
    const addIdx = source.indexOf("onClick={() => openAddKeyDialog(item)}", start);
    if (start < 0 || editIdx < 0 || addIdx < 0) {
      throw new Error("Could not locate the list row action buttons");
    }
    expect(addIdx).toBeGreaterThan(start);
    expect(addIdx).toBeLessThan(editIdx);
  });

  it("masks the key preview to a fixed length regardless of the real key's length", () => {
    // formatKeyPreview is shared from @/lib/format; the mask run must be a fixed
    // length so it never hints at the real key's length.
    const maskOf = (s: string) => (formatKeyPreview(s).match(/\*+/) ?? [""])[0];
    const shortMask = maskOf("sk-abcdefghijklmnop"); // 19 chars
    const longMask = maskOf("sk-abcdefghijklmnopqrstuvwxyz0123456789"); // longer
    expect(shortMask.length).toBe(28);
    expect(longMask.length).toBe(28);
    expect(formatKeyPreview("sk-abcdefghijklmnop")).toBe(`sk-abcdef${"*".repeat(28)}klmnop`);
  });
});
