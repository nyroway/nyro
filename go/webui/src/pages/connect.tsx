import { Suspense, lazy, useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Code2, Copy } from "lucide-react";

import { backend } from "@/lib/backend";
import type { Consumer, Route } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { formatKeyPreview } from "@/lib/format";
import { Combobox } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import { ProviderIcon } from "@/components/ui/provider-icon";

const CodeHighlighter = lazy(() => import("@/components/ui/code-highlighter"));

const PUBLIC_GATEWAY_URL_KEY = "gateway.public_url";
const DEFAULT_MAX_TOKENS = "1024";
// Fixed local base_url used in samples when gateway.public_url is not set.
const DEFAULT_LOCAL_BASE_URL = "http://127.0.0.1:19530";
// Environment variable the samples read the key from when the full key is not
// available to fill in (i.e. admin is not running with --plaintext-keys).
const API_KEY_ENV = "NYRO_API_KEY";

type CodeLanguage = "python" | "typescript" | "curl";
type CodeProtocol = "openai-compatible" | "openai-responses" | "anthropic-messages" | "google-gemini";

type CodeProtocolOption = {
  id: CodeProtocol;
  name: string;
  iconKey: string;
  apiPath: string;
};

const CODE_LANGS: CodeLanguage[] = ["python", "typescript", "curl"];
const CODE_PROTOCOLS: CodeProtocolOption[] = [
  { id: "openai-compatible", name: "OpenAI Compatible", iconKey: "openai", apiPath: "/v1/chat/completions" },
  { id: "openai-responses", name: "OpenAI Responses", iconKey: "openai", apiPath: "/v1/responses" },
  { id: "anthropic-messages", name: "Anthropic Messages", iconKey: "anthropic", apiPath: "/v1/messages" },
  { id: "google-gemini", name: "Google Gemini", iconKey: "gemini", apiPath: "/v1beta/models/{model}:generateContent" },
];

// A consumer key flattened with its owning consumer's route grants, so the
// Connect page can decide which keys apply to the selected model. token is set
// only when the admin runs with --plaintext-keys (recoverable storage).
type FlatKey = {
  id: string;
  name: string;
  keyPreview: string;
  token?: string;
  grantsAll: boolean;
  routes: string[];
};

