import { describe, expect, it } from "vitest";
import { PROTOCOL_TABLE, protocolDisplayName, resolveProtocol } from "./protocol";

describe("protocol identity metadata", () => {
  it("uses the canonical three-column protocol identity table", () => {
    expect(PROTOCOL_TABLE).toEqual([
      {
        id: "anthropic-messages",
        displayName: "Anthropic Messages API",
        defaultBaseUrl: "https://api.anthropic.com",
      },
      {
        id: "openai-chat",
        displayName: "OpenAI Compatible API",
        defaultBaseUrl: "https://api.openai.com/v1",
      },
      {
        id: "openai-responses",
        displayName: "OpenAI Responses API",
        defaultBaseUrl: "https://api.openai.com/v1",
      },
      {
        id: "google-gemini",
        displayName: "Google Gemini API",
        defaultBaseUrl: "https://generativelanguage.googleapis.com",
      },
    ]);
    expect(PROTOCOL_TABLE.some((item) => "name" in item)).toBe(false);
  });

  it("resolves canonical aliases and rejects dropped legacy IDs", () => {
    expect(resolveProtocol("openai")).toBe("openai-chat");
    expect(resolveProtocol("codex")).toBe("openai-responses");
    expect(resolveProtocol("openai-resp")).toBe("openai-responses");
    expect(resolveProtocol("gemini")).toBe("google-gemini");
    expect(protocolDisplayName("codex")).toBe("OpenAI Responses API");
    expect(resolveProtocol("openai-chatcompletions")).toBeNull();
    expect(resolveProtocol("gemini-generatecontent")).toBeNull();
  });
});
