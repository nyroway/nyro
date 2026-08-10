import { describe, expect, it } from "vitest";

import { codeTemplate } from "@/pages/connect";

describe("connect code samples", () => {
  it("uses the environment variable when a raw key is unavailable", () => {
    const code = codeTemplate({
      protocol: "openai-compatible",
      model: "gpt-5",
      host: "http://127.0.0.1:19530",
      language: "curl",
      stream: false,
      maxTokens: 1024,
      apiKeyLiteral: "",
      useEnvVar: true,
    });

    expect(code).toContain("$NYRO_API_KEY");
    expect(code).not.toContain("undefined");
  });

  it("encodes Gemini model path segments while preserving variant separators", () => {
    const code = codeTemplate({
      protocol: "gemini-generatecontent",
      model: "team/gemma3:1b",
      host: "https://gateway.example.com",
      language: "curl",
      stream: true,
      apiKeyLiteral: "nyro-secret",
      useEnvVar: false,
    });

    expect(code).toContain("team%2Fgemma3:1b:streamGenerateContent?alt=sse");
    expect(code).toContain("x-goog-api-key: nyro-secret");
  });
});
