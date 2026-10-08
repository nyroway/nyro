import { describe, expect, it } from "vitest";

import { codeTemplate, tokenizeCode } from "./connect-code";

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

describe("tokenizeCode", () => {
  it("keeps every character of the source in order", () => {
    const samples = [
      codeTemplate({ protocol: "anthropic-messages", model: "claude-sonnet", host: "https://gateway.example.com", language: "python", stream: true, apiKeyLiteral: "nyro-secret", useEnvVar: false }),
      codeTemplate({ protocol: "openai-compatible", model: "gpt-5", host: "http://127.0.0.1:19530", language: "typescript", stream: false, maxTokens: 512, apiKeyLiteral: "", useEnvVar: true }),
      codeTemplate({ protocol: "gemini-generatecontent", model: "team/gemma3:1b", host: "https://gateway.example.com", language: "curl", stream: true, apiKeyLiteral: "nyro-secret", useEnvVar: false }),
    ];

    for (const code of samples) {
      const tokens = tokenizeCode(code, "bash");
      expect(tokens.map((token) => token.text).join("")).toBe(code);
    }
  });

  it("colors comments, strings, numbers, and keywords", () => {
    const tokens = tokenizeCode(
      `# pip install openai\nfrom openai import OpenAI\n\nclient = OpenAI(api_key="nyro-secret", max_tokens=1024)`,
      "python",
    );

    expect(tokens).toContainEqual({ text: "# pip install openai", kind: "comment" });
    expect(tokens).toContainEqual({ text: "from", kind: "keyword" });
    expect(tokens).toContainEqual({ text: "import", kind: "keyword" });
    expect(tokens).toContainEqual({ text: '"nyro-secret"', kind: "string" });
    expect(tokens).toContainEqual({ text: "1024", kind: "number" });
    expect(tokens).toContainEqual({ text: "OpenAI", kind: "plain" });
  });

  it("does not treat URLs inside strings as line comments", () => {
    const tokens = tokenizeCode(
      `// npm install openai\nimport OpenAI from "openai";\nconst baseURL = "http://127.0.0.1:19530/v1";`,
      "typescript",
    );

    expect(tokens).toContainEqual({ text: '"http://127.0.0.1:19530/v1"', kind: "string" });
    expect(tokens).toContainEqual({ text: "// npm install openai", kind: "comment" });
    expect(tokens.filter((token) => token.kind === "comment")).toHaveLength(1);
  });
});
