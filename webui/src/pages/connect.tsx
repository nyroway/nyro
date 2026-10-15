import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy } from "lucide-react";

import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ApiKey, GatewayStatus, ModelCapabilities, Model as ModelType } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { localizedMessage, type MessageKey } from "@/lib/messages";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { showToast } from "@/lib/toast";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { NyroSelect } from "@/components/ui/nyro-select";

type CodeLanguage = "python" | "typescript" | "curl";
type CliToolId = "claude-code" | "codex-cli" | "gemini-cli" | "opencode";
type GatewayProtocol = "openai-compatible" | "anthropic-messages" | "google-gemini";
type CodeProtocol = GatewayProtocol;
type RouteKind = "chat" | "embedding";

type CliTool = {
  id: CliToolId;
  name: string;
  iconKey: string;
  protocol: GatewayProtocol;
  desc: MessageKey;
};

type CodeProtocolOption = {
  id: CodeProtocol;
  name: string;
  iconKey: string;
  apiPath: string;
};

const CLI_TOOLS: CliTool[] = [
  {
    id: "claude-code",
    name: "Claude Code",
    iconKey: "claude",
    protocol: "anthropic-messages",
    desc: "connect.claudeCli",
  },
  {
    id: "codex-cli",
    name: "Codex CLI",
    iconKey: "openai",
    protocol: "openai-compatible",
    desc: "connect.codexCli",
  },
  {
    id: "gemini-cli",
    name: "Gemini CLI",
    iconKey: "gemini",
    protocol: "google-gemini",
    desc: "connect.geminiCli",
  },
  {
    id: "opencode",
    name: "OpenCode",
    iconKey: "opencode-logo-light",
    protocol: "openai-compatible",
    desc: "connect.opencodeCli",
  },
];

const CODE_LANGS: CodeLanguage[] = ["python", "typescript", "curl"];
const CODE_PROTOCOLS: CodeProtocolOption[] = [
  { id: "openai-compatible", name: "OpenAI Compatible", iconKey: "openai", apiPath: "/v1/chat/completions" },
  { id: "anthropic-messages", name: "Anthropic Messages", iconKey: "anthropic", apiPath: "/v1/messages" },
  { id: "google-gemini", name: "Google Gemini", iconKey: "gemini", apiPath: "/v1beta/models/{model}:generateContent" },
];
const OPTIONAL_KEY_PLACEHOLDER = "sk-00000000000000000000000000000000";
const UNSELECTED_KEY_PLACEHOLDER = "sk-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx";
const CLI_ROUTE_ANCHOR_STORAGE_KEY = "nyro.connect.cli.route-anchor.v1";

function maskApiKey(key: string) {
  if (key.length <= 14) return key;
  return `${key.slice(0, 12)}••`;
}

function protocolLabel(protocol: GatewayProtocol, _isZh: boolean) {
  if (protocol === "openai-compatible") return "OpenAI Compatible";
  if (protocol === "anthropic-messages") return "Anthropic Messages";
  return "Google Gemini";
}

function protocolApiPath(protocol: CodeProtocol) {
  if (protocol === "openai-compatible") return "/v1/chat/completions";
  if (protocol === "anthropic-messages") return "/v1/messages";
  return "/v1beta/models/{model}:generateContent";
}

function protocolApiPathForRoute(protocol: CodeProtocol, _routeType: RouteKind) {
  return protocolApiPath(protocol);
}

function jsonText(input: unknown) {
  return JSON.stringify(input, null, 2);
}

function encodeGeminiModelForPath(model: string) {
  // Keep ":" so model names like gemma3:1b stay readable.
  return encodeURIComponent(model).replace(/%3A/gi, ":");
}

