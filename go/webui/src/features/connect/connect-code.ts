import type { Consumer } from "@/lib/types";

export type CodeLanguage = "python" | "typescript" | "curl";
export type CodeProtocol = "openai-compatible" | "openai-responses" | "anthropic-messages" | "gemini-generatecontent";
export type SyntaxLanguage = "python" | "typescript" | "bash";

export type CodeProtocolOption = {
  id: CodeProtocol;
  name: string;
  iconKey: string;
  apiPath: string;
};

export const CODE_LANGS: CodeLanguage[] = ["python", "typescript", "curl"];

export const CODE_PROTOCOLS: CodeProtocolOption[] = [
  { id: "openai-compatible", name: "OpenAI Compatible", iconKey: "openai", apiPath: "/v1/chat/completions" },
  { id: "openai-responses", name: "OpenAI Responses", iconKey: "openai", apiPath: "/v1/responses" },
  { id: "anthropic-messages", name: "Anthropic Messages", iconKey: "anthropic", apiPath: "/v1/messages" },
  { id: "gemini-generatecontent", name: "Google Gemini", iconKey: "gemini", apiPath: "/v1beta/models/{model}:generateContent" },
];

// Environment variable the samples read the key from when the full key is not
// available to fill in (i.e. admin is not running with --plaintext-keys).
export const API_KEY_ENV = "NYRO_API_KEY";

// A consumer key flattened with its owning consumer's route grants, so the
// Connect page can decide which keys apply to the selected model. token is set
// only when the admin runs with --plaintext-keys (recoverable storage).
export type FlatKey = {
  id: string;
  name: string;
  keyPreview: string;
  token?: string;
  grantsAll: boolean;
  routes: string[];
};

export function flattenKeys(consumers: Consumer[]): FlatKey[] {
  const out: FlatKey[] = [];
  for (const c of consumers) {
    if (c.enabled === false) continue;
    const routes = c.routes ?? [];
    const grantsAll = routes.length === 0; // empty grant = access to all routes
    for (const k of c.keys ?? []) {
      if (k.enabled === false) continue;
      out.push({ id: k.id, name: k.name, keyPreview: k.key_preview, token: k.token, grantsAll, routes });
    }
  }
  return out;
}

export function protocolLabel(protocol: CodeProtocol) {
  return CODE_PROTOCOLS.find((option) => option.id === protocol)?.name ?? protocol;
}

export function languageLabel(language: CodeLanguage) {
  if (language === "python") return "Python";
  if (language === "typescript") return "TypeScript";
  return "cURL";
}

export function syntaxLanguage(language: CodeLanguage): SyntaxLanguage {
  if (language === "python") return "python";
  if (language === "typescript") return "typescript";
  return "bash";
}

function jsonText(input: unknown) {
  return JSON.stringify(input, null, 2);
}

function encodeGeminiModelForPath(model: string) {
  // Keep ":" readable for model variants like gemma3:1b.
  return encodeURIComponent(model).replace(/%3A/gi, ":");
}

