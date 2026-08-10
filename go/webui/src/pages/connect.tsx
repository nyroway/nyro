import { Suspense, lazy, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";

import { backend } from "@/lib/backend";
import type { Consumer, Route } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { formatKeyPreview } from "@/lib/format";
import { Combobox } from "@/components/ui/combobox";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { Notice } from "@/components/v2/notice";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { Status } from "@/components/v2/status";
import { Surface } from "@/components/v2/surface";
import { localizedMessage } from "@/lib/messages";

const CodeHighlighter = lazy(() => import("@/components/ui/code-highlighter"));

const PUBLIC_GATEWAY_URL_KEY = "gateway.public_url";
const DEFAULT_MAX_TOKENS = "1024";
// Fixed local base_url used in samples when gateway.public_url is not set.
const DEFAULT_LOCAL_BASE_URL = "http://127.0.0.1:19530";
// Environment variable the samples read the key from when the full key is not
// available to fill in (i.e. admin is not running with --plaintext-keys).
const API_KEY_ENV = "NYRO_API_KEY";

type CodeLanguage = "python" | "typescript" | "curl";
type CodeProtocol = "openai-compatible" | "openai-responses" | "anthropic-messages" | "gemini-generatecontent";

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
  { id: "gemini-generatecontent", name: "Google Gemini", iconKey: "gemini", apiPath: "/v1beta/models/{model}:generateContent" },
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

// Exported for deterministic sample-generation tests; the page remains the only runtime consumer.
// eslint-disable-next-line react-refresh/only-export-components
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

export default function ConnectPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";

  const [codeLang, setCodeLang] = useState<CodeLanguage>("python");
  const [selectedProtocol, setSelectedProtocol] = useState<CodeProtocol>("openai-compatible");
  const [selectedRouteId, setSelectedRouteId] = useState("");
  const [selectedKeyId, setSelectedKeyId] = useState("");
  const [stream, setStream] = useState(false);
  const [maxTokensInput, setMaxTokensInput] = useState(DEFAULT_MAX_TOKENS);
  const [copied, setCopied] = useState(false);

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

  const effectiveRouteID = routes.some((route) => route.id === selectedRouteId) ? selectedRouteId : "";
  const selectedRoute = useMemo(
    () => routes.find((r) => r.id === effectiveRouteID) ?? null,
    [routes, effectiveRouteID],
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

  const effectiveKeyID = showKeyPicker && availableKeys.some((key) => key.id === selectedKeyId) ? selectedKeyId : "";
  const selectedKey = useMemo(
    () => availableKeys.find((k) => k.id === effectiveKeyID) ?? null,
    [availableKeys, effectiveKeyID],
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
    <PageLayout header={<PageHeader title={t("page.connect.title")} description={t("page.connect.subtitle")} />}>
      <Notice title={localizedMessage(isZh, "v2.connect.gatewayEndpoint")}>
        <code>{host}</code>{localizedMessage(isZh, "v2.connect.samplesReadTheKeyFromAnEnvironmentVariable")}
      </Notice>

      <div className="v2-connect-workspace">
        <Surface className="v2-connect-config" title={localizedMessage(isZh, "v2.connect.requestConfiguration")} description={localizedMessage(isZh, "v2.connect.changesUpdateTheCodeSampleImmediately")}>
          <div className="v2-connect-form">
            <div className="v2-connect-field">
              <label>{localizedMessage(isZh, "v2.connect.ingressProtocol")}</label>
              <div className="v2-protocol-options">
                {CODE_PROTOCOLS.map((protocol) => (
                  <button key={protocol.id} type="button" className={protocol.id === selectedProtocol ? "active" : ""} onClick={() => setSelectedProtocol(protocol.id)}>
                    <ProviderIcon iconKey={protocol.iconKey} name={protocol.name} protocol={protocol.id} size={22} />
                    <span><strong>{protocol.name}</strong><code>{protocol.apiPath}</code></span>
                  </button>
                ))}
              </div>
            </div>
            <div className="v2-connect-field">
              <label>{localizedMessage(isZh, "v2.connect.model")}</label>
              <Combobox
                value={effectiveRouteID}
                onValueChange={(value) => { setSelectedRouteId(value); setSelectedKeyId(""); }}
                options={routes.filter((route) => route.enabled).map((route) => ({ value: route.id, label: route.model }))}
                placeholder={routes.length ? (localizedMessage(isZh, "v2.connect.selectModel")) : (localizedMessage(isZh, "v2.connect.createAModelFirst"))}
                searchPlaceholder={localizedMessage(isZh, "v2.connect.searchModels")}
                emptyText={localizedMessage(isZh, "v2.connect.noModelsAvailable")}
              />
              <small>{localizedMessage(isZh, "v2.connect.onlyEnabledModelRoutesAreShown")}</small>
            </div>
            {showKeyPicker ? (
              <div className="v2-connect-field">
                <label>API Key</label>
                <Combobox value={effectiveKeyID} onValueChange={setSelectedKeyId} options={availableKeys.map((key) => ({ value: key.id, label: `${key.name} · ${formatKeyPreview(key.keyPreview)}` }))} placeholder={localizedMessage(isZh, "v2.connect.selectApiKey")} searchPlaceholder={localizedMessage(isZh, "v2.connect.searchApiKeys")} emptyText={localizedMessage(isZh, "v2.connect.noApiKeysAvailable")} />
                <small>{localizedMessage(isZh, "v2.connect.theKeyMustBeAllowedToAccessThe")}</small>
              </div>
            ) : (
              <Notice title={localizedMessage(isZh, "v2.connect.apiKey")}>{localizedMessage(isZh, "connect.keyFromEnvironment", { name: API_KEY_ENV })}</Notice>
            )}
            <div className="v2-connect-grid">
              <div className="v2-connect-field"><label>{localizedMessage(isZh, "v2.api-keys.maxOutputTokens")}</label><Input type="number" min={1} inputMode="numeric" value={maxTokensInput} onChange={(event) => setMaxTokensInput(event.target.value)} /></div>
              <div className="v2-connect-field"><label>{localizedMessage(isZh, "v2.connect.streaming")}</label><div className="v2-connect-switch"><span>{stream ? (localizedMessage(isZh, "v2.connect.enabled")) : (localizedMessage(isZh, "v2.settings.disabled"))}</span><Switch checked={stream} onCheckedChange={setStream} /></div></div>
            </div>
          </div>
        </Surface>

        <section className="v2-code-surface">
          <header>
            <div className="v2-code-tabs">{CODE_LANGS.map((language) => <button type="button" key={language} className={codeLang === language ? "active" : ""} onClick={() => setCodeLang(language)}>{languageLabel(language)}</button>)}</div>
            <button type="button" className="v2-copy-code" onClick={copyCode}>{copied ? <Check /> : <Copy />}{copied ? (localizedMessage(isZh, "v2.api-keys.copied")) : (localizedMessage(isZh, "v2.connect.copyCode"))}</button>
          </header>
          <div className="v2-code-body">
            {selectedRoute ? <Suspense fallback={<pre>{generatedCode}</pre>}><CodeHighlighter code={generatedCode} language={syntaxLanguage(codeLang)} dark padding={0} /></Suspense> : <div className="v2-code-empty">{localizedMessage(isZh, "v2.connect.selectAModelToGenerateCodeHere")}</div>}
          </div>
          <footer>{selectedRoute ? `${protocolLabel(selectedProtocol)} · ${selectedRoute.model}` : (localizedMessage(isZh, "v2.connect.waitingForAModel"))}</footer>
        </section>
      </div>

      <Surface title={localizedMessage(isZh, "v2.connect.integrationChecklist")} description={localizedMessage(isZh, "v2.connect.confirmTheseItemsBeforeRunningTheSample")}>
        <div className="v2-connect-checks">
          <div><span>{localizedMessage(isZh, "v2.connect.gatewayEndpoint")}</span><Status tone="success">{localizedMessage(isZh, "v2.connect.configured")}</Status></div>
          <div><span>{localizedMessage(isZh, "v2.connect.modelRoute")}</span><Status tone={selectedRoute ? "success" : "warning"}>{selectedRoute?.model ?? (localizedMessage(isZh, "v2.connect.notSelected"))}</Status></div>
          <div><span>{localizedMessage(isZh, "v2.connect.consumerKey")}</span><Status tone={!selectedRoute?.enable_auth || !showKeyPicker || selectedKey ? "success" : "warning"}>{!selectedRoute?.enable_auth ? (localizedMessage(isZh, "v2.connect.notRequired")) : useEnvVar ? API_KEY_ENV : (selectedKey?.name ?? (localizedMessage(isZh, "v2.connect.notSelected")))}</Status></div>
          <div><span>{localizedMessage(isZh, "v2.connect.protocolTranslation")}</span><Status tone="info">{protocolLabel(selectedProtocol)}</Status></div>
        </div>
      </Surface>
    </PageLayout>
  );
}
