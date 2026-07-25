import { describe, expect, it } from "vitest";
import { PROTOCOL_TABLE, protocolDisplayName, resolveProtocol } from "./protocol";

describe("protocol identity metadata", () => {
  it("uses the canonical three-column protocol identity table", () => {
    expect(PROTOCOL_TABLE).toEqual([
      {
        id: "anthropic-messages",
        displayName: "Anthropic Messages",
        defaultBaseUrl: "https://api.anthropic.com",
      },
      {
        id: "openai-chatcompletions",
        displayName: "OpenAI Chat Completions",
        defaultBaseUrl: "https://api.openai.com/v1",
      },
      {
        id: "openai-responses",
        displayName: "OpenAI Responses",
        defaultBaseUrl: "https://api.openai.com/v1",
      },
      {
        id: "gemini-generatecontent",
        displayName: "Gemini generateContent",
        defaultBaseUrl: "https://generativelanguage.googleapis.com",
      },
    ]);
    expect(PROTOCOL_TABLE.some((item) => "name" in item)).toBe(false);
  });

  it("resolves the one frozen alias per protocol and rejects dropped IDs", () => {
    expect(resolveProtocol("openai")).toBe("openai-chatcompletions");
    expect(resolveProtocol("codex")).toBe("openai-responses");
    expect(resolveProtocol("claude")).toBe("anthropic-messages");
    expect(resolveProtocol("gemini")).toBe("gemini-generatecontent");
    expect(protocolDisplayName("codex")).toBe("OpenAI Responses");
    // Retired in the family/api rename.
    expect(resolveProtocol("openai-chat")).toBeNull();
    expect(resolveProtocol("google-gemini")).toBeNull();
    // Retired mechanically-derived aliases — one alias per protocol now.
    expect(resolveProtocol("openai-resp")).toBeNull();
    expect(resolveProtocol("openai-embed")).toBeNull();
  });
});
