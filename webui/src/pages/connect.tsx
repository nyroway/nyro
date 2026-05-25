import { Suspense, lazy, useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Code2, Copy, TerminalSquare } from "lucide-react";

import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ApiKey, GatewayStatus, ModelCapabilities, Model as ModelType } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Button } from "@/components/ui/button";
import { Combobox } from "@/components/ui/combobox";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";

const CodeHighlighter = lazy(() => import("@/components/ui/code-highlighter"));

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
  desc: { zh: string; en: string };
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
    desc: { zh: "Anthropic 官方命令行编程助手", en: "Anthropic official coding CLI assistant" },
  },
  {
    id: "codex-cli",
    name: "Codex CLI",
    iconKey: "openai",
    protocol: "openai-compatible",
    desc: { zh: "OpenAI 命令行编程工具", en: "OpenAI coding CLI tool" },
  },
  {
    id: "gemini-cli",
    name: "Gemini CLI",
    iconKey: "gemini",
    protocol: "google-gemini",
    desc: { zh: "Google Gemini 命令行工具", en: "Google Gemini command line tool" },
  },
  {
    id: "opencode",
    name: "OpenCode",
    iconKey: "opencode-logo-light",
    protocol: "openai-compatible",
    desc: { zh: "开源 AI 编程命令行工具", en: "Open-source AI coding CLI tool" },
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
  // Keep ":" readable for model variants like gemma3:1b.
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
      // Ignore corrupted local cache.
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
    if (!selectedRoute?.access_control) {
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
    selectedRoute?.access_control ? selectedApiKey?.key ?? UNSELECTED_KEY_PLACEHOLDER : OPTIONAL_KEY_PLACEHOLDER;
  const proxyPort = status?.proxy_port;
  const hasProxyPort = typeof proxyPort === "number" && Number.isFinite(proxyPort) && proxyPort > 0;
  const host = hasProxyPort ? `http://localhost:${proxyPort}` : "http://localhost:<proxy-port>";
  const codeModel = selectedRoute?.virtual_model ?? "gpt-4o";
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
    if (!selectedCliRoute?.access_control) {
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
    selectedCliRoute?.access_control
      ? selectedCliApiKey?.key ?? UNSELECTED_KEY_PLACEHOLDER
      : OPTIONAL_KEY_PLACEHOLDER;
  const cliModel = selectedCliRoute?.virtual_model ?? "gpt-4o";
  const canSyncCli =
    IS_TAURI &&
    hasProxyPort &&
    selectedCliReady &&
    Boolean(selectedCliRoute) &&
    (!selectedCliRoute?.access_control || Boolean(selectedCliApiKey));

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
    return isZh ? "操作失败，请重试" : "Operation failed, please retry";
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
        text: isZh ? "同步成功" : "Sync successful",
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
        title: isZh ? "同步配置失败" : "Failed to sync config",
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
          ? (isZh ? "恢复成功" : "Restore successful")
          : (isZh ? "无可恢复配置" : "No backup found"),
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
        title: isZh ? "恢复配置失败" : "Failed to restore config",
        description: message,
      });
    },
  });

  async function copyText(text: string, target: "code" | "cli") {
    await navigator.clipboard.writeText(text);
    setCopiedTarget(target);
    setTimeout(() => setCopiedTarget(null), 1200);
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "接入" : "Connect"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh
            ? "通过代码或命令行工具将应用连接到 Nyro Gateway"
            : "Connect your app to Nyro Gateway via code or CLI tools"}
        </p>
      </div>

      <div className="connect-panel glass rounded-2xl p-5">
        <Tabs value={tab} onValueChange={(next) => setTab(next as "code" | "cli")} className="space-y-3">
          <TabsList className="connect-switch-tabs-list grid h-12 w-full grid-cols-2 rounded-xl p-1 md:w-1/2">
            <TabsTrigger
              className="connect-switch-tab-trigger h-10 gap-1.5 text-sm font-medium"
              value="cli"
            >
              <TerminalSquare className="h-4 w-4" />
              {isZh ? "工具接入" : "CLI"}
            </TabsTrigger>
            <TabsTrigger
              className="connect-switch-tab-trigger h-10 gap-1.5 text-sm font-medium"
              value="code"
            >
              <Code2 className="h-4 w-4" />
              {isZh ? "代码接入" : "Code"}
            </TabsTrigger>
          </TabsList>

          <TabsContent value="cli" className="!mt-1 space-y-4">
            <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
              {CLI_TOOLS.map((tool) => {
                const active = tool.id === selectedCliToolId;
                const isReady = IS_TAURI ? Boolean(cliReadyStatus[tool.id]) : true;
                return (
                  <button
                    key={tool.id}
                    type="button"
                    onClick={() => setSelectedCliToolId(tool.id)}
                    data-state={active ? "on" : "off"}
                    className="provider-preset-card connect-cli-tool-card h-auto w-full rounded-2xl text-left"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="flex items-start gap-3">
                        <div className="mt-0.5 inline-flex h-9 w-9 items-center justify-center">
                          <ProviderIcon
                            iconKey={tool.iconKey}
                            name={tool.name}
                            protocol={tool.protocol}
                            size={30}
                            className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                          />
                          <ProviderIcon
                            iconKey={tool.iconKey}
                            name={tool.name}
                            protocol={tool.protocol}
                            size={30}
                            monochrome
                            className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                          />
                        </div>
                        <div>
                          <p className="text-base leading-tight font-semibold text-slate-900">{tool.name}</p>
                          <p className="mt-1 text-sm text-slate-500">{isZh ? tool.desc.zh : tool.desc.en}</p>
                        </div>
                      </div>
                      <Badge variant={isReady ? "success" : "secondary"} className="connect-label-badge">
                        {IS_TAURI
                          ? isReady
                            ? (isZh ? "已就绪" : "Ready")
                            : (isZh ? "未就绪" : "Not Ready")
                          : (isZh ? "手动配置" : "Manual setup")}
                      </Badge>
                    </div>
                  </button>
                );
              })}
            </div>

            {selectedCliReady ? (
              <div className="connect-cli-shell rounded-xl border p-4 space-y-3">
                <div className="flex items-center gap-2">
                  <TerminalSquare className="h-4 w-4 text-slate-600" />
                  <p className="text-sm font-semibold text-slate-900">{selectedCliTool.name}</p>
                  <Badge variant="outline" className="connect-label-badge">{protocolLabel(selectedCliTool.protocol, isZh)}</Badge>
                </div>

                <div className="grid grid-cols-2 gap-4 items-start">
                  <div className="space-y-2">
                    <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                      {isZh ? "选择模型" : "Select Model"}
                    </p>
                  <Combobox
                    className="bg-white"
                      value={selectedCliRouteId}
                      onValueChange={(routeId) => {
                        setSelectedCliRouteId(routeId);
                        setCliRouteAnchorByTool((prev) => ({ ...prev, [selectedCliTool.id]: routeId }));
                      }}
                    options={cliRoutes.map((route) => ({
                      value: route.id,
                      label: `${route.name} · ${route.virtual_model}`,
                    }))}
                    placeholder={
                      cliRoutes.length > 0
                        ? (isZh ? "选择模型" : "Select model")
                        : (isZh ? "请先创建模型" : "Create a model first")
                    }
                    searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                    emptyText={isZh ? "暂无可选模型" : "No models available"}
                  />
                    {selectedCliCapabilities && (
                      <div className="flex flex-wrap gap-2 text-xs text-slate-600 pt-1">
                        {selectedCliCapabilities.reasoning && <Badge variant="success" className="connect-label-badge">{isZh ? "推理" : "Reasoning"}</Badge>}
                        {selectedCliCapabilities.tool_call && <Badge variant="success" className="connect-label-badge">{isZh ? "工具调用" : "Tools"}</Badge>}
                        <Badge variant="success" className="connect-label-badge">
                          {`ctx:${Math.round(selectedCliCapabilities.context_window / 1024)}K`}
                        </Badge>
                        {selectedCliCapabilities.embedding_length != null && selectedCliCapabilities.embedding_length > 0 && (
                          <Badge variant="success" className="connect-label-badge">
                            {`emb:${selectedCliCapabilities.embedding_length}`}
                          </Badge>
                        )}
                      </div>
                    )}
                  </div>
                  {selectedCliRoute?.access_control && (
                    <div className="space-y-2">
                      <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                        {isZh ? "选择 API Key" : "Select API Key"}
                      </p>
                    <Combobox
                      className="bg-white"
                        value={selectedCliKeyId}
                        onValueChange={setSelectedCliKeyId}
                      options={cliAvailableKeys.map((key) => ({
                        value: key.id,
                        label: `${key.name} · ${maskApiKey(key.key)}`,
                      }))}
                      placeholder={isZh ? "选择 API Key" : "Select API key"}
                      searchPlaceholder={isZh ? "搜索 API Key..." : "Search API keys..."}
                      emptyText={isZh ? "暂无可选 API Key" : "No API keys available"}
                    />
                    </div>
                  )}
                </div>
                <div className="w-1/2 space-y-2">
                  <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                    {IS_TAURI ? (isZh ? "更新配置" : "Update Config") : (isZh ? "配置预览" : "Config Preview")}
                  </p>
                  {IS_TAURI ? (
                    <div className="grid grid-cols-3 gap-2">
                      <div className="space-y-1">
                        <Button
                          className="w-full"
                          disabled={!canSyncCli || syncCliMut.isPending}
                          onClick={() => {
                            setCliActionMessage(null);
                            setCliSuccessAction(null);
                            syncCliMut.mutate();
                          }}
                        >
                          {syncCliMut.isPending
                            ? (isZh ? "同步中..." : "Syncing...")
                            : cliSuccessAction === "sync"
                              ? <Check className="h-4 w-4" />
                              : (isZh ? "同步配置" : "Sync Config")}
                        </Button>
                        <p
                          className={`min-h-5 text-xs ${
                            cliActionMessage?.action === "sync"
                              ? (cliActionMessage.kind === "success" ? "text-green-600" : "text-red-600")
                              : "text-transparent"
                          }`}
                        >
                          {cliActionMessage?.action === "sync" ? cliActionMessage.text : "\u00A0"}
                        </p>
                      </div>
                      <div className="space-y-1">
                        <Button
                          className="w-full"
                          disabled={restoreCliMut.isPending}
                          onClick={() => {
                            setCliActionMessage(null);
                            setCliSuccessAction(null);
                            restoreCliMut.mutate();
                          }}
                        >
                          {restoreCliMut.isPending
                            ? (isZh ? "恢复中..." : "Restoring...")
                            : cliSuccessAction === "restore"
                              ? <Check className="h-4 w-4" />
                              : (isZh ? "恢复配置" : "Restore Config")}
                        </Button>
                        <p
                          className={`min-h-5 text-xs ${
                            cliActionMessage?.action === "restore"
                              ? (cliActionMessage.kind === "success" ? "text-green-600" : "text-red-600")
                              : "text-transparent"
                          }`}
                        >
                          {cliActionMessage?.action === "restore" ? cliActionMessage.text : "\u00A0"}
                        </p>
                      </div>
                      <div>
                        <Button className="w-full" onClick={() => setIsCliPreviewVisible((prev) => !prev)}>
                          {isCliPreviewVisible
                            ? (isZh ? "隐藏配置" : "Hide Config")
                            : (isZh ? "查看配置" : "View Config")}
                        </Button>
                      </div>
                    </div>
                  ) : (
                    <Button className="w-full" onClick={() => setIsCliPreviewVisible((prev) => !prev)}>
                      {isCliPreviewVisible
                        ? (isZh ? "隐藏配置" : "Hide Config")
                        : (isZh ? "查看配置" : "View Config")}
                    </Button>
                  )}
                </div>
                {isCliPreviewVisible && (
                  <div className="-mt-3 space-y-2">
                    <p className="ml-1 text-xs text-slate-500">
                      {isZh ? "仅展示将被更新的配置片段" : "Only showing configuration fragments to be updated"}
                    </p>
                    <div className="connect-cli-preview relative overflow-hidden rounded-lg border">
                      <button
                        onClick={() => copyText(cliPreview, "cli")}
                        className="connect-code-copy-btn absolute top-3 right-3 rounded-xl p-3 cursor-pointer transition-colors"
                        title={isZh ? "复制配置预览" : "Copy preview"}
                      >
                        {copiedTarget === "cli" ? <Check className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
                      </button>
                      <Suspense fallback={<pre className="overflow-x-auto text-xs text-slate-500">{cliPreview}</pre>}>
                        <CodeHighlighter
                          code={cliPreview}
                          language={cliPreviewLang}
                          dark={isDarkTheme}
                          padding="14px 16px"
                        />
                      </Suspense>
                    </div>
                  </div>
                )}
                {cliRoutes.length === 0 && (
                  <p className="text-xs text-amber-600">
                    {isZh
                      ? "当前没有可选对话模型，请先创建对话模型。"
                      : "No chat models available. Create a chat model first."}
                  </p>
                )}
                {selectedCliRoute?.access_control && !selectedCliApiKey && (
                  <p className="text-xs text-amber-600">
                    {isZh
                      ? "当前模型开启了访问控制，请先选择 API Key 再同步。"
                      : "This model requires access control. Select an API key before syncing."}
                  </p>
                )}
              </div>
            ) : (
              <p className="text-xs text-amber-600">
                {isZh
                  ? "当前 CLI 未就绪，配置面板已隐藏。"
                  : "Selected CLI is not ready, configuration panel is hidden."}
              </p>
            )}
          </TabsContent>

          <TabsContent value="code" className="!mt-1 space-y-4">
            <div className="space-y-2">
              <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                {isZh ? "选择接入协议" : "Select Ingress Protocol"}
              </p>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
                {CODE_PROTOCOLS.map((protocol) => {
                  const active = protocol.id === selectedCodeProtocol;
                  return (
                    <button
                      key={protocol.id}
                      type="button"
                      onClick={() => setSelectedCodeProtocol(protocol.id)}
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
                          <p className="text-base leading-tight font-semibold text-slate-900">{protocol.name}</p>
                          <p className="mt-1 text-xs text-slate-500">{protocol.apiPath}</p>
                        </div>
                      </div>
                    </button>
                  );
                })}
              </div>
            </div>

            <div className="connect-cli-shell rounded-xl border p-4 space-y-3">
              <div className="flex items-center gap-2">
                <Code2 className="h-4 w-4 text-slate-600" />
                <p className="text-sm font-semibold text-slate-900">{protocolLabel(selectedCodeProtocol, isZh)}</p>
                <Badge variant="outline" className="connect-label-badge">
                  {protocolApiPathForRoute(selectedCodeProtocol, codeRouteType)}
                </Badge>
              </div>

              <div className="grid grid-cols-2 gap-4 items-start">
                <div className="space-y-2">
                  <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                    {isZh ? "选择模型" : "Select Model"}
                  </p>
                  <Combobox
                    className="bg-white"
                    value={selectedCodeRouteId}
                    onValueChange={setSelectedCodeRouteId}
                    options={codeRoutes.map((route) => ({
                      value: route.id,
                      label: `${route.name} · ${route.virtual_model}`,
                    }))}
                    placeholder={
                      codeRoutes.length > 0
                        ? (isZh ? "选择模型" : "Select model")
                        : (isZh ? "请先创建模型" : "Create a model first")
                    }
                    searchPlaceholder={isZh ? "搜索模型..." : "Search models..."}
                    emptyText={isZh ? "暂无可选模型" : "No models available"}
                  />
                </div>

                {selectedRoute?.access_control && (
                  <div className="space-y-2">
                    <p className="ml-1 text-xs leading-none font-normal text-slate-900">
                      {isZh ? "选择 API Key" : "Select API Key"}
                    </p>
                    <Combobox
                      className="bg-white"
                      value={selectedCodeKeyId}
                      onValueChange={setSelectedCodeKeyId}
                      options={codeAvailableKeys.map((key) => ({
                        value: key.id,
                        label: `${key.name} · ${maskApiKey(key.key)}`,
                      }))}
                      placeholder={isZh ? "选择 API Key" : "Select API key"}
                      searchPlaceholder={isZh ? "搜索 API Key..." : "Search API keys..."}
                      emptyText={isZh ? "暂无可选 API Key" : "No API keys available"}
                    />
                  </div>
                )}
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
                      onClick={() => copyText(generatedCode, "code")}
                      className="connect-code-copy-btn absolute top-3 right-3 rounded-xl p-3 cursor-pointer transition-colors"
                      title={isZh ? "复制代码" : "Copy code"}
                    >
                      {copiedTarget === "code" ? <Check className="h-4 w-4 text-green-600" /> : <Copy className="h-4 w-4" />}
                    </button>
                    <Suspense fallback={<pre className="overflow-x-auto text-xs text-slate-500">{generatedCode}</pre>}>
                      <CodeHighlighter
                        code={generatedCode}
                        language={syntaxLanguage(codeLang)}
                        dark={isDarkTheme}
                        padding={0}
                      />
                    </Suspense>
                  </div>
                </div>
              ) : (
                <p className="text-xs text-amber-600">
                  {isZh ? "请先选择模型以生成代码示例。" : "Select a model first to generate code samples."}
                </p>
              )}

              {selectedRoute && !selectedRoute.access_control && (
                <p className="text-xs text-slate-500">
                  {isZh
                    ? `当前模型未开启访问控制，示例中已使用占位 API Key：${OPTIONAL_KEY_PLACEHOLDER}`
                    : `Access control is disabled on this model. The sample uses placeholder key: ${OPTIONAL_KEY_PLACEHOLDER}`}
                </p>
              )}
            </div>
          </TabsContent>

        </Tabs>
      </div>
      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => {
          if (!open) setErrorDialog(null);
        }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={isZh ? "我知道了" : "OK"}
        onConfirm={() => setErrorDialog(null)}
      />
    </div>
  );
}
