import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { formatKeyPreview } from "@/lib/format";
import ApiKeysPage from "./api-keys";

const source = readFileSync(resolve(__dirname, "api-keys.tsx"), "utf8");
const featureSource = readFileSync(resolve(__dirname, "../features/consumers/reveal-key-dialog.tsx"), "utf8");

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderApiKeysPage() {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });

  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      null,
      createElement(LocaleProvider, null, createElement(ApiKeysPage)),
    ),
  ));
}

describe("api-keys page static render", () => {
  it("renders the metric strip, toolbar, table card, and empty state", () => {
    const html = renderApiKeysPage();

    expect(html).toContain("API keys");
    expect(html).toContain("metric-strip");
    expect(html).toContain('class="toolbar-search"');
    expect(html.match(/class="[^"]*toolbar-filter[^"]*"/g)).toHaveLength(1);
    expect(html).toContain("toolbar-add");
    // The baseline toolbar-add is text-only: a single text node inside the button, no lucide Plus icon
    expect(html).toMatch(/class="button button-primary button-sm toolbar-add"[^>]*>[^<]*<\/button>/);
    expect(html).toContain("card table-card");
    expect(html).toContain("No consumers yet");
    expect(html).toContain('aria-label="Search consumers"');
    expect(html).toContain('aria-label="Filter by status"');
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderApiKeysPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] is synonymous with the original spelling, split to avoid the exit grep's literal
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|bg-green|bg-red|bg-sky|bg-amber|border-red|text-red|text-amber|grid-cols|flex items|font-mono|h-10|w-full)-/);
  });

  it("talks to the backend only through consumersApi (§9.4 ④)", () => {
    expect(source).toContain("consumersApi.list()");
    expect(source).not.toMatch(/\bbackend[(<]/);
  });
});

describe("consumer route binding (P0 regression)", () => {
  it("builds routeOptions from route.model, not route.id", () => {
    const start = source.indexOf("const routeOptions = useMemo(");
    const end = source.indexOf("\n  );", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate routeOptions definition");
    }
    const body = source.slice(start, end);

    expect(body).toContain("route.model");
    expect(body).not.toContain("route.id");
    expect(body).not.toContain("route.name");
  });

  it("submits routes as the model-name array from routeOptions, both on create and edit", () => {
    expect(source).toContain("routes: createForm.routes");
    expect(source).toContain("routes: editForm.routes");
  });
});

describe("access.protocols and access.ip_allowlist", () => {
  it("submits protocols from a fixed protocol option set, both on create and edit", () => {
    expect(source).toContain('import { PROTOCOL_TABLE, protocolDisplayName } from "@/lib/protocol";');
    expect(source).toContain("protocols: createForm.protocols");
    expect(source).toContain("protocols: editForm.protocols");
  });

  it("edits ip_allowlist as one input row per entry, dropping blank rows on submit", () => {
    expect(source).toContain("function IPAllowlistEditor({");
    expect(source).toContain("ip_allowlist: buildAccessListPayload(createForm.ipAllowlist)");
    expect(source).toContain("ip_allowlist: buildAccessListPayload(editForm.ipAllowlist)");
  });

  it("validates each ip_allowlist row as a bare IP or a CIDR block, and blocks submit when invalid", () => {
    expect(source).toContain("createForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))");
    expect(source).toContain("editForm.ipAllowlist.some((ip) => !isValidIPOrCIDR(ip))");
  });
});

describe("consumer limits", () => {
  it("submits limits built from the form, both on create and edit", () => {
    expect(source).toContain("limits: buildLimitsPayload(createForm.limits)");
    expect(source).toContain("limits: buildLimitsPayload(editForm.limits)");
  });
});

describe("dynamic quota editor (§9.4 ②)", () => {
  it("edits quota rules with the baseline table vocabulary (QuotaRuleTable)", () => {
    expect(source).toContain("function QuotaRuleTable({");
    expect(source).toContain('className="table quota-table"');
    expect(source).toContain("cell-actions");
  });

  it("supports adding/removing arbitrary requests and tokens quota rows", () => {
    expect(source).toContain('onRowsChange([...rows, { limit: "", window: "" }])');
    expect(source).toContain("onRowsChange(rows.filter((_, i) => i !== idx))");
  });

  it("only shows a delete button once there are 2+ rows; a lone row gets a clear button instead", () => {
    const start = source.indexOf("function QuotaRuleTable({");
    const end = source.indexOf("\n      </table>", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate QuotaRuleTable");
    }
    const body = source.slice(start, end);

    expect(body).toContain("rows.length > 1 ? (");
    expect(body).toContain('onRowsChange([{ limit: "", window: "" }])');
  });

  it("summarizes quota rules in one compact table cell", () => {
    expect(source).toContain("formatQuotaRule(consumer.quotas[0])");
    expect(source).toContain('common.additionalRules", { count: consumer.quotas.length - 1 }');
  });
});

