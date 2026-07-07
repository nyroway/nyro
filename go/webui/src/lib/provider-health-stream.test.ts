import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { decodeProviderHealthSSEFrame, decodeRouteImportSSEFrame } from "./backend";

const source = readFileSync(resolve(__dirname, "backend.ts"), "utf8");

describe("provider draft health SSE decoding", () => {
  it("decodes health events from SSE frames", () => {
    const event = decodeProviderHealthSSEFrame([
      "event: health",
      'data: {"type":"check","check":"model_request","status":"passed","model":"gpt-test"}',
    ].join("\n"));

    expect(event).toEqual({
      type: "check",
      check: "model_request",
      status: "passed",
      model: "gpt-test",
    });
  });

  it("ignores frames without JSON data", () => {
    expect(decodeProviderHealthSSEFrame("event: ping")).toBeNull();
  });
});

describe("provider health stream endpoint", () => {
  it("uses the saved provider test URL directly", () => {
    expect(source).toContain("`/api/v1/upstreams/${id}/test`");
    expect(source).toContain("`/api/v1/upstreams/${id}/routes/import/stream`");
    expect(source).toContain("`${base}/upstreams/${args?.id}/routes/import/preview`");
    expect(source).not.toContain("/test/stream");
    expect(source).not.toContain("case \"test_provider\"");
    expect(source).not.toContain("case \"copy_provider\"");
  });

  it("maps provider model discovery to the Go upstream models endpoint", () => {
    expect(source).toContain("case \"get_provider_models\"");
    expect(source).toContain("`${base}/upstreams/${args?.id}/models`");
  });
});

describe("route import SSE decoding", () => {
  it("decodes route import events from SSE frames", () => {
    const event = decodeRouteImportSSEFrame([
      "event: route_import",
      'data: {"type":"route","model":"gpt-test","status":"created"}',
    ].join("\n"));

    expect(event).toEqual({
      type: "route",
      model: "gpt-test",
      status: "created",
    });
  });
});