export function codeTemplate(params: {
  protocol: CodeProtocol;
  model: string;
  host: string;
  language: CodeLanguage;
  stream: boolean;
  maxTokens?: number;
  // apiKeyLiteral is the full recoverable key to inline as a string literal;
  // when useEnvVar is true it is ignored and the sample reads the key from the
  // API_KEY_ENV environment variable instead.
  apiKeyLiteral: string;
  useEnvVar: boolean;
}) {
  const { protocol, model, host, language, stream, maxTokens, apiKeyLiteral, useEnvVar } = params;

  // Per-language rendering of the API key: an environment-variable reference
  // (non-plaintext mode) or an inlined string literal (recoverable key).
  const pyKey = useEnvVar ? `os.environ["${API_KEY_ENV}"]` : `"${apiKeyLiteral}"`;
  const tsKey = useEnvVar ? `process.env.${API_KEY_ENV}` : `"${apiKeyLiteral}"`;
  const shKey = useEnvVar ? `$${API_KEY_ENV}` : apiKeyLiteral;
  const pyOsImport = useEnvVar ? "import os\n" : "";

  // ── cURL ──────────────────────────────────────────────────────────────
  if (language === "curl") {
    const streamFlag = stream ? "-N \\\n  " : "";
    if (protocol === "openai-compatible") {
      const body: Record<string, unknown> = { model, messages: [{ role: "user", content: "Hello" }] };
      if (maxTokens) body.max_tokens = maxTokens;
      if (stream) body.stream = true;
      return `curl ${host}/v1/chat/completions \\
  ${streamFlag}-H "Authorization: Bearer ${shKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText(body)}'`;
    }
    if (protocol === "openai-responses") {
      const body: Record<string, unknown> = { model, input: "Hello" };
      if (maxTokens) body.max_output_tokens = maxTokens;
      if (stream) body.stream = true;
      return `curl ${host}/v1/responses \\
  ${streamFlag}-H "Authorization: Bearer ${shKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText(body)}'`;
    }
    if (protocol === "anthropic-messages") {
      const body: Record<string, unknown> = {
        model,
        max_tokens: maxTokens ?? 1024,
        messages: [{ role: "user", content: "Hello" }],
      };
      if (stream) body.stream = true;
      return `curl ${host}/v1/messages \\
  ${streamFlag}-H "x-api-key: ${shKey}" \\
      -H "anthropic-version: 2023-06-01" \\
      -H "Content-Type: application/json" \\
      -d '${jsonText(body)}'`;
    }
    const geminiBody: Record<string, unknown> = { contents: [{ role: "user", parts: [{ text: "Hello" }] }] };
    if (maxTokens) geminiBody.generationConfig = { maxOutputTokens: maxTokens };
    const method = stream ? "streamGenerateContent" : "generateContent";
    return `curl ${host}/v1beta/models/${encodeGeminiModelForPath(model)}:${method}${stream ? "?alt=sse" : ""} \\
  ${streamFlag}-H "x-goog-api-key: ${shKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText(geminiBody)}'`;
  }

  // ── Python ────────────────────────────────────────────────────────────
  if (language === "python") {
    if (protocol === "openai-compatible") {
      const kw = maxTokens ? `\n    max_tokens=${maxTokens},` : "";
      const head = `# pip install openai
${pyOsImport}from openai import OpenAI

client = OpenAI(
    api_key=${pyKey},
    base_url="${host}/v1"
)`;
      if (stream) {
        return `${head}

stream = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}],${kw}
    stream=True
)

for chunk in stream:
    print(chunk.choices[0].delta.content or "", end="", flush=True)`;
      }
      return `${head}

response = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}],${kw}
)

print(response.choices[0].message.content)`;
    }
    if (protocol === "openai-responses") {
      const kw = maxTokens ? `\n    max_output_tokens=${maxTokens},` : "";
      const head = `# pip install openai
${pyOsImport}from openai import OpenAI

client = OpenAI(
    api_key=${pyKey},
    base_url="${host}/v1"
)`;
      if (stream) {
        return `${head}

stream = client.responses.create(
    model="${model}",
    input="Hello",${kw}
    stream=True
)

for event in stream:
    if event.type == "response.output_text.delta":
        print(event.delta, end="", flush=True)`;
      }
      return `${head}

response = client.responses.create(
    model="${model}",
    input="Hello",${kw}
)

print(response.output_text)`;
    }
    if (protocol === "anthropic-messages") {
      const mt = maxTokens ?? 1024;
      const head = `# pip install anthropic
${pyOsImport}from anthropic import Anthropic

client = Anthropic(
    api_key=${pyKey},
    base_url="${host}"
)`;
      if (stream) {
        return `${head}

with client.messages.stream(
    model="${model}",
    max_tokens=${mt},
    messages=[{"role": "user", "content": "Hello"}]
) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)`;
      }
      return `${head}

response = client.messages.create(
    model="${model}",
    max_tokens=${mt},
    messages=[{"role": "user", "content": "Hello"}]
)

print(response.content[0].text)`;
    }
    const cfg = maxTokens ? `\n    config={"max_output_tokens": ${maxTokens}},` : "";
    const head = `# pip install google-genai
${pyOsImport}from google import genai

client = genai.Client(
    api_key=${pyKey},
    http_options={"base_url": "${host}"}
)`;
    if (stream) {
      return `${head}

for chunk in client.models.generate_content_stream(
    model="${model}",
    contents="Hello",${cfg}
):
    print(chunk.text, end="", flush=True)`;
    }
    return `${head}

response = client.models.generate_content(
    model="${model}",
    contents="Hello",${cfg}
)

print(response.text)`;
  }

  // ── TypeScript ──────────────────────────────────────────────────────────
  if (protocol === "openai-compatible") {
    const mt = maxTokens ? `\n  max_tokens: ${maxTokens},` : "";
    const head = `// npm install openai
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: ${tsKey},
  baseURL: "${host}/v1",
});`;
    if (stream) {
      return `${head}

const stream = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "Hello" }],${mt}
  stream: true,
});

for await (const chunk of stream) {
  process.stdout.write(chunk.choices[0]?.delta?.content ?? "");
}`;
    }
    return `${head}

const response = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "Hello" }],${mt}
});

console.log(response.choices[0]?.message?.content);`;
  }
  if (protocol === "openai-responses") {
    const mt = maxTokens ? `\n  max_output_tokens: ${maxTokens},` : "";
    const head = `// npm install openai
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: ${tsKey},
  baseURL: "${host}/v1",
});`;
    if (stream) {
      return `${head}

const stream = await client.responses.create({
  model: "${model}",
  input: "Hello",${mt}
  stream: true,
});

for await (const event of stream) {
  if (event.type === "response.output_text.delta") process.stdout.write(event.delta);
}`;
    }
    return `${head}

const response = await client.responses.create({
  model: "${model}",
  input: "Hello",${mt}
});

console.log(response.output_text);`;
  }
  if (protocol === "anthropic-messages") {
    const mt = maxTokens ?? 1024;
    const head = `// npm install @anthropic-ai/sdk
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  apiKey: ${tsKey},
  baseURL: "${host}",
});`;
    if (stream) {
      return `${head}

const stream = client.messages.stream({
  model: "${model}",
  max_tokens: ${mt},
  messages: [{ role: "user", content: "Hello" }],
});

for await (const event of stream) {
  if (event.type === "content_block_delta" && event.delta.type === "text_delta") {
    process.stdout.write(event.delta.text);
  }
}`;
    }
    return `${head}

const response = await client.messages.create({
  model: "${model}",
  max_tokens: ${mt},
  messages: [{ role: "user", content: "Hello" }],
});

console.log(response.content[0]);`;
  }
  const cfg = maxTokens ? `\n  config: { maxOutputTokens: ${maxTokens} },` : "";
  const head = `// npm install @google/genai
import { GoogleGenAI } from "@google/genai";
const client = new GoogleGenAI({
  apiKey: ${tsKey},
  baseUrl: "${host}",
});`;
  if (stream) {
    return `${head}

const stream = await client.models.generateContentStream({
  model: "${model}",
  contents: "Hello",${cfg}
});

for await (const chunk of stream) {
  process.stdout.write(chunk.text ?? "");
}`;
  }
  return `${head}

const response = await client.models.generateContent({
  model: "${model}",
  contents: "Hello",${cfg}
});

console.log(response.text);`;
}

