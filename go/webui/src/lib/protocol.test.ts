import { describe, expect, it } from "vitest";
import { protocolDisplayName, resolveProtocol } from "./protocol";

// The protocol table, the alias set, and the rejected-identifier list are all
// asserted against internal/llm/protocol/protocols.json in
// protocol.contract.test.ts. What
// is left here is the behaviour around them: how resolveProtocol normalises its
// input, and what the accessors return for input that is not a protocol at all.

describe("resolveProtocol normalisation", () => {
  it.each([null, undefined, "", "   "])("returns null for empty input %p", (raw) => {
    expect(resolveProtocol(raw)).toBeNull();
  });

  it("trims and lowercases before resolving", () => {
    expect(resolveProtocol("  OpenAI  ")).toBe("openai-chatcompletions");
  });
});

describe("protocolDisplayName", () => {
  it("returns null for an unrecognised or empty string", () => {
    expect(protocolDisplayName("not-a-protocol")).toBeNull();
    expect(protocolDisplayName(null)).toBeNull();
    expect(protocolDisplayName("")).toBeNull();
  });
});
