import { describe, expect, it } from "vitest";
import { decodeProviderHealthSSEFrame } from "./backend";

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