// ── 轻量语法着色（§9.5 ①：降级为纯 pre + token 着色，移除
//    react-syntax-highlighter 依赖）─────────────────────────────────────
// 只覆盖三类示例代码里实际出现的形态：注释、字符串、数字、保留字。
// 字符串先于注释匹配，URL 里的 "//" 都在引号内，不会被当成 TS 注释。

export type CodeTokenKind = "comment" | "string" | "number" | "keyword";

export type CodeToken = {
  text: string;
  kind: CodeTokenKind | "plain";
};

const CODE_KEYWORDS: Record<SyntaxLanguage, ReadonlySet<string>> = {
  python: new Set([
    "from", "import", "as", "for", "in", "with", "if", "else", "elif",
    "True", "False", "None", "print", "return", "def", "not", "and", "or",
  ]),
  typescript: new Set([
    "import", "from", "const", "new", "await", "for", "of", "if", "else",
    "true", "false", "null", "undefined", "return", "export", "type",
  ]),
  bash: new Set(["curl"]),
};

const CODE_TOKEN_PATTERN =
  /(#[^\n]*|\/\/[^\n]*)|("(?:[^"\\\n]|\\.)*"|'(?:[^'\\\n]|\\.)*')|(\b\d+(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)/g;

export function tokenizeCode(code: string, language: SyntaxLanguage): CodeToken[] {
  const keywords = CODE_KEYWORDS[language];
  const tokens: CodeToken[] = [];
  let last = 0;
  for (const match of code.matchAll(CODE_TOKEN_PATTERN)) {
    const index = match.index ?? 0;
    if (index > last) tokens.push({ text: code.slice(last, index), kind: "plain" });
    const [text, comment, string, number, word] = match;
    if (comment) tokens.push({ text, kind: "comment" });
    else if (string) tokens.push({ text, kind: "string" });
    else if (number) tokens.push({ text, kind: "number" });
    else if (keywords.has(word)) tokens.push({ text, kind: "keyword" });
    else tokens.push({ text, kind: "plain" });
    last = index + text.length;
  }
  if (last < code.length) tokens.push({ text: code.slice(last), kind: "plain" });
  return tokens;
}