function flattenKeys(consumers: Consumer[]): FlatKey[] {
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

function protocolLabel(protocol: CodeProtocol) {
  if (protocol === "openai-compatible") return "OpenAI Compatible";
  if (protocol === "openai-responses") return "OpenAI Responses";
  if (protocol === "anthropic-messages") return "Anthropic Messages";
  return "Google Gemini";
}

function jsonText(input: unknown) {
  return JSON.stringify(input, null, 2);
}

function encodeGeminiModelForPath(model: string) {
  // Keep ":" readable for model variants like gemma3:1b.
  return encodeURIComponent(model).replace(/%3A/gi, ":");
}

function syntaxLanguage(language: CodeLanguage) {
  if (language === "python") return "python";
  if (language === "typescript") return "typescript";
  return "bash";
}

function languageLabel(language: CodeLanguage) {
  if (language === "python") return "Python";
  if (language === "typescript") return "TypeScript";
  return "cURL";
}

function codeTemplate(params: {
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

export default function ConnectPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const [codeLang, setCodeLang] = useState<CodeLanguage>("python");
  const [selectedProtocol, setSelectedProtocol] = useState<CodeProtocol>("openai-compatible");
  const [selectedRouteId, setSelectedRouteId] = useState("");
  const [selectedKeyId, setSelectedKeyId] = useState("");
  const [stream, setStream] = useState(false);
  const [maxTokensInput, setMaxTokensInput] = useState(DEFAULT_MAX_TOKENS);
  const [copied, setCopied] = useState(false);
  const [isDarkTheme, setIsDarkTheme] = useState(false);

  const { data: routes = [] } = useQuery<Route[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_routes"),
  });
  const { data: consumers = [] } = useQuery<Consumer[]>({
    queryKey: ["consumers"],
    queryFn: () => backend("list_consumers"),
  });
  const { data: publicUrl } = useQuery<string | null>({
    queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY],
    queryFn: () => backend("get_setting", { key: PUBLIC_GATEWAY_URL_KEY }),
  });

  useEffect(() => {
    const root = document.documentElement;
    const syncTheme = () => setIsDarkTheme(root.getAttribute("data-theme") === "dark");
    syncTheme();
    const observer = new MutationObserver(syncTheme);
    observer.observe(root, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);

  useEffect(() => {
    if (selectedRouteId && !routes.some((r) => r.id === selectedRouteId)) {
      setSelectedRouteId("");
    }
  }, [routes, selectedRouteId]);

  const selectedRoute = useMemo(
    () => routes.find((r) => r.id === selectedRouteId) ?? null,
    [routes, selectedRouteId],
  );

  const flatKeys = useMemo(() => flattenKeys(consumers), [consumers]);
  // Plaintext mode is inferred from the presence of a recoverable token on any
  // key (only admin --plaintext-keys populates it). It gates both the key
  // dropdown and whether the sample can inline a real key.
  const plaintextMode = useMemo(() => flatKeys.some((k) => !!k.token), [flatKeys]);
  const availableKeys = useMemo(() => {
    if (!selectedRoute) return [];
    return flatKeys.filter((k) => k.grantsAll || k.routes.includes(selectedRoute.model));
  }, [flatKeys, selectedRoute]);

  const showKeyPicker = Boolean(selectedRoute?.enable_auth) && plaintextMode;

  useEffect(() => {
    if (!showKeyPicker) {
      setSelectedKeyId("");
      return;
    }
    if (selectedKeyId && !availableKeys.some((k) => k.id === selectedKeyId)) {
      setSelectedKeyId("");
    }
  }, [availableKeys, selectedKeyId, showKeyPicker]);

  const selectedKey = useMemo(
    () => availableKeys.find((k) => k.id === selectedKeyId) ?? null,
    [availableKeys, selectedKeyId],
  );

  // base_url: a configured public URL wins; otherwise a fixed local address.
  const trimmedPublicUrl = (publicUrl ?? "").trim().replace(/\/+$/, "");
  const host = trimmedPublicUrl || DEFAULT_LOCAL_BASE_URL;

  // Inline a real key only when plaintext storage exposed one for the selected
  // key; otherwise the sample reads it from an environment variable.
  const realKey = showKeyPicker ? selectedKey?.token ?? "" : "";
  const useEnvVar = realKey === "";

  const parsedMaxTokens = (() => {
    const n = Number.parseInt(maxTokensInput, 10);
    return Number.isFinite(n) && n > 0 ? n : undefined;
  })();

  const generatedCode = codeTemplate({
    protocol: selectedProtocol,
    model: selectedRoute?.model ?? "gpt-4o",
    host,
    language: codeLang,
    stream,
    maxTokens: parsedMaxTokens,
    apiKeyLiteral: realKey,
    useEnvVar,
  });

  async function copyCode() {
    try {
      await navigator.clipboard.writeText(generatedCode);
      setCopied(true);
      setTimeout(() => setCopied(false), 1200);
    } catch {
      // Clipboard may be unavailable (insecure context); ignore silently.
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "接入" : "Connect"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh
            ? "通过代码将应用连接到 Nyro Gateway（支持流式与非流式）"
            : "Connect your app to Nyro Gateway via code (streaming and non-streaming)"}
        </p>
      </div>

      <div className="connect-panel glass rounded-2xl p-5 space-y-4">
        <div className="space-y-2">
          <p className="ml-1 text-xs leading-none font-normal text-slate-900">
            {isZh ? "选择接入协议" : "Select Ingress Protocol"}
          </p>
          <div className="grid grid-cols-2 gap-2">
            {CODE_PROTOCOLS.map((protocol) => {
              const active = protocol.id === selectedProtocol;
              return (
                <button
                  key={protocol.id}
                  type="button"
                  onClick={() => setSelectedProtocol(protocol.id)}
                  data-state={active ? "on" : "off"}
                  className="provider-preset-card h-auto rounded-xl px-3 py-2.5 text-left"
                >
                  <div className="flex items-start gap-2.5">
                    <div className="inline-flex h-9 w-9 items-center justify-center">
                      <ProviderIcon
                        iconKey={protocol.iconKey}
                        name={protocol.name}
                        protocol={protocol.id}
                        size={30}
                        className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                      />
                      <ProviderIcon
                        iconKey={protocol.iconKey}
                        name={protocol.name}
                        protocol={protocol.id}
                        size={30}
                        monochrome
                        className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                      />
                    </div>
                    <div className="min-w-0">
                      <p className="text-sm leading-tight font-semibold text-slate-900">{protocol.name}</p>
                      <p className="mt-1 text-xs text-slate-500 break-all">{protocol.apiPath}</p>
                    </div>
                  </div>
                </button>
              );
            })}
          </div>
        </div>

        <div className="connect-cli-shell rounded-xl border p-4 space-y-3">
          <div className="flex flex-wrap items-center gap-2">
            <Code2 className="h-4 w-4 text-slate-600" />
            <p className="text-sm font-semibold text-slate-900">{protocolLabel(selectedProtocol)}</p>
            <Badge variant="outline" className="connect-label-badge">
              {CODE_PROTOCOLS.find((p) => p.id === selectedProtocol)?.apiPath}
            </Badge>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 items-start">
            <div className="space-y-2">
              <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                {isZh ? "选择模型" : "Select Model"}
              </p>
              <Combobox
                className="bg-white"
                value={selectedRouteId}
                onValueChange={setSelectedRouteId}
                options={routes.map((r) => ({ value: r.id, label: r.model }))}
                placeholder={
                  routes.length > 0
                    ? isZh ? "选择模型" : "Select model"
                    : isZh ? "请先创建模型" : "Create a model first"
                }
                searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                emptyText={isZh ? "暂无可选模型" : "No models available"}
              />
            </div>

            {showKeyPicker && (
              <div className="space-y-2">
                <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                  {isZh ? "选择 API Key" : "Select API Key"}
                </p>
                <Combobox
                  className="bg-white"
                  value={selectedKeyId}
                  onValueChange={setSelectedKeyId}
                  options={availableKeys.map((k) => ({
                    value: k.id,
                    label: `${k.name} · ${formatKeyPreview(k.keyPreview)}`,
                  }))}
                  placeholder={isZh ? "选择 API Key" : "Select API key"}
                  searchPlaceholder={isZh ? "搜索 API Key..." : "Search API keys..."}
                  emptyText={isZh ? "暂无可选 API Key" : "No API keys available"}
                />
              </div>
            )}
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 items-start">
            <div className="space-y-2">
              <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                {isZh ? "最大 Token 数" : "Max tokens (max_tokens)"}
              </p>
              <Input
                type="number"
                min={1}
                inputMode="numeric"
                className="bg-white"
                value={maxTokensInput}
                onChange={(e) => setMaxTokensInput(e.target.value)}
                placeholder={isZh ? "默认 1024" : "Default 1024"}
              />
            </div>
            <div className="space-y-2">
              <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                {isZh ? "输出方式" : "Output Mode"}
              </p>
              <label className="flex cursor-pointer items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
                <span className="text-xs text-slate-600">
                  {stream
                    ? isZh ? "流式：逐块返回" : "Streaming: incremental chunks (stream)"
                    : isZh ? "非流式：一次性返回" : "Non-streaming: single response"}
                </span>
                <Switch checked={stream} onCheckedChange={setStream} />
              </label>
            </div>
          </div>

          {selectedRoute ? (
            <div className="space-y-2">
              <div className="connect-code-tabs flex gap-1">
                {CODE_LANGS.map((lang) => (
                  <button
                    key={lang}
                    onClick={() => setCodeLang(lang)}
                    className={`connect-code-tab-btn px-3 py-2 text-xs font-medium transition-colors cursor-pointer ${
                      codeLang === lang ? "is-active" : ""
                    }`}
                  >
                    {languageLabel(lang)}
                  </button>
                ))}
              </div>

              <div className="connect-code-example-box relative rounded-xl p-4">
                <button
                  onClick={copyCode}
                  className="connect-code-copy-btn absolute top-3 right-3 rounded-xl p-3 cursor-pointer transition-colors"
                  title={isZh ? "复制代码" : "Copy code"}
                >
                  {copied ? <Check className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
                </button>
                <Suspense fallback={<pre className="overflow-x-auto text-xs text-slate-500">{generatedCode}</pre>}>
                  <CodeHighlighter code={generatedCode} language={syntaxLanguage(codeLang)} dark={isDarkTheme} padding={0} />
                </Suspense>
              </div>
            </div>
          ) : (
            <p className="text-xs text-amber-600">
              {isZh ? "请先选择模型以生成代码示例。" : "Select a model first to generate code samples."}
            </p>
          )}

          {selectedRoute && useEnvVar && (
            <p className="text-xs text-slate-500">
              {isZh
                ? `示例从环境变量 ${API_KEY_ENV} 读取密钥；如需回填完整 Key，请以 --plaintext-keys 启动 admin。`
                : `The sample reads the key from the ${API_KEY_ENV} environment variable; start admin with --plaintext-keys to inline the full key.`}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
