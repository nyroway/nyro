import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

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

describe("dynamic quota editor", () => {
  it("supports adding/removing arbitrary requests and tokens quota rows", () => {
    expect(source).toContain('function renderGroup(kind: "requests" | "tokens", title: string)');
    expect(source).toContain('updateRows(kind, [...rows, { limit: "", window: "" }])');
    expect(source).toContain("updateRows(kind, rows.filter((_, i) => i !== idx))");
  });

  it("treats concurrency as a single value with no window", () => {
    const start = source.indexOf("function buildQuotasPayload");
    const end = source.indexOf("\nfunction quotaBadgeLabel", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate buildQuotasPayload");
    }
    const body = source.slice(start, end);

    expect(body).toContain('quotas.push({ quota_type: "concurrency", quota_limit: Number.parseInt(form.concurrency, 10) });');
  });

  it("renders one quota badge per entry in consumer.quotas, not a fixed set", () => {
    expect(source).toContain("function renderQuotaBadges(quotas: ConsumerQuota[] | undefined)");
    expect(source).toContain("return (quotas ?? []).map((q, idx) => {");
  });
});

describe("multi-key management", () => {
  it("adds a key via add_consumer_key and reveals the one-time token", () => {
    const start = source.indexOf("const addKeyMut = useMutation({");
    const end = source.indexOf("\n\n  const updateKeyMut", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate addKeyMut");
    }
    const body = source.slice(start, end);

    expect(body).toContain('backend<ConsumerKey>("add_consumer_key", { id: consumerId, input })');
    expect(body).toContain("openRevealDialog({ name: created.name, token: created.token })");
  });

  it("updates a key via update_consumer_key with the {id, keyId, input} shape", () => {
    expect(source).toContain('backend<ConsumerKey>("update_consumer_key", { id: consumerId, keyId, input })');
  });

  it("deletes a key via delete_consumer_key with the {id, keyId} shape", () => {
    expect(source).toContain('backend("delete_consumer_key", { id: consumerId, keyId })');
  });

  it("rotates a key by adding a new one and deleting the old one, then reveals the new token", () => {
    const start = source.indexOf("const rotateKeyMut = useMutation({");
    const end = source.indexOf("\n\n  const totalPages", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate rotateKeyMut");
    }
    const body = source.slice(start, end);

    expect(body).toContain('backend<ConsumerKey>("add_consumer_key"');
    expect(body).toContain('await backend("delete_consumer_key", { id: consumerId, keyId: key.id });');
    expect(body).toContain("openRevealDialog({ name: created.name, token: created.token })");
  });

  it("only ever copies the key_prefix for existing keys, never a full plaintext key", () => {
    const start = source.indexOf("async function copyKeyPrefix");
    const end = source.indexOf("\n  function renderQuotaBadges", start);
    if (start < 0 || end < 0) {
      throw new Error("Could not locate copyKeyPrefix");
    }
    const body = source.slice(start, end);

    expect(body).toContain("navigator.clipboard.writeText(key.key_prefix)");
  });
});