function codeTemplate(params: {
  protocol: CodeProtocol;
  model: string;
  apiKey: string;
  host: string;
  language: CodeLanguage;
  routeType: RouteKind;
}) {
  const { protocol, model, apiKey, host, language, routeType } = params;

  if (routeType === "embedding") {
    if (language === "curl") {
      return `curl ${host}/v1/embeddings \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText({
    model,
    input: "hello world",
  })}'`;
    }
    if (language === "python") {
      return `# pip install openai
from openai import OpenAI

client = OpenAI(
    api_key="${apiKey}",
    base_url="${host}/v1"
)

response = client.embeddings.create(
    model="${model}",
    input="hello world"
)

print(response.data[0].embedding[:8])`;
    }
    return `// npm install openai
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "${apiKey}",
  baseURL: "${host}/v1",
});

const response = await client.embeddings.create({
  model: "${model}",
  input: "hello world",
});

return response.data[0].embedding;`;
  }

  if (language === "curl") {
    if (protocol === "openai-compatible") {
      return `curl ${host}/v1/chat/completions \\
  -H "Authorization: Bearer ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText({
    model,
    messages: [{ role: "user", content: "Hello" }],
  })}'`;
    }
    if (protocol === "anthropic-messages") {
      return `curl ${host}/v1/messages \\
  -H "x-api-key: ${apiKey}" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText({
    model,
    max_tokens: 1024,
    messages: [{ role: "user", content: "Hello" }],
  })}'`;
    }
    return `curl ${host}/v1beta/models/${encodeGeminiModelForPath(model)}:generateContent \\
  -H "x-goog-api-key: ${apiKey}" \\
  -H "Content-Type: application/json" \\
  -d '${jsonText({
    contents: [{ role: "user", parts: [{ text: "Hello" }] }],
  })}'`;
  }

  if (language === "python") {
    if (protocol === "openai-compatible") {
      return `# pip install openai
from openai import OpenAI

client = OpenAI(
    api_key="${apiKey}",
    base_url="${host}/v1"
)

response = client.chat.completions.create(
    model="${model}",
    messages=[{"role": "user", "content": "Hello"}]
)

print(response.choices[0].message.content)`;
    }
    if (protocol === "anthropic-messages") {
      return `# pip install anthropic
from anthropic import Anthropic

client = Anthropic(
    api_key="${apiKey}",
    base_url="${host}"
)

response = client.messages.create(
    model="${model}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello"}]
)

print(response.content[0].text)`;
    }
    return `# pip install google-genai
from google import genai

client = genai.Client(
    api_key="${apiKey}",
    http_options={"base_url": "${host}"}
)

response = client.models.generate_content(
    model="${model}",
    contents="Hello"
)

print(response.text)`;
  }

  if (protocol === "openai-compatible") {
    return `// npm install openai
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: "${apiKey}",
  baseURL: "${host}/v1",
});

const response = await client.chat.completions.create({
  model: "${model}",
  messages: [{ role: "user", content: "Hello" }],
});

const content = response.choices[0]?.message?.content;
return content;`;
  }
  if (protocol === "anthropic-messages") {
    return `// npm install @anthropic-ai/sdk
import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({
  apiKey: "${apiKey}",
  baseURL: "${host}",
});

const response = await client.messages.create({
  model: "${model}",
  max_tokens: 1024,
  messages: [{ role: "user", content: "Hello" }],
});

return response.content[0];`;
  }
  return `// npm install @google/genai
import { GoogleGenAI } from "@google/genai";

const client = new GoogleGenAI({
  apiKey: "${apiKey}",
  baseUrl: "${host}",
});

const response = await client.models.generateContent({
  model: "${model}",
  contents: "Hello",
});

return response.text;`;
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

function inferClaudeProfile(model: string) {
  const value = model.toLowerCase();
  if (value.includes("haiku")) return "haiku";
  if (value.includes("sonnet")) return "sonnet";
  return "opus";
}

function cliPreviewTemplate(params: {
  tool: CliTool;
  host: string;
  apiKey: string;
  model: string;
}) {
  const { tool, host, apiKey, model } = params;
  if (tool.id === "claude-code") {
    return `# ~/.claude/settings.json
{
  "env": {
    "ANTHROPIC_AUTH_TOKEN": "${apiKey}",
    "ANTHROPIC_BASE_URL": "${host}",
    "ANTHROPIC_MODEL": "${model}",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "${model}",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "${model}",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "${model}",
    "CLAUDE_CODE_NO_FLICKER": "1"
  },
  "model": "${inferClaudeProfile(model)}"
}`;
  }
  if (tool.id === "codex-cli") {
    const codexModel = model;
    const codexBaseUrl = `${host}/v1`;
    return `# ~/.codex/auth.json
{
  "OPENAI_API_KEY": "${apiKey}"
}

# ~/.codex/config.toml
model_provider = "nyro"
model = "${codexModel}"
model_reasoning_effort = "high"
disable_response_storage = true

[model_providers]
[model_providers.nyro]
name = "Nyro Gateway"
base_url = "${codexBaseUrl}"
wire_api = "responses"
requires_openai_auth = true`;
  }
  if (tool.id === "gemini-cli") {
    return `# ~/.gemini/.env
GEMINI_API_KEY=${apiKey}
GEMINI_MODEL=${model}
GOOGLE_GEMINI_BASE_URL=${host}

# ~/.gemini/settings.json
{
  "security": {
    "auth": {
      "selectedType": "gemini-api-key"
    }
  }
}`;
  }
  return `# ~/.config/opencode/opencode.json
{
  "$schema": "https://opencode.ai/config.json",
  "model": "nyro/${model}",
  "provider": {
    "nyro": {
      "name": "Nyro Gateway",
      "npm": "@ai-sdk/openai-compatible",
      "options": {
        "apiKey": "${apiKey}",
        "baseURL": "${host}/v1",
        "model": "${model}"
      },
      "models": {
        "${model}": {
          "name": "${model}"
        }
      }
    }
  }
}`;
}

export default function ConnectPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();

  const [tab, setTab] = useState<"code" | "cli">("cli");
  const [codeLang, setCodeLang] = useState<CodeLanguage>("python");
  const [selectedCodeProtocol, setSelectedCodeProtocol] = useState<CodeProtocol>("openai-compatible");
  const [selectedCodeRouteId, setSelectedCodeRouteId] = useState("");
  const [selectedCliRouteId, setSelectedCliRouteId] = useState("");
  const [selectedCodeKeyId, setSelectedCodeKeyId] = useState("");
  const [selectedCliKeyId, setSelectedCliKeyId] = useState("");
  const [selectedCliToolId, setSelectedCliToolId] = useState<CliToolId>("claude-code");
  const [copiedTarget, setCopiedTarget] = useState<"code" | "cli" | null>(null);
  const [isDarkTheme, setIsDarkTheme] = useState(false);
  const [cliActionMessage, setCliActionMessage] = useState<{
    action: "sync" | "restore";
    kind: "success" | "error";
    text: string;
  } | null>(null);
  const [cliSuccessAction, setCliSuccessAction] = useState<"sync" | "restore" | null>(null);
  const [isCliPreviewVisible, setIsCliPreviewVisible] = useState(false);
  const [cliRouteAnchorByTool, setCliRouteAnchorByTool] = useState<Partial<Record<CliToolId, string>>>({});
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);
  const cliFeedbackTimeoutRef = useRef<number | null>(null);

  const { data: routes = [] } = useQuery<ModelType[]>({
    queryKey: ["routes"],
    queryFn: () => backend("list_models"),
  });
  const { data: apiKeys = [] } = useQuery<ApiKey[]>({
    queryKey: ["api-keys"],
    queryFn: () => backend("list_api_keys"),
  });
  const { data: status } = useQuery<GatewayStatus>({
    queryKey: ["gateway-status"],
    queryFn: () => backend("get_gateway_status"),
  });
  const { data: cliReadyStatus = {} } = useQuery<Partial<Record<CliToolId, boolean>>>({
    queryKey: ["connect-cli-ready-status"],
    queryFn: () => backend("detect_cli_tools"),
    enabled: IS_TAURI,
    staleTime: 30_000,
    refetchInterval: 30_000,
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
    if (typeof window === "undefined") return;
    try {
      const raw = window.localStorage.getItem(CLI_ROUTE_ANCHOR_STORAGE_KEY);
      if (!raw) return;
      const parsed: unknown = JSON.parse(raw);
      if (!parsed || typeof parsed !== "object") return;
      const next: Partial<Record<CliToolId, string>> = {};
      for (const tool of CLI_TOOLS) {
        const value = (parsed as Record<string, unknown>)[tool.id];
        if (typeof value === "string" && value.length > 0) {
          next[tool.id] = value;
        }
      }
      setCliRouteAnchorByTool(next);
    } catch {
      // Ignore a corrupted local cache.
    }
  }, []);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(CLI_ROUTE_ANCHOR_STORAGE_KEY, JSON.stringify(cliRouteAnchorByTool));
  }, [cliRouteAnchorByTool]);

  useEffect(() => {
    setCliActionMessage(null);
    setCliSuccessAction(null);
    setIsCliPreviewVisible(false);
  }, [selectedCliToolId]);

  useEffect(() => {
    if (tab !== "cli") return;
    if (!selectedCliRouteId) return;
    setIsCliPreviewVisible(true);
  }, [selectedCliRouteId, tab]);

  useEffect(
    () => () => {
      if (cliFeedbackTimeoutRef.current) {
        window.clearTimeout(cliFeedbackTimeoutRef.current);
      }
    },
    [],
  );

  const codeRoutes = routes;

  useEffect(() => {
    if (selectedCodeRouteId && !codeRoutes.some((route) => route.id === selectedCodeRouteId)) {
      setSelectedCodeRouteId("");
    }
  }, [codeRoutes, selectedCodeRouteId]);

  const selectedRoute = useMemo(
    () => codeRoutes.find((route) => route.id === selectedCodeRouteId) ?? null,
    [codeRoutes, selectedCodeRouteId],
  );

  const codeAvailableKeys = useMemo(() => {
    if (!selectedRoute) return [];
    return apiKeys.filter((key) => key.model_ids.includes(selectedRoute.id));
  }, [apiKeys, selectedRoute]);

  useEffect(() => {
    if (!selectedRoute?.enable_auth) {
      setSelectedCodeKeyId("");
      return;
    }
    if (selectedCodeKeyId && !codeAvailableKeys.some((key) => key.id === selectedCodeKeyId)) {
      setSelectedCodeKeyId("");
    }
  }, [codeAvailableKeys, selectedCodeKeyId, selectedRoute]);

  const selectedApiKey = useMemo(
    () => codeAvailableKeys.find((key) => key.id === selectedCodeKeyId) ?? null,
    [codeAvailableKeys, selectedCodeKeyId],
  );

  const codeEffectiveApiKey =
    selectedRoute?.enable_auth ? selectedApiKey?.key ?? UNSELECTED_KEY_PLACEHOLDER : OPTIONAL_KEY_PLACEHOLDER;
  const proxyPort = status?.proxy_port;
  const hasProxyPort = typeof proxyPort === "number" && Number.isFinite(proxyPort) && proxyPort > 0;
  const host = hasProxyPort ? `http://localhost:${proxyPort}` : "http://localhost:<proxy-port>";
  const codeModel = selectedRoute?.name ?? "gpt-4o";
  const codeRouteType: RouteKind = "chat";
  const codeProtocol = selectedCodeProtocol;
  const selectedCliTool =
    CLI_TOOLS.find((tool) => tool.id === selectedCliToolId) ?? CLI_TOOLS.find((tool) => tool.id === "claude-code")!;
  const selectedCliReady = IS_TAURI ? Boolean(cliReadyStatus[selectedCliTool.id]) : true;
  const cliRoutes = routes;
  const selectedCliRoute = useMemo(
    () => cliRoutes.find((route) => route.id === selectedCliRouteId) ?? null,
    [cliRoutes, selectedCliRouteId],
  );
  const cliAvailableKeys = useMemo(() => {
    if (!selectedCliRoute) return [];
    return apiKeys.filter((key) => key.model_ids.includes(selectedCliRoute.id));
  }, [apiKeys, selectedCliRoute]);

  useEffect(() => {
    if (tab !== "cli") return;
    const currentRouteExists = selectedCliRouteId && cliRoutes.some((route) => route.id === selectedCliRouteId);
    if (currentRouteExists) return;

    const anchoredRouteId = cliRouteAnchorByTool[selectedCliTool.id];
    if (anchoredRouteId && cliRoutes.some((route) => route.id === anchoredRouteId)) {
      if (selectedCliRouteId !== anchoredRouteId) {
        setSelectedCliRouteId(anchoredRouteId);
      }
      return;
    }

    if (anchoredRouteId) {
      setCliRouteAnchorByTool((prev) => ({ ...prev, [selectedCliTool.id]: "" }));
    }
    if (selectedCliRouteId) {
      setSelectedCliRouteId("");
    }
  }, [cliRouteAnchorByTool, cliRoutes, selectedCliTool.id, selectedCliRouteId, tab]);

  useEffect(() => {
    if (!selectedCliRoute?.enable_auth) {
      setSelectedCliKeyId("");
      return;
    }
    if (selectedCliKeyId && !cliAvailableKeys.some((key) => key.id === selectedCliKeyId)) {
      setSelectedCliKeyId("");
    }
  }, [selectedCliRoute, selectedCliKeyId, cliAvailableKeys]);

  const selectedCliApiKey = useMemo(
    () => cliAvailableKeys.find((key) => key.id === selectedCliKeyId) ?? null,
    [cliAvailableKeys, selectedCliKeyId],
  );
  const { data: selectedCliCapabilities } = useQuery<ModelCapabilities | null>({
    queryKey: [
      "connect-cli-model-capabilities",
      selectedCliRoute?.target_provider,
      selectedCliRoute?.target_model,
    ],
    queryFn: async () => {
      if (!selectedCliRoute?.target_provider || !selectedCliRoute?.target_model.trim()) return null;
      try {
        return await backend<ModelCapabilities>("get_model_capabilities", {
          providerId: selectedCliRoute.target_provider,
          model: selectedCliRoute.target_model.trim(),
        });
      } catch {
        return null;
      }
    },
    enabled: Boolean(selectedCliRoute?.target_provider && selectedCliRoute?.target_model.trim()),
    staleTime: 60_000,
  });
  const cliEffectiveApiKey =
    selectedCliRoute?.enable_auth
      ? selectedCliApiKey?.key ?? UNSELECTED_KEY_PLACEHOLDER
      : OPTIONAL_KEY_PLACEHOLDER;
  const cliModel = selectedCliRoute?.name ?? "gpt-4o";
  const canSyncCli =
    IS_TAURI &&
    hasProxyPort &&
    selectedCliReady &&
    Boolean(selectedCliRoute) &&
    (!selectedCliRoute?.enable_auth || Boolean(selectedCliApiKey));

  const generatedCode = codeTemplate({
    protocol: codeProtocol,
    model: codeModel,
    apiKey: codeEffectiveApiKey,
    host,
    language: codeLang,
    routeType: codeRouteType,
  });
  const cliPreview = cliPreviewTemplate({
    tool: selectedCliTool,
    host,
    apiKey: cliEffectiveApiKey,
    model: cliModel,
  });
  const cliPreviewLang = "bash";

  function formatCliError(error: unknown) {
    const localized = localizeBackendErrorMessage(error, isZh);
    if (localized && localized !== "undefined" && localized !== "null") return localized;
    return localizedMessage(isZh, "connect.operationFailedPleaseRetry");
  }

  function setCliTransientFeedback(params: {
    action: "sync" | "restore";
    kind: "success" | "error";
    text: string;
    withCheck?: boolean;
  }) {
    if (cliFeedbackTimeoutRef.current) {
      window.clearTimeout(cliFeedbackTimeoutRef.current);
    }
    setCliActionMessage({
      action: params.action,
      kind: params.kind,
      text: params.text,
    });
    setCliSuccessAction(params.withCheck ? params.action : null);
    cliFeedbackTimeoutRef.current = window.setTimeout(() => {
      setCliActionMessage(null);
      setCliSuccessAction(null);
      cliFeedbackTimeoutRef.current = null;
    }, 3000);
  }

  const syncCliMut = useMutation({
    mutationFn: () =>
      backend<string[]>("sync_cli_config", {
        toolId: selectedCliTool.id,
        host,
        apiKey: cliEffectiveApiKey,
        model: cliModel,
        capabilities: selectedCliCapabilities
          ? {
              contextWindow: selectedCliCapabilities.context_window,
              reasoning: selectedCliCapabilities.reasoning,
            }
          : undefined,
      }),
    onSuccess: () => {
      setCliTransientFeedback({
        action: "sync",
        kind: "success",
        text: localizedMessage(isZh, "connect.syncSuccessful"),
        withCheck: true,
      });
      qc.invalidateQueries({ queryKey: ["connect-cli-ready-status"] });
    },
    onError: (error) => {
      const message = formatCliError(error);
      setCliTransientFeedback({
        action: "sync",
        kind: "error",
        text: message,
      });
      setErrorDialog({
        title: localizedMessage(isZh, "connect.failedToSyncConfig"),
        description: message,
      });
    },
  });

  const restoreCliMut = useMutation({
    mutationFn: () =>
      backend<string[]>("restore_cli_config", {
        toolId: selectedCliTool.id,
      }),
    onSuccess: (paths) => {
      if (paths.length > 0) {
        setSelectedCliRouteId("");
        setSelectedCliKeyId("");
        setCliRouteAnchorByTool((prev) => ({ ...prev, [selectedCliTool.id]: "" }));
      }
      setCliTransientFeedback({
        action: "restore",
        kind: "success",
        text: paths.length
          ? (localizedMessage(isZh, "connect.restoreSuccessful"))
          : (localizedMessage(isZh, "connect.noBackupFound")),
        withCheck: true,
      });
    },
    onError: (error) => {
      const message = formatCliError(error);
      setCliTransientFeedback({
        action: "restore",
        kind: "error",
        text: message,
      });
      setErrorDialog({
        title: localizedMessage(isZh, "connect.failedToRestoreConfig"),
        description: message,
      });
    },
  });

  async function copyText(text: string, target: "code" | "cli") {
    await navigator.clipboard.writeText(text);
    setCopiedTarget(target);
    showToast(localizedMessage(isZh, "common.copied"));
    setTimeout(() => setCopiedTarget(null), 1200);
  }

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "connect.connect")}
          description={
            localizedMessage(isZh, "connect.connectYourAppToNyroGatewayViaCodeOrCLITools")
          }
        />
      )}
    >
      <div className="card">
        <div className="tabs" role="tablist">
          <button
            type="button"
            role="tab"
            aria-selected={tab === "cli"}
            className={tab === "cli" ? "tab active" : "tab"}
            onClick={() => setTab("cli")}
          >
            {localizedMessage(isZh, "connect.cli")}
          </button>
          <button
            type="button"
            role="tab"
            aria-selected={tab === "code"}
            className={tab === "code" ? "tab active" : "tab"}
            onClick={() => setTab("code")}
          >
            {localizedMessage(isZh, "connect.code")}
          </button>
        </div>

        <div className="card-body form-stack">
          {tab === "cli" ? (
            <>
              <div className="form-grid">
                {CLI_TOOLS.map((tool) => {
                  const active = tool.id === selectedCliToolId;
                  const isReady = IS_TAURI ? Boolean(cliReadyStatus[tool.id]) : true;
                  return (
                    <button
                      key={tool.id}
                      type="button"
                      onClick={() => setSelectedCliToolId(tool.id)}
                      className={active ? "radio-card selected" : "radio-card"}
                      aria-pressed={active}
                    >
                      <ProviderIcon
                        iconKey={tool.iconKey}
                        name={tool.name}
                        protocol={tool.protocol}
                        size={22}
                      />
                      <span className="radio-text">
                        <strong>{tool.name}</strong>
                        <span>{localizedMessage(isZh, tool.desc)}</span>
                      </span>
                      <span className="badge">
                        {IS_TAURI
                          ? isReady
                            ? (localizedMessage(isZh, "connect.ready"))
                            : (localizedMessage(isZh, "connect.notReady"))
                          : (localizedMessage(isZh, "connect.manualSetup"))}
                      </span>
                    </button>
                  );
                })}
              </div>

              {selectedCliReady ? (
                <>
                  <div className="card-title-row">
                    <strong>{selectedCliTool.name}</strong>
                    <span className="badge">{protocolLabel(selectedCliTool.protocol, isZh)}</span>
                  </div>

                  <div className="form-grid">
                    <div className="field">
                      <span className="field-label">{localizedMessage(isZh, "connect.selectModel")}</span>
                      <NyroSelect
                        ariaLabel={localizedMessage(isZh, "connect.selectModel")}
                        value={selectedCliRouteId}
                        options={[
                          {
                            value: "",
                            label: cliRoutes.length > 0
                              ? (localizedMessage(isZh, "connect.selectModel2"))
                              : (localizedMessage(isZh, "connect.createAModelFirst")),
                          },
                          ...cliRoutes.map((route) => ({ value: route.id, label: route.name })),
                        ]}
                        onChange={(routeId) => {
                          setSelectedCliRouteId(routeId);
                          setCliRouteAnchorByTool((prev) => ({ ...prev, [selectedCliTool.id]: routeId }));
                        }}
                      />
                      {selectedCliCapabilities ? (
                        <div className="sample-chips">
                          {selectedCliCapabilities.reasoning ? <span className="tag">{localizedMessage(isZh, "common.reasoning")}</span> : null}
                          {selectedCliCapabilities.tool_call ? <span className="tag">{localizedMessage(isZh, "common.tools")}</span> : null}
                          <span className="tag">{`ctx:${Math.round(selectedCliCapabilities.context_window / 1024)}K`}</span>
                          {selectedCliCapabilities.embedding_length != null && selectedCliCapabilities.embedding_length > 0 ? (
                            <span className="tag">{`emb:${selectedCliCapabilities.embedding_length}`}</span>
                          ) : null}
                        </div>
                      ) : null}
                    </div>

                    {selectedCliRoute?.enable_auth ? (
                      <div className="field">
                        <span className="field-label">{localizedMessage(isZh, "connect.selectAPIKey")}</span>
                        <NyroSelect
                          ariaLabel={localizedMessage(isZh, "connect.selectAPIKey")}
                          value={selectedCliKeyId}
                          options={[
                            { value: "", label: localizedMessage(isZh, "connect.selectAPIKey2") },
                            ...cliAvailableKeys.map((key) => ({
                              value: key.id,
                              label: `${key.name} · ${maskApiKey(key.key)}`,
                            })),
                          ]}
                          onChange={setSelectedCliKeyId}
                        />
                      </div>
                    ) : null}
                  </div>

                  <div className="field">
                    <span className="field-label">
                      {IS_TAURI ? (localizedMessage(isZh, "connect.updateConfig")) : (localizedMessage(isZh, "connect.configPreview"))}
                    </span>
                    {/* IS_TAURI is always false: the browser branch only offers preview and copy. */}
                    <div className="config-actions">
                      {IS_TAURI ? (
                        <>
                          <button
                            type="button"
                            className="button button-primary button-sm"
                            disabled={!canSyncCli || syncCliMut.isPending}
                            onClick={() => {
                              setCliActionMessage(null);
                              setCliSuccessAction(null);
                              syncCliMut.mutate();
                            }}
                          >
                            {syncCliMut.isPending
                              ? (localizedMessage(isZh, "connect.syncing"))
                              : cliSuccessAction === "sync"
                                ? <Check aria-hidden="true" />
                                : (localizedMessage(isZh, "connect.syncConfig"))}
                          </button>
                          <button
                            type="button"
                            className="button button-secondary button-sm"
                            disabled={restoreCliMut.isPending}
                            onClick={() => {
                              setCliActionMessage(null);
                              setCliSuccessAction(null);
                              restoreCliMut.mutate();
                            }}
                          >
                            {restoreCliMut.isPending
                              ? (localizedMessage(isZh, "connect.restoring"))
                              : cliSuccessAction === "restore"
                                ? <Check aria-hidden="true" />
                                : (localizedMessage(isZh, "connect.restoreConfig"))}
                          </button>
                          <button type="button" className="button button-sm" onClick={() => setIsCliPreviewVisible((prev) => !prev)}>
                            {isCliPreviewVisible
                              ? (localizedMessage(isZh, "connect.hideConfig"))
                              : (localizedMessage(isZh, "connect.viewConfig"))}
                          </button>
                        </>
                      ) : (
                        <button type="button" className="button button-sm" onClick={() => setIsCliPreviewVisible((prev) => !prev)}>
                          {isCliPreviewVisible
                            ? (localizedMessage(isZh, "connect.hideConfig"))
                            : (localizedMessage(isZh, "connect.viewConfig"))}
                        </button>
                      )}
                    </div>
                    {cliActionMessage?.action === "sync" ? (
                      <p className={cliActionMessage.kind === "success" ? "field-hint success" : "field-hint error"}>
                        {cliActionMessage.text}
                      </p>
                    ) : null}
                    {cliActionMessage?.action === "restore" ? (
                      <p className={cliActionMessage.kind === "success" ? "field-hint success" : "field-hint error"}>
                        {cliActionMessage.text}
                      </p>
                    ) : null}
                  </div>

                  {isCliPreviewVisible ? (
                    <div className="code-output">
                      <div className="code-output-bar">
                        <span className="code-output-lang">{localizedMessage(isZh, "connect.configPreview")}</span>
                        <span className="code-output-meta">
                          {localizedMessage(isZh, "connect.onlyShowingConfigurationFragmentsToBeUpdated")}
                        </span>
                        <button
                          type="button"
                          className="button button-sm"
                          onClick={() => copyText(cliPreview, "cli")}
                          aria-label={localizedMessage(isZh, "connect.copyPreview")}
                        >
                          {copiedTarget === "cli" ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
                          {copiedTarget === "cli" ? (localizedMessage(isZh, "common.copied")) : (localizedMessage(isZh, "common.copy"))}
                        </button>
                      </div>
                      <pre
                        className="code-output-body"
                        data-language={cliPreviewLang}
                        data-code-theme={isDarkTheme ? "dark" : "light"}
                      >
                        {cliPreview}
                      </pre>
                    </div>
                  ) : null}

                  {cliRoutes.length === 0 ? (
                    <div className="alert alert-warning">
                      {localizedMessage(isZh, "connect.noChatModelsAvailableCreateAChatModelFirst")}
                    </div>
                  ) : null}
                  {selectedCliRoute?.enable_auth && !selectedCliApiKey ? (
                    <div className="alert alert-warning">
                      {localizedMessage(isZh, "connect.thisModelRequiresAccessControlSelectAnAPIKeyBeforeSyncin")}
                    </div>
                  ) : null}
                </>
              ) : (
                <div className="alert alert-warning">
                  {localizedMessage(isZh, "connect.selectedCLIIsNotReadyConfigurationPanelIsHidden")}
                </div>
              )}
            </>
          ) : (
            <>
              <div className="field">
                <span className="field-label">{localizedMessage(isZh, "connect.selectIngressProtocol")}</span>
                <div className="provider-pick">
                  {CODE_PROTOCOLS.map((protocol) => {
                    const active = protocol.id === selectedCodeProtocol;
                    return (
                      <button
                        key={protocol.id}
                        type="button"
                        onClick={() => setSelectedCodeProtocol(protocol.id)}
                        className={active ? "provider-pick-item selected" : "provider-pick-item"}
                        aria-pressed={active}
                        data-tip={protocol.apiPath}
                      >
                        <ProviderIcon
                          iconKey={protocol.iconKey}
                          name={protocol.name}
                          protocol={protocol.id}
                          size={20}
                        />
                        <span>{protocol.name}</span>
                      </button>
                    );
                  })}
                </div>
                <span className="field-hint">{protocolApiPathForRoute(selectedCodeProtocol, codeRouteType)}</span>
              </div>

              <div className="card-title-row">
                <strong>{protocolLabel(selectedCodeProtocol, isZh)}</strong>
                <span className="badge">{protocolApiPathForRoute(selectedCodeProtocol, codeRouteType)}</span>
              </div>

              <div className="form-grid">
                <div className="field">
                  <span className="field-label">{localizedMessage(isZh, "connect.selectModel")}</span>
                  <NyroSelect
                    ariaLabel={localizedMessage(isZh, "connect.selectModel")}
                    value={selectedCodeRouteId}
                    options={[
                      {
                        value: "",
                        label: codeRoutes.length > 0
                          ? (localizedMessage(isZh, "connect.selectModel2"))
                          : (localizedMessage(isZh, "connect.createAModelFirst")),
                      },
                      ...codeRoutes.map((route) => ({ value: route.id, label: route.name })),
                    ]}
                    onChange={setSelectedCodeRouteId}
                  />
                </div>

                {selectedRoute?.enable_auth ? (
                  <div className="field">
                    <span className="field-label">{localizedMessage(isZh, "connect.selectAPIKey")}</span>
                    <NyroSelect
                      ariaLabel={localizedMessage(isZh, "connect.selectAPIKey")}
                      value={selectedCodeKeyId}
                      options={[
                        { value: "", label: localizedMessage(isZh, "connect.selectAPIKey2") },
                        ...codeAvailableKeys.map((key) => ({
                          value: key.id,
                          label: `${key.name} · ${maskApiKey(key.key)}`,
                        })),
                      ]}
                      onChange={setSelectedCodeKeyId}
                    />
                  </div>
                ) : null}
              </div>

              {selectedRoute ? (
                <div className="code-output connect-code">
                  <div className="code-output-bar">
                    <div className="discover-switch" role="tablist" aria-label={localizedMessage(isZh, "connect.codeLanguage")}>
                      {CODE_LANGS.map((lang) => (
                        <button
                          key={lang}
                          type="button"
                          role="tab"
                          aria-selected={codeLang === lang}
                          className={codeLang === lang ? "selected" : undefined}
                          onClick={() => setCodeLang(lang)}
                        >
                          {languageLabel(lang)}
                        </button>
                      ))}
                    </div>
                    <span className="code-output-meta">{protocolLabel(selectedCodeProtocol, isZh)}</span>
                    <button
                      type="button"
                      className="button button-sm"
                      onClick={() => copyText(generatedCode, "code")}
                      aria-label={localizedMessage(isZh, "connect.copyCode")}
                    >
                      {copiedTarget === "code" ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
                      {copiedTarget === "code" ? (localizedMessage(isZh, "common.copied")) : (localizedMessage(isZh, "common.copy"))}
                    </button>
                  </div>
                  <pre
                    className="code-output-body"
                    data-language={syntaxLanguage(codeLang)}
                    data-code-theme={isDarkTheme ? "dark" : "light"}
                  >
                    {generatedCode}
                  </pre>
                </div>
              ) : (
                <div className="alert alert-warning">
                  {localizedMessage(isZh, "connect.selectAModelFirstToGenerateCodeSamples")}
                </div>
              )}

              {selectedRoute && !selectedRoute.enable_auth ? (
                <p className="field-hint">
                  {localizedMessage(isZh, "connect.accessControlIsDisabledOnThisModelTheSampleUsesPlacehold", { a: OPTIONAL_KEY_PLACEHOLDER })}
                </p>
              ) : null}
            </>
          )}
        </div>
      </div>

      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => {
          if (!open) setErrorDialog(null);
        }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={localizedMessage(isZh, "common.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}