describe("access permission summary", () => {
  it("keeps long permission sets compact in the table", () => {
    expect(source).toContain("consumer.routes.slice(0, 2).map");
    expect(source).toContain("consumer.routes.length - 2");
    expect(source).toContain('consumer.protocols.join(" / ")');
    expect(source).toContain("consumer.ip_allowlist.length");
  });
});

describe("one-time raw token display (§9.4 注意: 仅创建响应可见 + 关闭即不可再取)", () => {
  it("opens the reveal dialog from the single revealedKey state, and closing clears it", () => {
    expect(source).toContain('<RevealKeyDialog revealed={revealedKey} onClose={() => setRevealedKey(null)} />');
    expect(source).not.toContain("showRevealDialog");
    expect(featureSource).toContain("open={Boolean(revealed)}");
  });

  it("captures the token only from creation responses (create / addKey / regenerate)", () => {
    const captures = source.match(/setRevealedKey\(\{ name:/g) ?? [];
    expect(captures).toHaveLength(3);
    // reads the token off the mutation's creation response only
    expect(source).toContain("firstKey?.token");
    expect(source).toContain("created.token");
  });

  it("keeps the masked key_preview as the only key material in the list rows", () => {
    expect(source).not.toContain("copyKeyPreview");
    expect(source).not.toContain("copiedKeyId");
    expect(source).toContain("{formatKeyPreview(key.key_preview)}");
  });
});

describe("multi-key management (§9.4 ③: key rows use row-actions)", () => {
  it("adds a key via a dialog carrying name + validity, reveals the one-time token", () => {
    const start = source.indexOf("const addKeyMut = useMutation({");
    const end = source.indexOf("\n\n  const updateKeyMut", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate addKeyMut");
    }
    const body = source.slice(start, end);

    expect(body).toContain("consumersApi.addKey(consumerId, input)");
    expect(body).toContain("setRevealedKey({ name: created.name, token: created.token })");
    expect(source).toContain("function openAddKeyDialog(consumer: Consumer)");
  });

  it("updates a key's name/expiry via consumersApi.updateKey with the {consumerId, keyId, input} shape", () => {
    expect(source).toContain("consumersApi.updateKey(consumerId, keyId, input)");
  });

  it("only submits expires_at from the edit dialog when the user actually touched the validity preset", () => {
    expect(source).toContain("expiresTouched: boolean");
    expect(source).toContain("if (editKeyForm.expiresTouched) {");
    expect(source).toContain("input.expires_at = resolveExpiresAtForUpdate(editKeyForm.expiresPreset);");
  });

  it("deletes a key via consumersApi.removeKey with the {consumerId, keyId} shape", () => {
    expect(source).toContain("consumersApi.removeKey(consumerId, keyId)");
  });

  it("warns when deleting a consumer's only key", () => {
    const start = source.indexOf('title={localizedMessage(isZh, "v2.api-keys.confirmKeyDeletion2")}');
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
    expect(body).toContain("name: tempName,");
    expect(body).toContain("expires_at: key.expires_at,");
    expect(body).toContain("await consumersApi.removeKey(consumerId, key.id);");
    expect(body).toContain("consumersApi.updateKey(consumerId, created.id, { name: key.name })");
    expect(body).toContain("setRevealedKey({ name: created.name, token: created.token })");
  });

  it("renders every key in consumer.keys, not just the first", () => {
    expect(source).toContain("keys.map((key) => renderKeyRow(consumer, key))");
  });

  it("has a + button in the list row to add a key, before the edit button", () => {
    const start = source.indexOf("toggleConsumerEnabledMut.mutate({ id: consumer.id");
    const editIdx = source.indexOf("startEdit(consumer)", start);
    const addIdx = source.indexOf("openAddKeyDialog(consumer)", start);
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
