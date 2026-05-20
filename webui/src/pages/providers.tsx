import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  Provider,
  CreateProvider,
  UpdateProvider,
  TestResult,
  OAuthSessionInitData,
  OAuthSessionStatusData,
  ProviderPreset,
  ProviderChannelPreset,
  ProviderProtocol,
  ProviderOAuthStatusData,
} from "@/lib/types";
import {
  Server,
  Plus,
  Trash2,
  CheckCircle,
  XCircle,
  Zap,
  Loader2,
  Pencil,
  X,
  ChevronLeft,
  ChevronRight,
  Eye,
  EyeOff,
  Info,
  ToggleRight,
  ToggleLeft,
  Copy,
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { NyroIcon } from "@/components/ui/nyro-icon";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Badge } from "@/components/ui/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import { resolveProtocol, PROTOCOL_TABLE } from "@/lib/protocol";
import { openExternalUrl } from "@/lib/open-external";

function protocolUrl(protocol: string) {
  return PROTOCOL_TABLE.find((p) => p.id === resolveProtocol(protocol))?.defaultBaseUrl
    ?? "https://api.openai.com/v1";
}

const emptyCreate: CreateProvider = {
  name: "",
  vendor: undefined,
  protocol: "openai-compat",
  base_url: "https://api.openai.com/v1",
  use_proxy: false,
  auth_mode: "apikey",
  preset_key: "",
  channel: "",
  models_source: "",
  static_models: "",
  api_key: "",
};
const PAGE_SIZE = 7;
const DEFAULT_PRESET_ID = "nyro";
const protocolOptions = [
  { label: "OpenAI Compatible", value: "openai-compat" },
  { label: "OpenAI Responses",  value: "openai-resps"  },
  { label: "Anthropic Messages", value: "anthropic-msgs" },
  { label: "Google Generative AI", value: "google-genai" },
] as const satisfies ReadonlyArray<{ label: string; value: ProviderProtocol }>;

function validateProviderEndpoint(
  protocol: string | undefined,
  baseUrl: string | undefined,
  isZh: boolean,
): string | null {
  if (!protocol?.trim()) {
    return isZh ? "协议不能为空" : "Protocol is required";
  }
  const trimmed = baseUrl?.trim() ?? "";
  if (!trimmed) {
    return isZh ? "Base URL 不能为空" : "Base URL is required";
  }
  try {
    new URL(trimmed);
  } catch {
    return isZh ? `无效的 Base URL: ${baseUrl}` : `Invalid base URL: ${baseUrl}`;
  }
  return null;
}

function availableProtocolsForPreset(
  preset?: ProviderPreset | null,
  channelId?: string,
): ProviderProtocol[] {
  if (!preset || preset.id === DEFAULT_PRESET_ID) {
    return protocolOptions.map((item) => item.value);
  }

  const byChannel = preset.channels?.find((channel) => channel.id === channelId);
  const collectKeys = (channels: ProviderChannelPreset[]) =>
    channels.flatMap((channel) => Object.keys(channel.baseUrls ?? {}));

  const rawKeys = byChannel
    ? Object.keys(byChannel.baseUrls ?? {})
    : collectKeys(preset.channels ?? []);

  // Resolve old/legacy keys to canonical Protocol IDs.
  const known = new Set(protocolOptions.map((item) => item.value));
  const filtered = [...new Set(
    rawKeys
      .map((key) => resolveProtocol(key) as ProviderProtocol | null)
      .filter((p): p is ProviderProtocol => p !== null && known.has(p)),
  )];

  return filtered.length ? filtered : protocolOptions.map((item) => item.value);
}

function resolvePresetProtocol(
  preset: ProviderPreset,
  channelId?: string,
  preferred?: ProviderProtocol,
): ProviderProtocol {
  const available = availableProtocolsForPreset(preset, channelId);
  const canonicalDefault = (resolveProtocol(preset.defaultProtocol) ?? "openai-compat") as ProviderProtocol;
  if (preferred && available.includes(preferred)) return preferred;
  if (available.includes(canonicalDefault)) return canonicalDefault;
  return available[0] ?? canonicalDefault;
}

function presetLabel(preset: ProviderPreset, isZh: boolean) {
  return isZh ? preset.label.zh : preset.label.en;
}

function presetLabelClass(preset: ProviderPreset, isZh: boolean) {
  const len = presetLabel(preset, isZh).trim().length;
  if (len >= 16) return "provider-preset-label provider-preset-label-micro";
  if (len >= 12) return "provider-preset-label provider-preset-label-compact";
  return "provider-preset-label";
}

function channelLabel(channel: ProviderChannelPreset, isZh: boolean) {
  return isZh ? channel.label.zh : channel.label.en;
}

function toGatewayBaseUrl(url: string) {
  const normalized = url.trim().replace(/\/+$/, "");
  return normalized;
}

function defaultModelsEndpoint(baseUrl: string, protocol: ProviderProtocol) {
  const normalized = baseUrl.trim().replace(/\/+$/, "");
  let parsed: URL | null = null;
  try {
    parsed = new URL(normalized);
  } catch {
    parsed = null;
  }

  if (protocol === "openai-compat" || protocol === "openai-resps" || protocol === "anthropic-msgs") {
    // OpenRouter model discovery endpoint should be /api/v1/models.
    if (parsed?.host === "openrouter.ai") {
      const pathname = parsed.pathname.replace(/\/+$/, "");
      if (pathname === "/api" || pathname === "/api/v1") {
        return `${parsed.origin}/api/v1/models`;
      }
    }

    try {
      const pathname = new URL(normalized).pathname.replace(/\/+$/, "");
      return pathname && pathname !== "/" ? `${normalized}/models` : `${normalized}/v1/models`;
    } catch {
      return normalized.endsWith("/v1") ? `${normalized}/models` : `${normalized}/v1/models`;
    }
  }

  if (protocol === "google-genai") {
    return `${normalized}/v1beta/models`;
  }

  return "";
}

function isVertexProviderSelection(value?: Pick<CreateProvider, "vendor" | "preset_key"> | Pick<UpdateProvider, "vendor" | "preset_key"> | null) {
  const vendor = value?.vendor?.trim().toLowerCase();
  const preset = value?.preset_key?.trim().toLowerCase();
  return vendor === "vertexai" || preset === "vertexai";
}

function defaultVertexBaseUrl(protocol: ProviderProtocol | string) {
  const base = "https://aiplatform.googleapis.com/v1/projects/{project}/locations/global";
  return protocol === "openai-compat" ? `${base}/endpoints/openapi` : base;
}

function joinStaticModels(models?: string[]) {
  return models?.join("\n") ?? "";
}

function fallbackChannelPreset(): ProviderChannelPreset {
  return {
    id: "default",
    label: { zh: "默认", en: "Default" },
    baseUrls: {},
  };
}

function fallbackProviderPreset(): ProviderPreset {
  return {
    id: DEFAULT_PRESET_ID,
    label: { zh: "自定义", en: "Custom" },
    defaultProtocol: "openai-compat",
    channels: [],
  };
}

function presetChannels(preset?: ProviderPreset | null) {
  return preset?.channels?.length ? preset.channels : [fallbackChannelPreset()];
}

function presetChannelAuthMode(
  preset?: ProviderPreset | null,
  channelId?: string | null,
): "apikey" | "oauth" {
  const channel = presetChannels(preset).find((item) => item.id === channelId) ?? presetChannels(preset)[0];
  return channel?.authMode === "oauth" ? "oauth" : "apikey";
}

function normalizeAuthMode(mode?: string | null): "apikey" | "oauth" {
  if (!mode) return "apikey";
  return mode.trim().toLowerCase() === "oauth" ? "oauth" : "apikey";
}

function mergeProviderOAuthStatus(provider: Provider, status: ProviderOAuthStatusData): Provider {
  if (provider.id !== status.provider_id) return provider;

  const resourceUrl = toGatewayBaseUrl(status.resource_url ?? "");
  return {
    ...provider,
    base_url: resourceUrl || provider.base_url,
    oauth_status: status.status,
    oauth_expires_at: status.expires_at ?? null,
    oauth_last_error: status.last_error ?? null,
    oauth_updated_at: status.updated_at ?? null,
    updated_at: status.updated_at ?? provider.updated_at,
  };
}

function nextProviderCopyName(providers: Provider[], originalName: string) {
  const base = `${originalName}_Copy`;
  const existingNames = new Set(providers.map((provider) => provider.name));
  if (!existingNames.has(base)) return base;

  for (let index = 2; ; index += 1) {
    const candidate = `${base}${index}`;
    if (!existingNames.has(candidate)) return candidate;
  }
}

function resolvePresetConfig(
  preset: ProviderPreset,
  protocol: ProviderProtocol,
  channelId?: string,
) {
  const channel = presetChannels(preset).find((item) => item.id === channelId) ?? presetChannels(preset)[0];
  const sourceBaseUrls = channel?.baseUrls ?? {};
  const rawBaseUrl = Object.entries(sourceBaseUrls).find(
    ([key]) => resolveProtocol(key) === protocol,
  )?.[1];
  const baseUrl = rawBaseUrl ? toGatewayBaseUrl(rawBaseUrl) : "";
  const modelsSource = channel?.modelsSource ?? channel?.modelsEndpoint ?? "";
  const apiKey = channel?.apiKey ?? "";
  const staticModels = joinStaticModels(channel?.staticModels);

  return {
    baseUrl,
    modelsSource,
    apiKey,
    staticModels,
    channel,
  };
}

function FieldLabel({ children, info }: { children: string; info?: string }) {
  return (
    <label className="ml-1 inline-flex items-center gap-1 text-xs leading-none font-normal text-slate-900">
      <span>{children}</span>
      {info ? (
        <TooltipProvider delayDuration={120}>
          <Tooltip>
            <TooltipTrigger asChild>
              <span
                className="inline-flex cursor-help text-slate-400 hover:text-slate-600"
                aria-label={info}
              >
                <Info className="h-3.5 w-3.5" />
              </span>
            </TooltipTrigger>
            <TooltipContent>{info}</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      ) : null}
    </label>
  );
}

type TestLogLevel = "info" | "success" | "error";

type TestLogEntry = {
  timestamp: string;
  level: TestLogLevel;
  message: string;
};

const PROVIDER_TEST_RESULTS_STORAGE_KEY = "nyro.provider-test-results.v1";

function nowTimestamp() {
  const now = new Date();
  const hh = String(now.getHours()).padStart(2, "0");
  const mm = String(now.getMinutes()).padStart(2, "0");
  const ss = String(now.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

function loadProviderTestResults(): Record<string, TestResult> {
  if (typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(PROVIDER_TEST_RESULTS_STORAGE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, TestResult>;
    if (!parsed || typeof parsed !== "object") return {};

    const normalized: Record<string, TestResult> = {};
    for (const [id, value] of Object.entries(parsed)) {
      if (!value || typeof value !== "object" || typeof value.success !== "boolean") continue;
      normalized[id] = {
        success: value.success,
        latency_ms: Number.isFinite(value.latency_ms) ? value.latency_ms : 0,
        model: typeof value.model === "string" ? value.model : undefined,
        error: typeof value.error === "string" ? value.error : undefined,
      };
    }
    return normalized;
  } catch {
    return {};
  }
}

function saveProviderTestResults(results: Record<string, TestResult>) {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(PROVIDER_TEST_RESULTS_STORAGE_KEY, JSON.stringify(results));
  } catch {
    // Ignore storage errors to avoid breaking provider UI.
  }
}

export default function ProvidersPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const qc = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, TestResult>>(loadProviderTestResults);
  const [testDialogOpen, setTestDialogOpen] = useState(false);
  const [testLogs, setTestLogs] = useState<TestLogEntry[]>([]);
  const [isTestRunning, setIsTestRunning] = useState(false);
  const [testTarget, setTestTarget] = useState<Provider | null>(null);
  const [providerToDelete, setProviderToDelete] = useState<Provider | null>(null);
  const [providerToCopy, setProviderToCopy] = useState<Provider | null>(null);
  const [selectedPresetId, setSelectedPresetId] = useState(DEFAULT_PRESET_ID);
  const [showCreateApiKey, setShowCreateApiKey] = useState(true);
  const [showEditApiKey, setShowEditApiKey] = useState(false);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);
  const [createOAuthSession, setCreateOAuthSession] = useState<OAuthSessionInitData | null>(null);
  const [createOAuthStatus, setCreateOAuthStatus] = useState<OAuthSessionStatusData | null>(null);
  const [createOAuthBusy, setCreateOAuthBusy] = useState(false);
  const [createOAuthCallbackUrl, setCreateOAuthCallbackUrl] = useState("");
  const [createOAuthCallbackError, setCreateOAuthCallbackError] = useState("");
  const [createOAuthCode, setCreateOAuthCode] = useState("");
  const [createOAuthCopied, setCreateOAuthCopied] = useState(false);
  const [createOAuthCopyFailed, setCreateOAuthCopyFailed] = useState(false);
  const createOAuthPollerRef = useRef<number | null>(null);
  // Edit-mode OAuth re-authorization states
  const [editOAuthSession, setEditOAuthSession] = useState<OAuthSessionInitData | null>(null);
  const [editOAuthSessionStatus, setEditOAuthSessionStatus] = useState<OAuthSessionStatusData | null>(null);
  const [editOAuthBusy, setEditOAuthBusy] = useState(false);
  const [editOAuthCallbackUrl, setEditOAuthCallbackUrl] = useState("");
  const [editOAuthCode, setEditOAuthCode] = useState("");
  const [showEditReauth, setShowEditReauth] = useState(false);
  const editOAuthPollerRef = useRef<number | null>(null);
  const activeTestRunRef = useRef(0);
  const logsContainerRef = useRef<HTMLDivElement | null>(null);

  const { data: providers = [], isLoading } = useQuery<Provider[]>({
    queryKey: ["providers"],
    queryFn: () => backend("get_providers"),
  });
  const { data: providerPresetsRaw = [] } = useQuery<ProviderPreset[]>({
    queryKey: ["provider-presets"],
    queryFn: () => backend("get_provider_presets"),
  });
  const { data: proxyEnabledSetting } = useQuery<string | null>({
    queryKey: ["setting", "proxy_enabled"],
    queryFn: () => backend("get_setting", { key: "proxy_enabled" }),
  });
  const providerPresets = useMemo(
    () => (providerPresetsRaw.length ? providerPresetsRaw : [fallbackProviderPreset()]),
    [providerPresetsRaw],
  );
  const editingProvider = useMemo(
    () => providers.find((provider) => provider.id === editingId) ?? null,
    [providers, editingId],
  );
  const isGlobalProxyEnabled = useMemo(() => {
    const normalized = (proxyEnabledSetting ?? "").trim().toLowerCase();
    return ["1", "true", "yes", "on"].includes(normalized);
  }, [proxyEnabledSetting]);
  const [form, setForm] = useState<CreateProvider>(emptyCreate);
  const selectedPreset = useMemo(
    () => providerPresets.find((preset) => preset.id === selectedPresetId) ?? null,
    [providerPresets, selectedPresetId],
  );
  useEffect(() => {
    if (providerPresets.some((preset) => preset.id === selectedPresetId)) return;
    setSelectedPresetId(providerPresets[0]?.id ?? DEFAULT_PRESET_ID);
  }, [providerPresets, selectedPresetId]);

  const [editForm, setEditForm] = useState<UpdateProvider & { id: string }>({
    id: "",
    name: "",
    vendor: undefined,
    protocol: "",
    base_url: "",
    use_proxy: false,
    preset_key: "",
    channel: "",
    models_source: "",
    static_models: "",
    api_key: "",
    auth_mode: "apikey",
  });
  const isEditingOAuthProvider = Boolean(
    editingProvider
      && (
        normalizeAuthMode(editingProvider.auth_mode) === "oauth"
        || normalizeAuthMode(editForm.auth_mode) === "oauth"
      ),
  );
  const editOAuthStatusQuery = useQuery<ProviderOAuthStatusData>({
    queryKey: ["provider-oauth-status", editingProvider?.id],
    queryFn: () => backend("get_provider_oauth_status", { id: editingProvider?.id }),
    enabled: isEditingOAuthProvider,
  });

  function syncProviderOAuthStatus(status: ProviderOAuthStatusData) {
    qc.setQueryData<ProviderOAuthStatusData>(["provider-oauth-status", status.provider_id], status);
    qc.setQueryData<Provider[]>(["providers"], (current) =>
      current?.map((provider) => mergeProviderOAuthStatus(provider, status)) ?? current,
    );
  }

  const createMut = useMutation({
    mutationFn: (input: CreateProvider) => backend<Provider>("create_provider", { input }),
    onSuccess: async (createdProvider: Provider) => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      closeCreateForm();
      await handleTest(createdProvider);
    },
    onError: (error: unknown) => {
      showErrorDialog("创建提供商失败", "Failed to create provider", error);
    },
  });

  const createOAuthMut = useMutation({
    mutationFn: ({ sessionId, input }: { sessionId: string; input: CreateProvider }) =>
      backend<Provider>("create_oauth_provider", { sessionId, input }),
    onSuccess: async (createdProvider: Provider) => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      closeCreateForm();
      await handleTest(createdProvider);
    },
    onError: (error: unknown) => {
      showErrorDialog("创建 OAuth 提供商失败", "Failed to create OAuth provider", error);
    },
  });

  const [editError, setEditError] = useState<string | null>(null);

  const reconnectOAuthMut = useMutation({
    mutationFn: (id: string) => backend<ProviderOAuthStatusData>("reconnect_provider_oauth", { id }),
    onSuccess: (status) => {
      setEditError(null);
      syncProviderOAuthStatus(status);
      qc.invalidateQueries({ queryKey: ["providers"], refetchType: "none" });
    },
    onError: (error: unknown) => {
      showErrorDialog("刷新 OAuth 凭证失败", "Failed to refresh OAuth credential", error);
    },
  });

  const logoutOAuthMut = useMutation({
    mutationFn: (id: string) => backend<ProviderOAuthStatusData>("logout_provider_oauth", { id }),
    onSuccess: (status) => {
      setEditError(null);
      syncProviderOAuthStatus(status);
      qc.invalidateQueries({ queryKey: ["providers"], refetchType: "none" });
    },
    onError: (error: unknown) => {
      showErrorDialog("断开 OAuth 失败", "Failed to disconnect OAuth", error);
    },
  });

  const updateMut = useMutation({
    mutationFn: ({ id, ...input }: UpdateProvider & { id: string }) =>
      backend("update_provider", { id, input }),
    onSuccess: () => {
      setEditError(null);
      qc.invalidateQueries({ queryKey: ["providers"] });
      setEditingId(null);
    },
    onError: (err: Error) => {
      setEditError(String(err));
      showErrorDialog("保存提供商失败", "Failed to save provider", err);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_provider", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("删除提供商失败", "Failed to delete provider", error);
    },
  });

  const copyMut = useMutation({
    mutationFn: (id: string) => backend<Provider>("copy_provider", { id }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["providers"] });
    },
    onError: (error: unknown) => {
      showErrorDialog("复制提供商失败", "Failed to copy provider", error);
    },
  });

  const [providerToDisable, setProviderToDisable] = useState<Provider | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_provider", { id, input: { is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("操作失败", "Operation failed", error);
    },
  });

  function appendTestLog(level: TestLogLevel, message: string) {
    setTestLogs((prev) => [...prev, { timestamp: nowTimestamp(), level, message }]);
  }

  function normalizeErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleZh: string, titleEn: string, error: unknown) {
    setErrorDialog({
      title: isZh ? titleZh : titleEn,
      description: normalizeErrorMessage(error),
    });
  }

  function closeTestDialog() {
    activeTestRunRef.current += 1;
    setIsTestRunning(false);
    setTestingId(null);
    setTestDialogOpen(false);
  }

  function stopCreateOAuthPolling() {
    if (createOAuthPollerRef.current != null) {
      window.clearInterval(createOAuthPollerRef.current);
      createOAuthPollerRef.current = null;
    }
  }

  function resetCreateOAuthState(cancelRemote = false) {
    const sessionId = createOAuthSession?.session_id;
    stopCreateOAuthPolling();
    setCreateOAuthSession(null);
    setCreateOAuthStatus(null);
    setCreateOAuthBusy(false);
    setCreateOAuthCallbackUrl("");
    setCreateOAuthCallbackError("");
    setCreateOAuthCode("");
    setCreateOAuthCopied(false);
    setCreateOAuthCopyFailed(false);
    if (cancelRemote && sessionId) {
      void backend("cancel_oauth_session", { sessionId }).catch(() => {
        // Best-effort cleanup only.
      });
    }
  }

  async function startCreateOAuth() {
    const vendor = selectedPreset?.id || form.vendor;
    if (!vendor) {
      setErrorDialog({
        title: isZh ? "无法发起 OAuth" : "Cannot start OAuth",
        description: isZh ? "请先选择 OAuth 供应商预设。" : "Please select an OAuth provider preset first.",
      });
      return;
    }

    resetCreateOAuthState(true);
    setCreateOAuthBusy(true);
    try {
      const init = await backend<OAuthSessionInitData>("init_oauth_session", {
        vendor,
        useProxy: Boolean(form.use_proxy),
      });
      setCreateOAuthSession(init);
      setCreateOAuthStatus({
        status: "pending",
        scheme: init.scheme,
        auth_url: init.auth_url,
        requires_manual_code: init.requires_manual_code,
        expires_in: init.expires_in,
        interval: init.interval,
        user_code: init.user_code,
        verification_uri_complete: init.verification_uri_complete,
      });
      setForm((prev) => {
        if (prev.name.trim()) return prev;
        const providerName = selectedPreset ? presetLabel(selectedPreset, false).trim() : vendor.trim();
        const suffix = init.user_code?.trim() ? `-${init.user_code.trim()}` : "";
        return {
          ...prev,
          name: providerName ? `${providerName}${suffix}` : prev.name,
        };
      });

      // Don't auto-open the browser — let the user click "Open Authorization Page"
      // after reviewing the URL. This avoids surprising pop-ups.

      if (init.requires_manual_code) {
        setCreateOAuthBusy(false);
        return;
      }

      const intervalMs = Math.max(2, Number(init.interval) || 2) * 1000;
      createOAuthPollerRef.current = window.setInterval(async () => {
        try {
          const status = await backend<OAuthSessionStatusData>("get_oauth_session_status", {
            sessionId: init.session_id,
          });
          setCreateOAuthStatus(status);
          if (status.status === "pending") {
            if ((status.expires_in ?? 0) <= 0) {
              stopCreateOAuthPolling();
              setCreateOAuthBusy(false);
              setCreateOAuthStatus({
                status: "error",
                code: "OAUTH_TIMEOUT",
                message: isZh ? "授权会话已超时，请重新发起授权。" : "OAuth session timed out, please start again.",
              });
            }
            return;
          }

          stopCreateOAuthPolling();
          setCreateOAuthBusy(false);
          if (status.status === "ready") {
            setForm((prev) => ({
              ...prev,
              base_url: toGatewayBaseUrl(status.resource_url ?? "") || prev.base_url,
            }));
          }
        } catch (error) {
          stopCreateOAuthPolling();
          setCreateOAuthBusy(false);
          setCreateOAuthStatus({
            status: "error",
            code: "OAUTH_STATUS_FAILED",
            message: normalizeErrorMessage(error),
          });
        }
      }, intervalMs);
    } catch (error) {
      setCreateOAuthBusy(false);
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_INIT_FAILED",
        message: normalizeErrorMessage(error),
      });
    }
  }

  async function cancelCreateOAuth() {
    const sessionId = createOAuthSession?.session_id;
    stopCreateOAuthPolling();
    setCreateOAuthBusy(false);
    if (!sessionId) {
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_CANCELLED",
        message: isZh ? "已取消" : "Cancelled",
      });
      return;
    }
    try {
      await backend("cancel_oauth_session", { sessionId });
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_CANCELLED",
        message: isZh ? "已取消" : "Cancelled",
      });
    } catch (error) {
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_CANCEL_FAILED",
        message: normalizeErrorMessage(error),
      });
    }
  }

  async function reopenCreateOAuthPage() {
    const authUrl = createOAuthSession?.auth_url || createOAuthSession?.verification_uri_complete;
    if (!authUrl) return;
    try {
      await openExternalUrl(authUrl);
    } catch (error) {
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_OPEN_BROWSER_FAILED",
        message: normalizeErrorMessage(error),
      });
    }
  }

  async function completeCreateOAuth() {
    const sessionId = createOAuthSession?.session_id;
    if (!sessionId) return;

    const callbackUrl = createOAuthCallbackUrl.trim();
    const code = createOAuthCode.trim();
    if (!callbackUrl && !code) {
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_INPUT_REQUIRED",
        message: isZh
          ? "请粘贴完整回调地址，或单独填写授权码。"
          : "Paste the full callback URL or enter the authorization code.",
      });
      return;
    }

    setCreateOAuthBusy(true);
    try {
      const status = await backend<OAuthSessionStatusData>("complete_oauth_session", {
        sessionId,
        callbackUrl: callbackUrl || undefined,
        code: code || undefined,
      });
      setCreateOAuthStatus(status);
      if (status.status === "ready") {
        setForm((prev) => ({
          ...prev,
          base_url: toGatewayBaseUrl(status.resource_url ?? "") || prev.base_url,
        }));
      }
    } catch (error) {
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_COMPLETE_FAILED",
        message: normalizeErrorMessage(error),
      });
    } finally {
      setCreateOAuthBusy(false);
    }
  }

  // --- Edit-mode OAuth re-authorization ---

  function stopEditOAuthPolling() {
    if (editOAuthPollerRef.current != null) {
      window.clearInterval(editOAuthPollerRef.current);
      editOAuthPollerRef.current = null;
    }
  }

  function resetEditOAuthState() {
    const sessionId = editOAuthSession?.session_id;
    stopEditOAuthPolling();
    setEditOAuthSession(null);
    setEditOAuthSessionStatus(null);
    setEditOAuthBusy(false);
    setEditOAuthCallbackUrl("");
    setEditOAuthCode("");
    if (sessionId) {
      void backend("cancel_oauth_session", { sessionId }).catch(() => {});
    }
  }

  async function startEditReauth(providerId: string, vendor: string, useProxy: boolean) {
    resetEditOAuthState();
    setShowEditReauth(true);
    setEditOAuthBusy(true);
    try {
      const init = await backend<OAuthSessionInitData>("init_oauth_session", {
        vendor,
        useProxy,
      });
      setEditOAuthSession(init);
      setEditOAuthSessionStatus({
        status: "pending",
        scheme: init.scheme,
        auth_url: init.auth_url,
        requires_manual_code: init.requires_manual_code,
        expires_in: init.expires_in,
        interval: init.interval,
        user_code: init.user_code,
        verification_uri_complete: init.verification_uri_complete,
      });

      const authUrl = init.auth_url || init.verification_uri_complete;
      if (authUrl) {
        void openExternalUrl(authUrl).catch(() => {});
      }

      if (init.requires_manual_code) {
        setEditOAuthBusy(false);
        return;
      }

      const intervalMs = Math.max(2, Number(init.interval) || 2) * 1000;
      editOAuthPollerRef.current = window.setInterval(async () => {
        try {
          const status = await backend<OAuthSessionStatusData>("get_oauth_session_status", {
            sessionId: init.session_id,
          });
          setEditOAuthSessionStatus(status);
          if (status.status === "pending") {
            if ((status.expires_in ?? 0) <= 0) {
              stopEditOAuthPolling();
              setEditOAuthBusy(false);
              setEditOAuthSessionStatus({
                status: "error",
                code: "OAUTH_TIMEOUT",
                message: isZh ? "授权会话已超时，请重新发起。" : "OAuth session timed out.",
              });
            }
            return;
          }
          stopEditOAuthPolling();
          setEditOAuthBusy(false);
          if (status.status === "ready") {
            // Bind the session to the existing provider
            try {
              await backend("bind_provider_oauth", {
                providerId,
                sessionId: init.session_id,
              });
              qc.invalidateQueries({ queryKey: ["providers"] });
              qc.invalidateQueries({ queryKey: ["provider-oauth-status", providerId] });
              setShowEditReauth(false);
              resetEditOAuthState();
            } catch (error) {
              setEditOAuthSessionStatus({
                status: "error",
                code: "OAUTH_BIND_FAILED",
                message: normalizeErrorMessage(error),
              });
            }
          }
        } catch (error) {
          stopEditOAuthPolling();
          setEditOAuthBusy(false);
          setEditOAuthSessionStatus({
            status: "error",
            code: "OAUTH_STATUS_FAILED",
            message: normalizeErrorMessage(error),
          });
        }
      }, intervalMs);
    } catch (error) {
      setEditOAuthBusy(false);
      setEditOAuthSessionStatus({
        status: "error",
        code: "OAUTH_INIT_FAILED",
        message: normalizeErrorMessage(error),
      });
    }
  }

  async function completeEditReauth(providerId: string) {
    const sessionId = editOAuthSession?.session_id;
    if (!sessionId) return;

    const callbackUrl = editOAuthCallbackUrl.trim();
    const code = editOAuthCode.trim();
    if (!callbackUrl && !code) {
      setEditOAuthSessionStatus({
        status: "error",
        code: "OAUTH_INPUT_REQUIRED",
        message: isZh
          ? "请粘贴完整回调地址，或单独填写授权码。"
          : "Paste the full callback URL or enter the authorization code.",
      });
      return;
    }

    setEditOAuthBusy(true);
    try {
      const status = await backend<OAuthSessionStatusData>("complete_oauth_session", {
        sessionId,
        callbackUrl: callbackUrl || undefined,
        code: code || undefined,
      });
      setEditOAuthSessionStatus(status);
      if (status.status === "ready") {
        try {
          await backend("bind_provider_oauth", {
            providerId,
            sessionId,
          });
          qc.invalidateQueries({ queryKey: ["providers"] });
          qc.invalidateQueries({ queryKey: ["provider-oauth-status", providerId] });
          setShowEditReauth(false);
          resetEditOAuthState();
        } catch (error) {
          setEditOAuthSessionStatus({
            status: "error",
            code: "OAUTH_BIND_FAILED",
            message: normalizeErrorMessage(error),
          });
        }
      }
    } catch (error) {
      setEditOAuthSessionStatus({
        status: "error",
        code: "OAUTH_COMPLETE_FAILED",
        message: normalizeErrorMessage(error),
      });
    } finally {
      setEditOAuthBusy(false);
    }
  }

  async function handleTest(provider: Provider) {
    const runId = activeTestRunRef.current + 1;
    activeTestRunRef.current = runId;
    const isCanceled = () => activeTestRunRef.current !== runId;

    setTestingId(provider.id);
    setTestTarget(provider);
    setTestLogs([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);
    setTestResult((prev) => {
      const next = { ...prev };
      delete next[provider.id];
      return next;
    });

    const finish = (result: TestResult, finalMessage: string, level: "success" | "error") => {
      if (isCanceled()) return;
      appendTestLog(level, finalMessage);
      setTestResult((prev) => ({ ...prev, [provider.id]: result }));
      setIsTestRunning(false);
      setTestingId(null);
    };

    try {
      const protocol = (resolveProtocol(provider.protocol || "openai") ?? "openai-compat") as ProviderProtocol;
      const baseUrl = provider.base_url?.trim() ?? "";

      appendTestLog("info", isZh ? `开始测试 ${provider.name}...` : `Start testing ${provider.name}...`);
      appendTestLog("info", isZh ? "▶ 连通性检测" : "▶ Connectivity check");
      appendTestLog("info", `→ [${protocol}] ${baseUrl}`);

      const connectivity = await backend<TestResult>("test_provider", { id: provider.id });
      if (isCanceled()) return;

      if (!connectivity.success) {
        const reason = connectivity.error ?? (isZh ? "连接失败" : "Connectivity check failed");
        finish(
          {
            success: false,
            latency_ms: connectivity.latency_ms ?? 0,
            model: undefined,
            error: reason,
          },
          `${isZh ? "✗ 连通性检测失败" : "✗ Connectivity check failed"}: ${reason}`,
          "error",
        );
        return;
      }

      appendTestLog(
        "success",
        `${isZh ? "✓ 连接成功，响应" : "✓ Connectivity ok, latency"} ${connectivity.latency_ms}ms`,
      );

      const modelsSource = provider.models_source?.trim();
      if (!modelsSource) {
        finish(
          { success: true, latency_ms: connectivity.latency_ms, model: undefined, error: undefined },
          isZh ? "✓ 未配置模型发现源，测试完成" : "✓ Model discovery source not configured, test finished",
          "success",
        );
        return;
      }

      appendTestLog("info", isZh ? "▶ 获取模型列表" : "▶ Fetch model list");
      appendTestLog("info", `→ ${modelsSource}`);

      const models = await backend<string[]>("test_provider_models", { id: provider.id });
      if (isCanceled()) return;

      if (!models.length) {
        finish(
          {
            success: false,
            latency_ms: connectivity.latency_ms,
            model: undefined,
            error: isZh ? "模型列表为空或格式异常" : "Model list is empty or malformed",
          },
          isZh ? "✗ 模型列表为空或格式异常" : "✗ Model list is empty or malformed",
          "error",
        );
        return;
      }

      appendTestLog(
        "success",
        `${isZh ? "✓ 认证通过，获取到" : "✓ Auth valid, fetched"} ${models.length} ${isZh ? "个模型" : "models"}`,
      );
      models.forEach((model) => appendTestLog("info", `· ${model}`));

      finish(
        {
          success: true,
          latency_ms: connectivity.latency_ms,
          model: models[0],
          error: undefined,
        },
        isZh ? "✓ 测试完成" : "✓ Test completed",
        "success",
      );
    } catch (error: unknown) {
      if (isCanceled()) return;
      const message = normalizeErrorMessage(error);
      finish(
        { success: false, latency_ms: 0, model: undefined, error: message },
        `${isZh ? "✗ 测试失败" : "✗ Test failed"}: ${message}`,
        "error",
      );
    }
  }

  function startEdit(p: Provider) {
    setEditingId(p.id);
    setEditError(null);
    setShowEditApiKey(false);
    const presetForEdit = providerPresets.find(
      (item) => item.id === (p.preset_key || DEFAULT_PRESET_ID),
    );
    const channel = p.channel || "default";
    const savedProtocol = (resolveProtocol(p.protocol) ?? "openai-compat") as ProviderProtocol;
    const safeProtocol = presetForEdit
      ? resolvePresetProtocol(presetForEdit, channel, savedProtocol)
      : savedProtocol;
    setEditForm({
      id: p.id,
      name: p.name,
      vendor: p.vendor ?? (p.preset_key || undefined),
      protocol: safeProtocol,
      base_url: p.base_url,
      use_proxy: p.use_proxy,
      preset_key: p.preset_key || DEFAULT_PRESET_ID,
      channel,
      models_source: p.models_source ?? "",
      static_models: p.static_models ?? "",
      api_key: p.api_key ?? "",
      auth_mode: normalizeAuthMode(p.auth_mode),
    });
  }

  function handlePresetChange(nextPresetId: string) {
    if (!nextPresetId) return;
    resetCreateOAuthState(true);
    setSelectedPresetId(nextPresetId);
    const preset = providerPresets.find((item) => item.id === nextPresetId);
    if (!preset) return;

    const nextChannelId = preset.channels?.[0]?.id ?? "";
    const nextProtocol = resolvePresetProtocol(preset, nextChannelId, (resolveProtocol(preset.defaultProtocol) ?? "openai-compat") as ProviderProtocol);
    const config = resolvePresetConfig(preset, nextProtocol, nextChannelId);
    const nextBaseUrl = config.baseUrl || protocolUrl(nextProtocol);

    setForm({
      ...emptyCreate,
      vendor: preset.id === DEFAULT_PRESET_ID ? undefined : preset.id,
      protocol: nextProtocol,
      base_url: nextBaseUrl,
      use_proxy: false,
      auth_mode: presetChannelAuthMode(preset, nextChannelId),
      preset_key: preset.id,
      channel: nextChannelId,
      models_source: config.modelsSource,
      static_models: config.staticModels,
      api_key: config.apiKey || "",
      name: "",
    });
  }

  function handlePresetChannelChange(nextChannelId: string) {
    if (!selectedPreset) return;
    const nextProtocol = resolvePresetProtocol(
      selectedPreset,
      nextChannelId,
      form.protocol as ProviderProtocol,
    );
    const config = resolvePresetConfig(selectedPreset, nextProtocol, nextChannelId);
    const nextBaseUrl = config.baseUrl || protocolUrl(nextProtocol);
    setForm((prev) => {
      const baseUrl = isVertexProviderSelection(prev)
        ? (nextBaseUrl || defaultVertexBaseUrl(nextProtocol))
        : nextBaseUrl;
      return {
        ...prev,
        channel: nextChannelId,
        protocol: nextProtocol,
        auth_mode: presetChannelAuthMode(selectedPreset, nextChannelId),
        base_url: baseUrl,
        models_source: config.modelsSource,
        static_models: config.staticModels,
        api_key: config.apiKey || prev.api_key,
      };
    });
  }

  function handleEditPresetChange(nextPresetId: string) {
    if (!nextPresetId) return;
    const preset = providerPresets.find((item) => item.id === nextPresetId);
    if (!preset) return;

    const nextChannelId = preset.channels?.[0]?.id ?? "";
    const nextAuthMode = presetChannelAuthMode(preset, nextChannelId);
    if (nextAuthMode === "oauth" && normalizeAuthMode(editingProvider?.auth_mode) !== "oauth") {
      setEditError(
        isZh
          ? "已有 Provider 不能在编辑时直接切到 OAuth 渠道，请新建一个 OAuth Provider。"
          : "Existing providers cannot switch directly to an OAuth channel while editing. Create a new OAuth provider instead.",
      );
      return;
    }

    setEditError(null);
    setEditForm((prev) =>
      prev
        ? (() => {
            const nextProtocol = resolvePresetProtocol(
              preset,
              nextChannelId,
              (prev.protocol as ProviderProtocol) || (resolveProtocol(preset.defaultProtocol) ?? "openai-compat") as ProviderProtocol,
            );
            const config = resolvePresetConfig(preset, nextProtocol, nextChannelId);
            const nextBaseUrl = config.baseUrl || protocolUrl(nextProtocol);
            const baseUrl = isVertexProviderSelection(prev)
              ? (nextBaseUrl || defaultVertexBaseUrl(nextProtocol))
              : nextBaseUrl;
            return {
              ...prev,
              vendor: preset.id === DEFAULT_PRESET_ID ? undefined : preset.id,
              preset_key: preset.id,
              channel: nextChannelId,
              protocol: nextProtocol,
              base_url: baseUrl,
              models_source: config.modelsSource,
              static_models: config.staticModels,
              api_key: config.apiKey || prev.api_key,
            };
          })()
        : prev,
    );
  }

  function closeCreateForm() {
    resetCreateOAuthState(true);
    setShowForm(false);
    setShowCreateApiKey(true);
    setSelectedPresetId(DEFAULT_PRESET_ID);
    setForm(emptyCreate);
  }

  const totalPages = Math.max(1, Math.ceil(providers.length / PAGE_SIZE));
  const pagedProviders = providers.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const createChannelOptions = selectedPreset ? presetChannels(selectedPreset) : [fallbackChannelPreset()];
  const createChannelValue =
    selectedPreset?.channels?.length
      ? (form.channel || createChannelOptions[0]?.id || "")
      : (createChannelOptions[0]?.id ?? "default");
  const createProtocolOptions = protocolOptions.filter((option) =>
    availableProtocolsForPreset(selectedPreset, createChannelValue).includes(option.value),
  );
  const hasCreatePresets = providerPresets.length > 0;
  const createResolvedAuthMode = presetChannelAuthMode(selectedPreset, createChannelValue);
  const createUsesVertexServiceAccount = isVertexProviderSelection(form);
  const createOAuthReady = createOAuthStatus?.status === "ready";
  const createOAuthRequiresManualCode =
    createOAuthStatus?.status === "pending"
      ? createOAuthStatus.requires_manual_code
      : createOAuthSession?.requires_manual_code ?? false;
  const showCreateOAuthGuide = createResolvedAuthMode === "oauth" && !createOAuthReady;

  useEffect(() => {
    if (page > totalPages - 1) {
      setPage(0);
    }
  }, [page, totalPages]);

  useEffect(() => {
    if (!logsContainerRef.current) return;
    logsContainerRef.current.scrollTop = logsContainerRef.current.scrollHeight;
  }, [testLogs]);

  useEffect(() => {
    saveProviderTestResults(testResult);
  }, [testResult]);

  useEffect(() => {
    if (isLoading) return;
    const validIds = new Set(providers.map((provider) => provider.id));
    setTestResult((prev) => {
      let changed = false;
      const next: Record<string, TestResult> = {};
      for (const [id, result] of Object.entries(prev)) {
        if (validIds.has(id)) {
          next[id] = result;
        } else {
          changed = true;
        }
      }
      return changed ? next : prev;
    });
  }, [isLoading, providers]);

  useEffect(() => {
    return () => {
      stopCreateOAuthPolling();
    };
  }, []);

  return (
    <div className="space-y-5">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold text-slate-900">{isZh ? "提供商" : "Providers"}</h1>
          <p className="mt-1 text-sm text-slate-500">
            {isZh ? "管理你的 LLM 提供商连接" : "Manage your LLM provider connections"}
          </p>
        </div>
        <Button
          onClick={() => {
            setEditingId(null);
            if (showForm) {
              closeCreateForm();
              return;
            }
            setShowForm(true);
            setShowCreateApiKey(true);
            resetCreateOAuthState(true);
            const initialPresetId = providerPresets[0]?.id;
            if (initialPresetId) {
              handlePresetChange(initialPresetId);
            } else {
              setSelectedPresetId("");
              setForm({ ...emptyCreate, auth_mode: "apikey" });
            }
          }}
          className="flex items-center gap-2"
        >
          <Plus className="h-4 w-4" />
          {isZh ? "新增提供商" : "Add Provider"}
        </Button>
      </div>

      {/* Create Form */}
      {showForm && (
        <div className="glass rounded-2xl p-6 space-y-6">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "新建提供商" : "New Provider"}</h2>
          <div className="space-y-3">
            {hasCreatePresets ? (
              <ToggleGroup
                type="single"
                value={selectedPresetId}
                onValueChange={handlePresetChange}
                className="provider-preset-group"
              >
                {[...providerPresets]
                  .sort((a, b) => (a.id === DEFAULT_PRESET_ID ? -1 : b.id === DEFAULT_PRESET_ID ? 1 : 0))
                  .map((preset) => (
                    <ToggleGroupItem
                      key={preset.id}
                      value={preset.id}
                      variant="outline"
                      size="lg"
                      className="provider-preset-card h-auto w-full flex-col gap-3 px-4 py-5"
                      aria-label={presetLabel(preset, isZh)}
                    >
                      {preset.icon === "nyro" || preset.icon === "custom" ? (
                        <>
                          <NyroIcon
                            size={26}
                            className="provider-preset-icon provider-preset-icon-custom provider-preset-icon-colored"
                          />
                          <NyroIcon
                            size={26}
                            monochrome
                            className="provider-preset-icon provider-preset-icon-custom provider-preset-icon-mono"
                          />
                        </>
                      ) : (
                        <>
                          <ProviderIcon
                            name={preset.icon ?? preset.label.en}
                            size={26}
                            className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                          />
                          <ProviderIcon
                            name={preset.icon ?? preset.label.en}
                            size={26}
                            monochrome
                            className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                          />
                        </>
                      )}
                      <span className={presetLabelClass(preset, isZh)}>{presetLabel(preset, isZh)}</span>
                    </ToggleGroupItem>
                  ))}
              </ToggleGroup>
            ) : (
              <div className="rounded-xl border border-dashed border-slate-200 bg-slate-50 px-4 py-5 text-sm text-slate-500">
                {isZh
                  ? "当前没有可用的厂商预设。"
                  : "No provider presets are available."}
              </div>
            )}
          </div>
          <div className="h-px bg-slate-200/70" />
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-4">
              <div className="col-span-2 space-y-2">
                <ToggleGroup
                  type="single"
                  value={createChannelValue}
                  onValueChange={(value) => {
                    if (!value || !selectedPreset?.channels?.length) return;
                    handlePresetChannelChange(value);
                  }}
                  className="provider-channel-group"
                >
                  {createChannelOptions.map((channel) => (
                    <ToggleGroupItem
                      key={channel.id}
                      value={channel.id}
                      variant="outline"
                      size="default"
                      className="provider-preset-card provider-channel-item"
                    >
                      {channelLabel(channel, isZh)}
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
              </div>
{showCreateOAuthGuide ? (
                <div className="col-span-2 rounded-xl border border-slate-200 bg-slate-50 p-4">
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <p className="text-sm font-semibold text-slate-800">
                        {isZh ? "OAuth 授权" : "OAuth Authorization"}
                      </p>
                      <p className="mt-1 text-xs text-slate-500">
                        {isZh ? "按下面步骤完成授权，完成后这里会自动收起。" : "Follow the steps below to finish authorization. This panel collapses after completion."}
                      </p>
                    </div>
                    <Badge variant={createOAuthBusy ? "secondary" : createOAuthStatus?.status === "error" ? "danger" : "secondary"}>
                      {createOAuthBusy
                        ? (isZh ? "进行中" : "In Progress")
                        : createOAuthStatus?.status === "error"
                          ? "Error"
                          : (isZh ? "待完成" : "Pending")}
                    </Badge>
                  </div>
                  <div className="mt-4 grid gap-3 md:grid-cols-2">
                    <div className={`rounded-lg border p-3 ${createOAuthSession ? "border-emerald-200 bg-emerald-50" : "border-slate-200 bg-white"}`}>
                      <div className="flex items-center gap-2 text-sm font-medium text-slate-800">
                        <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full text-xs ${createOAuthSession ? "bg-emerald-600 text-white" : "bg-slate-200 text-slate-700"}`}>1</span>
                        <span>{isZh ? "打开授权页" : "Open Authorization Page"}</span>
                      </div>
                      <p className="mt-2 text-xs text-slate-500">
                        {isZh ? "先打开浏览器完成登录授权。" : "Open the browser and complete sign-in first."}
                      </p>
                      <div className="mt-3 flex flex-wrap gap-2">
                        {!createOAuthSession ? (
                          <Button type="button" onClick={startCreateOAuth} disabled={createOAuthBusy || !selectedPreset}>
                            {createOAuthBusy ? (isZh ? "初始化中..." : "Initializing...") : (isZh ? "开始授权" : "Start Authorization")}
                          </Button>
                        ) : (
                          <>
                            <Button type="button" onClick={reopenCreateOAuthPage}>
                              {isZh ? "打开授权页" : "Open Authorization Page"}
                            </Button>
                            <Button
                              type="button"
                              variant="secondary"
                              onClick={async () => {
                                const url = createOAuthSession.auth_url || createOAuthSession.verification_uri_complete || "";
                                let ok = false;
                                try {
                                  await navigator.clipboard.writeText(url);
                                  ok = true;
                                } catch {
                                  // fallback for HTTP
                                  try {
                                    const ta = document.createElement("textarea");
                                    ta.value = url;
                                    ta.style.position = "fixed";
                                    ta.style.opacity = "0";
                                    document.body.appendChild(ta);
                                    ta.select();
                                    ok = document.execCommand("copy");
                                    document.body.removeChild(ta);
                                  } catch { /* ignore */ }
                                }
                                if (ok) {
                                  setCreateOAuthCopyFailed(false);
                                  setCreateOAuthCopied(true);
                                } else {
                                  setCreateOAuthCopyFailed(true);
                                }
                              }}
                            >
                              {createOAuthCopied ? (isZh ? "已复制" : "Copied!") : (isZh ? "复制链接" : "Copy Link")}
                            </Button>
                          </>
                        )}
                      </div>
                      {createOAuthCopied && (
                        <p className="mt-2 text-xs text-emerald-600">
                          {isZh ? "链接已复制，请在浏览器中打开并完成授权登录。" : "Link copied. Please open it in your browser and complete the authorization."}
                        </p>
                      )}
                      {createOAuthCopyFailed && createOAuthSession && (
                        <div className="mt-2 space-y-1">
                          <p className="text-xs text-rose-600">{isZh ? "复制失败，请手动复制以下链接并在浏览器中打开完成授权：" : "Copy failed. Please manually copy the link below and open it in your browser to authorize:"}</p>
                          <div className="rounded-lg border border-dashed border-slate-300 bg-white px-3 py-2 text-xs text-slate-600 break-all select-all">
                            {createOAuthSession.auth_url || createOAuthSession.verification_uri_complete}
                          </div>
                        </div>
                      )}
                    </div>
                    <div className={`rounded-lg border p-3 ${createOAuthStatus?.status === "error" ? "border-rose-200 bg-rose-50" : createOAuthSession ? "border-sky-200 bg-sky-50" : "border-slate-200 bg-white"}`}>
                      <div className="flex items-center gap-2 text-sm font-medium text-slate-800">
                        <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full text-xs ${(createOAuthStatus?.status === "pending" || createOAuthStatus?.status === "error") ? "bg-sky-600 text-white" : "bg-slate-200 text-slate-700"}`}>2</span>
                        <span>{isZh ? "粘贴回调结果" : "Paste Callback Result"}</span>
                      </div>
                      <p className="mt-2 text-xs text-slate-500">
                        {isZh ? "浏览器授权完成后，把回调地址或授权码粘贴到这里。" : "After browser authorization, paste the callback URL or authorization code here."}
                      </p>
                      <div className="mt-3 space-y-3">
                        <div className="space-y-2">
                          <FieldLabel>{isZh ? "回调地址" : "Callback URL"}</FieldLabel>
                          <Input
                            placeholder={isZh ? "例如：http://localhost:1455/auth/callback?code=..." : "For example: http://localhost:1455/auth/callback?code=..."}
                            value={createOAuthCallbackUrl}
                            onChange={(e) => {
                              const url = e.target.value;
                              setCreateOAuthCallbackUrl(url);
                              setCreateOAuthCallbackError("");
                              if (!url.trim()) return;
                              try {
                                const parsed = new URL(url);
                                const code = parsed.searchParams.get("code");
                                if (code) {
                                  setCreateOAuthCode(code);
                                } else if (parsed.searchParams.has("error")) {
                                  const desc = parsed.searchParams.get("error_description") || parsed.searchParams.get("error") || "";
                                  setCreateOAuthCallbackError(
                                    isZh
                                      ? `授权失败：${desc || "回调中包含 error 参数"}`
                                      : `Authorization failed: ${desc || "callback contains error parameter"}`,
                                  );
                                } else {
                                  setCreateOAuthCallbackError(
                                    isZh
                                      ? "回调地址中没有找到 code 参数，请确认粘贴了正确的回调地址。"
                                      : "No code parameter found in URL. Please confirm you pasted the correct callback URL.",
                                  );
                                }
                              } catch {
                                setCreateOAuthCallbackError(
                                  isZh
                                    ? "输入的内容不是有效的 URL，请粘贴完整的回调地址。"
                                    : "Input is not a valid URL. Please paste the complete callback URL.",
                                );
                              }
                            }}
                            disabled={!createOAuthSession || createOAuthBusy}
                          />
                          {createOAuthCallbackError && (
                            <p className="text-xs text-rose-600">{createOAuthCallbackError}</p>
                          )}
                        </div>
                        <div className="space-y-2">
                          <FieldLabel>{isZh ? "授权码" : "Authorization Code"}</FieldLabel>
                          <Input
                            placeholder="code..."
                            value={createOAuthCode}
                            onChange={(e) => setCreateOAuthCode(e.target.value)}
                            disabled={!createOAuthSession || createOAuthBusy}
                          />
                        </div>
                        <div className="flex flex-wrap gap-2">
                          <Button
                            type="button"
                            onClick={completeCreateOAuth}
                            disabled={createOAuthBusy || !createOAuthSession || !!createOAuthCallbackError}
                          >
                            {createOAuthBusy ? (isZh ? "提交中..." : "Submitting...") : (isZh ? "提交结果" : "Submit")}
                          </Button>
                          <Button
                            type="button"
                            variant="secondary"
                            onClick={cancelCreateOAuth}
                            disabled={!createOAuthSession}
                          >
                            {isZh ? "取消" : "Cancel"}
                          </Button>
                        </div>
                      </div>
                    </div>
                  </div>
                  <div className="mt-3 text-xs">
                    {createOAuthStatus?.status === "error" ? (
                      <p className="text-rose-600">{createOAuthStatus.code}: {createOAuthStatus.message}</p>
                    ) : createOAuthSession ? (
                      <p className="text-slate-500">
                        {createOAuthRequiresManualCode
                          ? (isZh ? "完成步骤 1 后，再执行步骤 2。" : "Finish step 1, then complete step 2.")
                          : (isZh ? "等待浏览器完成授权。" : "Waiting for browser authorization to complete.")}
                      </p>
                    ) : (
                      <p className="text-slate-500">{isZh ? "先执行步骤 1。" : "Start with step 1."}</p>
                    )}
                  </div>
                </div>
              ) : null}
              {createResolvedAuthMode === "oauth" && createOAuthReady ? (
                <div className="col-span-2 rounded-xl border border-emerald-200 bg-emerald-50 p-4 text-sm text-emerald-700">
                  <div className="font-medium">{isZh ? "OAuth 授权已完成" : "OAuth Authorization Completed"}</div>
                  <div className="mt-1 text-xs text-emerald-600">
                    {isZh ? "授权信息已就绪，继续填写下面配置并创建即可。" : "Authorization is ready. Continue with the configuration below and create the provider."}
                  </div>
                </div>
              ) : null}
              <div className="space-y-2">
                <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                <Input
                  placeholder={isZh ? "例如 OpenAI 生产" : "e.g. OpenAI Production"}
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </div>
              {createResolvedAuthMode !== "oauth" && (
                <div className={createUsesVertexServiceAccount ? "col-span-2 space-y-2" : "space-y-2"}>
                  <FieldLabel
                    info={
                      createUsesVertexServiceAccount
                        ? (isZh ? "粘贴 Google Cloud 服务账号 JSON，需包含 project_id、client_email、private_key。" : "Paste the Google Cloud service account JSON with project_id, client_email, and private_key.")
                        : undefined
                    }
                  >
                    {createUsesVertexServiceAccount ? (isZh ? "服务账号 JSON" : "Service Account JSON") : "API Key"}
                  </FieldLabel>
                  {createUsesVertexServiceAccount ? (
                    <textarea
                      placeholder={isZh ? "{\n  \"project_id\": \"...\",\n  \"client_email\": \"...\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\n...\"\n}" : "{\n  \"project_id\": \"...\",\n  \"client_email\": \"...\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\n...\"\n}"}
                      value={form.api_key}
                      rows={8}
                      className="min-h-32 w-full resize-y rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300"
                      autoCapitalize="none"
                      autoCorrect="off"
                      spellCheck={false}
                      onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                    />
                  ) : (
                    <div className="relative">
                      <Input
                        placeholder="sk-..."
                        type={showCreateApiKey ? "text" : "password"}
                        value={form.api_key}
                        className="pr-10"
                        onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                      />
                      <button
                        type="button"
                        onClick={() => setShowCreateApiKey((prev) => !prev)}
                        className="absolute top-1/2 right-3 -translate-y-1/2 text-slate-400 hover:text-slate-600 cursor-pointer"
                        aria-label={showCreateApiKey ? (isZh ? "隐藏 API Key" : "Hide API key") : (isZh ? "显示 API Key" : "Show API key")}
                      >
                        {showCreateApiKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                      </button>
                    </div>
                  )}
                </div>
              )}
              {createResolvedAuthMode !== "oauth" && (
              <div className="space-y-2">
                <FieldLabel>{isZh ? "协议" : "Protocol"}</FieldLabel>
                <Select
                  value={form.protocol}
                  onValueChange={(value) => {
                    const nextProtocol = value as ProviderProtocol;
                    const config = selectedPreset
                      ? resolvePresetConfig(selectedPreset, nextProtocol, form.channel)
                      : {
                          baseUrl: protocolUrl(nextProtocol),
                          modelsSource: defaultModelsEndpoint(protocolUrl(nextProtocol), nextProtocol),
                          staticModels: form.static_models ?? "",
                        };
                    const nextBaseUrl =
                      selectedPreset && selectedPreset.id !== DEFAULT_PRESET_ID
                        ? (config.baseUrl || form.base_url)
                        : config.baseUrl;
                    const baseUrl = createUsesVertexServiceAccount
                      ? (nextBaseUrl || defaultVertexBaseUrl(nextProtocol))
                      : nextBaseUrl;
                    setForm({
                      ...form,
                      protocol: nextProtocol,
                      base_url: baseUrl,
                      models_source: form.models_source,
                      static_models: config.staticModels,
                    });
                  }}
                >
                  <SelectTrigger>
                    <SelectValue placeholder={isZh ? "选择协议" : "Select protocol"} />
                  </SelectTrigger>
                  <SelectContent>
                    {createProtocolOptions.map((option) => (
                      <SelectItem key={option.value} value={option.value}>
                        {option.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              )}
              {createResolvedAuthMode !== "oauth" && (
              <div className="space-y-2">
                <FieldLabel>Base URL</FieldLabel>
                <Input
                  placeholder={isZh ? "输入上游基础地址" : "Enter upstream base URL"}
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                />
              </div>
              )}
              {createResolvedAuthMode !== "oauth" && (
              <div className="space-y-2">
                <FieldLabel
                  info={
                    isZh
                      ? "用于创建路由时自动获取可用模型列表"
                      : "Used to auto-fetch available model list when creating routes"
                  }
                >
                  {isZh ? "模型发现源" : "Model Discovery Source"}
                </FieldLabel>
                <Input
                  placeholder={isZh ? "可选，支持 https:// 或 ai://models.dev/..." : "Optional, supports https:// or ai://models.dev/..."}
                  value={form.models_source ?? ""}
                  onChange={(e) => setForm({ ...form, models_source: e.target.value })}
                />
              </div>
              )}
              {isGlobalProxyEnabled && createResolvedAuthMode !== "oauth" && (
                <div className="space-y-2">
                  <FieldLabel>{isZh ? "使用本地代理" : "Use Local Proxy"}</FieldLabel>
                  <div className="flex items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
                    <span className="text-xs text-slate-600">
                      {isZh ? "开启后走设置页中的本地代理地址" : "Route requests via local proxy from settings"}
                    </span>
                    <Switch
                      checked={Boolean(form.use_proxy)}
                      onCheckedChange={(checked) => setForm({ ...form, use_proxy: checked })}
                    />
                  </div>
                </div>
              )}
            </div>
              <div className="flex gap-3">
                <Button
                  onClick={() => {
                    const protocol = form.protocol || "openai-compat";
                    const baseUrl = toGatewayBaseUrl(form.base_url ?? "");
                    const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                    if (validation) {
                      setErrorDialog({
                        title: isZh ? "创建提供商失败" : "Failed to create provider",
                        description: validation,
                      });
                      return;
                    }
                    const input: CreateProvider = {
                      ...form,
                      protocol,
                      base_url: baseUrl,
                    };
                    if (createResolvedAuthMode === "oauth") {
                      const sessionId = createOAuthSession?.session_id;
                      if (!sessionId) return;
                      createOAuthMut.mutate({ sessionId, input });
                      return;
                    }
                    createMut.mutate(input);
                  }}
                  disabled={
                    createMut.isPending
                    || createOAuthMut.isPending
                    || !form.name.trim()
                    || (createResolvedAuthMode === "apikey" && !form.api_key)
                    || (createResolvedAuthMode === "oauth" && !createOAuthReady)
                  }
                >
                  {(createMut.isPending || createOAuthMut.isPending)
                    ? (isZh ? "创建中..." : "Creating...")
                    : (isZh ? "创建" : "Create")}
                </Button>
              <Button
                onClick={closeCreateForm}
                variant="secondary"
              >
                {isZh ? "取消" : "Cancel"}
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* List */}
      {isLoading ? (
        <div className="text-center text-sm text-slate-500 py-12">{isZh ? "加载中..." : "Loading..."}</div>
      ) : providers.length === 0 ? (
        <div className="glass rounded-2xl p-12 text-center">
          <Server className="mx-auto h-10 w-10 text-slate-400" />
          <p className="mt-3 text-sm text-slate-500">{isZh ? "还没有配置提供商" : "No providers configured yet"}</p>
          <p className="mt-1 text-xs text-slate-400">{isZh ? "添加提供商后开始使用" : "Add a provider to get started"}</p>
        </div>
      ) : (
        <div className="grid gap-3">
          {pagedProviders.map((p) => {
            const tr = testResult[p.id];
            const status = tr ? (tr.success ? "success" : "failed") : null;
            const isEditing = editingId === p.id;
            const editingPresetId = editForm.preset_key || DEFAULT_PRESET_ID;
            const editingPreset =
              providerPresets.find((preset) => preset.id === editingPresetId) ?? providerPresets[0] ?? null;
            const protocolLabels = [(resolveProtocol(p.protocol || "openai") ?? "openai-compat") as ProviderProtocol];
            const selectedPreset = providerPresets.find((preset) => preset.id === (p.preset_key || p.vendor || ""));
            const selectedProviderName = selectedPreset
              ? presetLabel(selectedPreset, isZh)
              : (p.vendor || p.preset_key || p.name);

            if (isEditing) {
              const editingChannelOptions = presetChannels(editingPreset);
              const editingChannelValue =
                editingPreset?.channels?.length
                  ? (editForm.channel || editingChannelOptions[0]?.id || "")
                  : (editingChannelOptions[0]?.id ?? "default");
              const editingProtocolOptions = protocolOptions.filter((option) =>
                availableProtocolsForPreset(editingPreset, editingChannelValue).includes(option.value),
              );
              const editingResolvedAuthMode = presetChannelAuthMode(editingPreset, editingChannelValue);
              const editUsesVertexServiceAccount = isVertexProviderSelection(editForm);
              const currentProviderIsOAuth =
                normalizeAuthMode(p.auth_mode) === "oauth"
                || normalizeAuthMode(editForm.auth_mode) === "oauth";
              const editRequiresNewOAuthProvider = editingResolvedAuthMode === "oauth" && !currentProviderIsOAuth;
              const editOAuthStatus = editOAuthStatusQuery.data;
              return (
                <div key={p.id} className="glass rounded-2xl p-5 space-y-4">
                  <div className="flex items-center justify-between">
                    <h3 className="text-sm font-semibold text-slate-900">{isZh ? "编辑提供商" : "Edit Provider"}</h3>
                    <button onClick={() => setEditingId(null)} className="p-1 text-slate-400 hover:text-slate-600 cursor-pointer">
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                  <div className="space-y-3">
                    <p className="text-sm font-semibold text-slate-700">
                      {isZh ? "1. 供应商" : "1. Provider"}
                    </p>
                    <ToggleGroup
                      type="single"
                      value={editingPresetId}
                      onValueChange={handleEditPresetChange}
                      className="provider-preset-group"
                    >
                      {[...providerPresets]
                        .sort((a, b) => (a.id === DEFAULT_PRESET_ID ? -1 : b.id === DEFAULT_PRESET_ID ? 1 : 0))
                        .map((preset) => (
                        <ToggleGroupItem
                          key={preset.id}
                          value={preset.id}
                          variant="outline"
                          size="lg"
                          disabled={presetChannelAuthMode(preset, preset.channels?.[0]?.id ?? "") === "oauth" && !currentProviderIsOAuth}
                          className="provider-preset-card h-auto w-full flex-col gap-3 px-4 py-5 disabled:cursor-not-allowed disabled:opacity-50"
                          aria-label={presetLabel(preset, isZh)}
                        >
                          {preset.icon === "nyro" || preset.icon === "custom" ? (
                            <>
                              <NyroIcon
                                size={26}
                                className="provider-preset-icon provider-preset-icon-custom provider-preset-icon-colored"
                              />
                              <NyroIcon
                                size={26}
                                monochrome
                                className="provider-preset-icon provider-preset-icon-custom provider-preset-icon-mono"
                              />
                            </>
                          ) : (
                            <>
                              <ProviderIcon
                                name={preset.icon ?? preset.label.en}
                                size={26}
                                className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                              />
                              <ProviderIcon
                                name={preset.icon ?? preset.label.en}
                                size={26}
                                monochrome
                                className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                              />
                            </>
                          )}
                          <span className={presetLabelClass(preset, isZh)}>{presetLabel(preset, isZh)}</span>
                        </ToggleGroupItem>
                      ))}
                    </ToggleGroup>
                  </div>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="col-span-2 space-y-2">
                      <FieldLabel>{isZh ? "渠道" : "Channel"}</FieldLabel>
                      <ToggleGroup
                        type="single"
                        value={editingChannelValue}
                        onValueChange={(value) => {
                          if (!value || !editingPreset?.channels?.length) return;
                          const nextAuthMode = presetChannelAuthMode(editingPreset, value);
                          if (nextAuthMode === "oauth" && !currentProviderIsOAuth) {
                            setEditError(
                              isZh
                                ? "已有 Provider 不能在编辑时直接切到 OAuth 渠道，请新建一个 OAuth Provider。"
                                : "Existing providers cannot switch directly to an OAuth channel while editing. Create a new OAuth provider instead.",
                            );
                            return;
                          }
                          const resolvedProtocol = resolvePresetProtocol(
                            editingPreset,
                            value,
                            (editForm.protocol as ProviderProtocol) || (resolveProtocol(editingPreset.defaultProtocol) ?? "openai-compat") as ProviderProtocol,
                          );
                          const config = resolvePresetConfig(
                            editingPreset,
                            resolvedProtocol,
                            value,
                          );
                          const nextBaseUrl = config.baseUrl || protocolUrl(resolvedProtocol);
                          const baseUrl = editUsesVertexServiceAccount
                            ? (nextBaseUrl || defaultVertexBaseUrl(resolvedProtocol))
                            : nextBaseUrl;
                          setEditError(null);
                          setEditForm({
                            ...editForm,
                            channel: value,
                            protocol: resolvedProtocol,
                            base_url: baseUrl,
                            models_source: config.modelsSource,
                            static_models: config.staticModels,
                          });
                        }}
                        className="provider-channel-group"
                      >
                        {editingChannelOptions.map((channel) => (
                          <ToggleGroupItem
                            key={channel.id}
                            value={channel.id}
                            variant="outline"
                            size="default"
                            disabled={presetChannelAuthMode(editingPreset, channel.id) === "oauth" && !currentProviderIsOAuth}
                            className="provider-preset-card provider-channel-item disabled:cursor-not-allowed disabled:opacity-50"
                          >
                            {channelLabel(channel, isZh)}
                          </ToggleGroupItem>
                        ))}
                      </ToggleGroup>
                    </div>
                    {editingResolvedAuthMode === "oauth" ? (
                      <details className="col-span-2 rounded-xl border border-slate-200 bg-slate-50" open={editOAuthStatus?.status !== "connected"}>
                        <summary className="flex items-center justify-between gap-3 p-4 cursor-pointer list-none [&::-webkit-details-marker]:hidden">
                          <div className="flex items-center gap-2">
                            <p className="text-sm font-semibold text-slate-800">
                              {isZh ? "OAuth 授权" : "OAuth Authorization"}
                            </p>
                            {editOAuthStatus?.status === "connected" && (
                              <span className="text-xs text-slate-400">
                                {isZh ? "（点击展开）" : "(click to expand)"}
                              </span>
                            )}
                          </div>
                          <Badge variant={
                            editOAuthStatus?.status === "connected" ? "success"
                              : editOAuthStatus?.status === "error" ? "danger" : "secondary"
                          }>
                            {editOAuthStatusQuery.isLoading
                              ? (isZh ? "读取中" : "Loading")
                              : editOAuthStatus?.status || (isZh ? "未知" : "Unknown")}
                          </Badge>
                        </summary>
                        <div className="px-4 pb-4">
                        {editRequiresNewOAuthProvider ? (
                          <div className="mt-4 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700">
                            <span>{isZh ? "已有 Provider 不能在编辑时直接切到 OAuth 渠道。" : "Existing providers cannot switch directly to an OAuth channel while editing."}</span>
                            <button
                              type="button"
                              className="ml-1 font-medium text-amber-800 underline hover:text-amber-900 cursor-pointer"
                              onClick={() => {
                                setEditingId(null);
                                setShowForm(true);
                                handlePresetChange("openai");
                              }}
                            >
                              {isZh ? "新建 OAuth Provider" : "Create new OAuth Provider"}
                            </button>
                          </div>
                        ) : editOAuthStatus?.status === "connected" ? (
                          <div className="mt-4 space-y-3">
                            <div className="rounded-lg border border-slate-200 bg-white px-3 py-2">
                              <div className="text-xs font-medium text-slate-700">
                                {isZh ? "当前授权状态" : "Authorization Status"}
                              </div>
                              <div className="mt-1 text-xs text-slate-500">
                                {editOAuthStatus?.expires_at && new Date(editOAuthStatus.expires_at).getTime() < Date.now()
                                  ? (isZh ? "Access Token 已过期，请点击续期。" : "Access token has expired. Please renew.")
                                  : (isZh ? "授权有效，可正常使用当前 Provider。" : "Authorization is valid. The provider is ready to use.")}
                              </div>
                            </div>
                            <div className="flex flex-wrap gap-2">
                              <Button
                                type="button"
                                variant="secondary"
                                onClick={() => reconnectOAuthMut.mutate(p.id)}
                                disabled={reconnectOAuthMut.isPending || logoutOAuthMut.isPending}
                              >
                                {reconnectOAuthMut.isPending ? (isZh ? "续期中..." : "Renewing...") : (isZh ? "续期 Token" : "Renew Token")}
                              </Button>
                              <Button
                                type="button"
                                variant="secondary"
                                onClick={() => logoutOAuthMut.mutate(p.id)}
                                disabled={logoutOAuthMut.isPending || reconnectOAuthMut.isPending}
                              >
                                {logoutOAuthMut.isPending ? (isZh ? "断开中..." : "Disconnecting...") : (isZh ? "断开授权" : "Disconnect")}
                              </Button>
                            </div>
                          </div>
                        ) : (
                          <div className="mt-4 space-y-3">
                            <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2">
                              <div className="text-xs font-medium text-amber-700">
                                {isZh ? "当前授权状态" : "Authorization Status"}
                              </div>
                              <div className="mt-1 text-xs text-amber-600">
                                {editOAuthStatus?.status === "error"
                                  ? (isZh ? "授权失败（Refresh Token 可能已失效），请重新授权。" : "Authorization failed (refresh token may have expired). Please re-authorize.")
                                  : (isZh ? "未授权，请进行 OAuth 授权。" : "Not authorized. Please complete OAuth authorization.")}
                              </div>
                            </div>
                            {editOAuthStatus?.last_error ? (
                              <p className="rounded-lg border border-rose-200 bg-rose-50 px-3 py-2 text-xs text-rose-600">{editOAuthStatus.last_error}</p>
                            ) : null}
                            {!showEditReauth ? (
                              <Button
                                type="button"
                                onClick={() => startEditReauth(p.id, p.vendor || p.preset_key || "", p.use_proxy)}
                              >
                                {isZh ? "开始授权" : "Start Authorization"}
                              </Button>
                            ) : (
                              <div className="space-y-3">
                                <div className="grid gap-3 md:grid-cols-2">
                                  <div className={`rounded-lg border p-3 ${editOAuthSession ? "border-emerald-200 bg-emerald-50" : "border-slate-200 bg-white"}`}>
                                    <div className="flex items-center gap-2 text-sm font-medium text-slate-800">
                                      <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full text-xs ${editOAuthSession ? "bg-emerald-600 text-white" : "bg-slate-200 text-slate-700"}`}>1</span>
                                      <span>{isZh ? "打开授权页" : "Open Authorization Page"}</span>
                                    </div>
                                    <p className="mt-2 text-xs text-slate-500">
                                      {isZh ? "先打开浏览器完成登录授权。" : "Open the browser and complete sign-in first."}
                                    </p>
                                    {editOAuthSession ? (
                                      <div className="mt-3 rounded-lg border border-dashed border-slate-300 bg-white px-3 py-2 text-xs text-slate-600">
                                        <div className="font-medium text-slate-700">{isZh ? "授权链接" : "Authorization URL"}</div>
                                        <div className="mt-1 break-all">{editOAuthSession.auth_url || editOAuthSession.verification_uri_complete}</div>
                                      </div>
                                    ) : null}
                                  </div>
                                  <div className={`rounded-lg border p-3 ${editOAuthSession ? "border-sky-200 bg-sky-50" : "border-slate-200 bg-white"}`}>
                                    <div className="flex items-center gap-2 text-sm font-medium text-slate-800">
                                      <span className={`inline-flex h-6 w-6 items-center justify-center rounded-full text-xs ${editOAuthSession ? "bg-sky-600 text-white" : "bg-slate-200 text-slate-700"}`}>2</span>
                                      <span>{isZh ? "粘贴回调结果" : "Paste Callback Result"}</span>
                                    </div>
                                    <p className="mt-2 text-xs text-slate-500">
                                      {isZh ? "浏览器授权完成后，把回调地址或授权码粘贴到这里。" : "After browser authorization, paste the callback URL or authorization code here."}
                                    </p>
                                    <div className="mt-3 space-y-3">
                                      <div className="space-y-2">
                                        <FieldLabel>{isZh ? "回调地址" : "Callback URL"}</FieldLabel>
                                        <Input
                                          placeholder={isZh ? "例如：http://localhost:1455/auth/callback?code=..." : "For example: http://localhost:1455/auth/callback?code=..."}
                                          value={editOAuthCallbackUrl}
                                          onChange={(e) => {
                                            const url = e.target.value;
                                            setEditOAuthCallbackUrl(url);
                                            try {
                                              const parsed = new URL(url);
                                              const codeParam = parsed.searchParams.get("code");
                                              if (codeParam) setEditOAuthCode(codeParam);
                                            } catch {
                                              // not a valid URL yet
                                            }
                                          }}
                                          disabled={!editOAuthSession || editOAuthBusy}
                                        />
                                      </div>
                                      <div className="space-y-2">
                                        <FieldLabel>{isZh ? "授权码" : "Authorization Code"}</FieldLabel>
                                        <Input
                                          placeholder="code..."
                                          value={editOAuthCode}
                                          onChange={(e) => setEditOAuthCode(e.target.value)}
                                          disabled={!editOAuthSession || editOAuthBusy}
                                        />
                                      </div>
                                      <div className="flex flex-wrap gap-2">
                                        <Button
                                          type="button"
                                          onClick={() => completeEditReauth(p.id)}
                                          disabled={editOAuthBusy || !editOAuthSession}
                                        >
                                          {editOAuthBusy ? (isZh ? "提交中..." : "Submitting...") : (isZh ? "提交结果" : "Submit")}
                                        </Button>
                                        <Button
                                          type="button"
                                          variant="secondary"
                                          onClick={() => { resetEditOAuthState(); setShowEditReauth(false); }}
                                        >
                                          {isZh ? "取消" : "Cancel"}
                                        </Button>
                                      </div>
                                    </div>
                                  </div>
                                </div>
                                {editOAuthSessionStatus?.status === "error" ? (
                                  <p className="text-xs text-rose-600">{editOAuthSessionStatus.code}: {editOAuthSessionStatus.message}</p>
                                ) : editOAuthSession ? (
                                  <p className="text-xs text-slate-500">
                                    {editOAuthSession.requires_manual_code
                                      ? (isZh ? "完成步骤 1 后，再执行步骤 2。" : "Finish step 1, then complete step 2.")
                                      : (isZh ? "等待浏览器完成授权。" : "Waiting for browser authorization to complete.")}
                                  </p>
                                ) : null}
                              </div>
                            )}
                          </div>
                        )}
                        </div>
                      </details>
                    ) : null}
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                      <Input
                        placeholder={isZh ? "提供商名称" : "Provider name"}
                        value={editForm.name ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, name: e.target.value })}
                      />
                    </div>
                    {editingResolvedAuthMode !== "oauth" ? (
                      <div className={editUsesVertexServiceAccount ? "col-span-2 space-y-2" : "space-y-2"}>
                        <FieldLabel
                          info={
                            editUsesVertexServiceAccount
                              ? (isZh ? "粘贴 Google Cloud 服务账号 JSON，需包含 project_id、client_email、private_key。" : "Paste the Google Cloud service account JSON with project_id, client_email, and private_key.")
                              : undefined
                          }
                        >
                          {editUsesVertexServiceAccount ? (isZh ? "服务账号 JSON" : "Service Account JSON") : (isZh ? "API Key" : "API Key")}
                        </FieldLabel>
                        {editUsesVertexServiceAccount ? (
                          <textarea
                            placeholder={isZh ? "{\n  \"project_id\": \"...\",\n  \"client_email\": \"...\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\n...\"\n}" : "{\n  \"project_id\": \"...\",\n  \"client_email\": \"...\",\n  \"private_key\": \"-----BEGIN PRIVATE KEY-----\\n...\"\n}"}
                            value={editForm.api_key ?? ""}
                            rows={8}
                            className="min-h-32 w-full resize-y rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300"
                            autoCapitalize="none"
                            autoCorrect="off"
                            spellCheck={false}
                            onChange={(e) => setEditForm({ ...editForm, api_key: e.target.value })}
                          />
                        ) : (
                          <div className="relative">
                            <Input
                              placeholder="sk-..."
                              type={showEditApiKey ? "text" : "password"}
                              value={editForm.api_key ?? ""}
                              className="pr-10"
                              onChange={(e) => setEditForm({ ...editForm, api_key: e.target.value })}
                            />
                            <button
                              type="button"
                              onClick={() => setShowEditApiKey((prev) => !prev)}
                              className="absolute top-1/2 right-3 -translate-y-1/2 text-slate-400 hover:text-slate-600 cursor-pointer"
                              aria-label={showEditApiKey ? (isZh ? "隐藏 API Key" : "Hide API key") : (isZh ? "显示 API Key" : "Show API key")}
                            >
                              {showEditApiKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                            </button>
                          </div>
                        )}
                      </div>
                    ) : null}
                    {editingResolvedAuthMode !== "oauth" ? (
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "协议" : "Protocol"}</FieldLabel>
                      <Select
                        value={editForm.protocol ?? ""}
                        onValueChange={(value) => {
                          const nextProtocol = value as ProviderProtocol;
                          const config = editingPreset
                            ? resolvePresetConfig(editingPreset, nextProtocol, editForm.channel ?? undefined)
                            : {
                                baseUrl: protocolUrl(nextProtocol),
                                modelsSource: defaultModelsEndpoint(protocolUrl(nextProtocol), nextProtocol),
                                staticModels: editForm.static_models ?? "",
                              };
                          const nextBaseUrl =
                            editingPreset && editingPreset.id !== DEFAULT_PRESET_ID
                              ? (config.baseUrl || editForm.base_url || "")
                              : config.baseUrl;
                          const baseUrl = editUsesVertexServiceAccount
                            ? (nextBaseUrl || defaultVertexBaseUrl(nextProtocol))
                            : nextBaseUrl;
                          setEditForm({
                            ...editForm,
                            protocol: nextProtocol,
                            base_url: baseUrl,
                            models_source: editForm.models_source,
                            static_models: config.staticModels,
                          });
                        }}
                      >
                        <SelectTrigger>
                          <SelectValue placeholder={isZh ? "选择协议" : "Select protocol"} />
                        </SelectTrigger>
                        <SelectContent>
                          {editingProtocolOptions.map((option) => (
                            <SelectItem key={option.value} value={option.value}>
                              {option.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    ) : null}
                    {editingResolvedAuthMode !== "oauth" ? (
                    <div className="space-y-2">
                      <FieldLabel>Base URL</FieldLabel>
                      <Input
                        placeholder={isZh ? "输入上游基础地址" : "Enter upstream base URL"}
                        value={editForm.base_url ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, base_url: e.target.value })}
                      />
                    </div>
                    ) : null}
                    {editingResolvedAuthMode !== "oauth" ? (
                    <div className="space-y-2">
                      <FieldLabel
                        info={
                          isZh
                            ? "用于创建路由时自动获取可用模型列表"
                            : "Used to auto-fetch available model list when creating routes"
                        }
                      >
                        {isZh ? "模型发现源" : "Model Discovery Source"}
                      </FieldLabel>
                      <Input
                        placeholder={isZh ? "可选，支持 https:// 或 ai://models.dev/..." : "Optional, supports https:// or ai://models.dev/..."}
                        value={editForm.models_source ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, models_source: e.target.value })}
                      />
                    </div>
                    ) : null}
                    {isGlobalProxyEnabled && editingResolvedAuthMode !== "oauth" && (
                      <div className="space-y-2">
                        <FieldLabel>{isZh ? "使用本地代理" : "Use Local Proxy"}</FieldLabel>
                        <div className="flex items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
                          <span className="text-xs text-slate-600">
                            {isZh ? "开启后走设置页中的本地代理地址" : "Route requests via local proxy from settings"}
                          </span>
                          <Switch
                            checked={Boolean(editForm.use_proxy)}
                            onCheckedChange={(checked) => setEditForm({ ...editForm, use_proxy: checked })}
                          />
                        </div>
                      </div>
                    )}
                  </div>
                  <div className="flex gap-3">
                    <Button
                      onClick={() => {
                        setEditError(null);
                        const protocol = editForm.protocol || "openai-compat";
                        const baseUrl = toGatewayBaseUrl(editForm.base_url ?? "");
                        const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                        if (validation) {
                          setEditError(validation);
                          return;
                        }
                        const input: UpdateProvider = {
                          name: editForm.name || undefined,
                          vendor: editForm.vendor || undefined,
                          protocol,
                          base_url: baseUrl,
                          use_proxy: Boolean(editForm.use_proxy),
                          preset_key: editForm.preset_key || undefined,
                          channel: editForm.channel || undefined,
                          models_source: editForm.models_source ?? "",
                          static_models: editForm.static_models || undefined,
                          api_key: editForm.api_key || undefined,
                        };
                        updateMut.mutate({ id: editForm.id, ...input });
                      }}
                      disabled={updateMut.isPending || editRequiresNewOAuthProvider || reconnectOAuthMut.isPending || logoutOAuthMut.isPending}
                    >
                      {updateMut.isPending ? (isZh ? "保存中..." : "Saving...") : (isZh ? "保存" : "Save")}
                    </Button>
                    <Button
                      onClick={() => { setEditingId(null); setEditError(null); }}
                      variant="secondary"
                    >
                      {isZh ? "取消" : "Cancel"}
                    </Button>
                  </div>
                  {editError && (
                    <p className="text-xs text-red-600 bg-red-50 rounded-lg px-3 py-2">{editError}</p>
                  )}
                </div>
              );
            }

            return (
              <div key={p.id} className="glass rounded-2xl p-4">
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-3">
                    <div className="flex h-9 w-9 items-center justify-center rounded-xl bg-slate-100">
                      <ProviderIcon
                        name={p.name}
                        protocol={p.protocol}
                        baseUrl={p.base_url}
                        size={30}
                        className="provider-preset-icon provider-preset-icon-colored rounded-xl border border-slate-300/70 bg-transparent"
                      />
                      <ProviderIcon
                        name={p.name}
                        protocol={p.protocol}
                        baseUrl={p.base_url}
                        size={30}
                        monochrome
                        className="provider-preset-icon provider-preset-icon-mono rounded-xl border border-slate-300/70 bg-transparent"
                      />
                    </div>
                    <div>
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="inline-flex h-5 items-center font-semibold text-slate-900">{p.name}</span>
                        <code className="inline-flex h-5 items-center rounded bg-slate-100 px-2 py-0.5 text-[10px] leading-none font-medium text-slate-600">
                          {selectedProviderName}
                        </code>
                        {protocolLabels.map((protocol) => (
                          <Badge
                            key={`${p.id}-${protocol}`}
                            variant={
                              protocol === "anthropic-msgs"
                                ? "warning"
                                : protocol === "google-genai"
                                  ? "secondary"
                                  : "success"
                            }
                            className={`connect-label-badge ${protocol === "google-genai" ? "bg-violet-50 text-violet-700" : ""}`}
                          >
                            {PROTOCOL_TABLE.find((pt) => pt.id === protocol)?.displayName ?? protocol}
                          </Badge>
                        ))}
                        {isGlobalProxyEnabled && p.use_proxy && (
                          <Badge variant="success" className="connect-label-badge">
                            {isZh ? "本地代理" : "Proxy"}
                          </Badge>
                        )}
                        {normalizeAuthMode(p.auth_mode) === "oauth" && (
                          <Badge variant="secondary" className="connect-label-badge bg-sky-50 text-sky-700">
                            OAuth
                          </Badge>
                        )}
                        {!p.is_enabled && (
                          <Badge variant="danger" className="connect-label-badge">
                            {isZh ? "已禁用" : "Disabled"}
                          </Badge>
                        )}
                        {status === "success" ? (
                          <CheckCircle
                            className="h-3.5 w-3.5 text-green-500"
                            aria-label={isZh ? "测试成功" : "Test passed"}
                          />
                        ) : status === "failed" ? (
                          <XCircle
                            className="h-3.5 w-3.5 text-red-400"
                            aria-label={isZh ? "测试失败" : "Test failed"}
                          />
                        ) : null}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-0.5">
                    <button
                      onClick={() => {
                        if (p.is_enabled) {
                          setProviderToDisable(p);
                        } else {
                          toggleEnabledMut.mutate({ id: p.id, is_enabled: true });
                        }
                      }}
                      title={p.is_enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600 cursor-pointer"
                    >
                      {p.is_enabled ? (
                        <ToggleRight className="h-4 w-4 text-green-500" />
                      ) : (
                        <ToggleLeft className="h-4 w-4 text-slate-400" />
                      )}
                    </button>
                    <button
                      onClick={() => handleTest(p)}
                      disabled={Boolean(testingId)}
                      title={isZh ? "测试" : "Test"}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-amber-50 hover:text-amber-500 cursor-pointer disabled:opacity-50"
                    >
                      {testingId === p.id ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <Zap className="h-3.5 w-3.5" />
                      )}
                    </button>
                    <button
                      onClick={() => startEdit(p)}
                      title={isZh ? "编辑" : "Edit"}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-blue-50 hover:text-blue-500 cursor-pointer"
                    >
                      <Pencil className="h-4 w-4" />
                    </button>
                    <button
                      onClick={() => setProviderToCopy(p)}
                      disabled={copyMut.isPending}
                      title={isZh ? "复制" : "Copy"}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-emerald-50 hover:text-emerald-500 cursor-pointer disabled:opacity-50"
                    >
                      {copyMut.isPending && providerToCopy?.id === p.id ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Copy className="h-4 w-4" />
                      )}
                    </button>
                    <button
                      onClick={() => setProviderToDelete(p)}
                      title={isZh ? "删除" : "Delete"}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-red-50 hover:text-red-500 cursor-pointer"
                    >
                      <Trash2 className="h-4 w-4" />
                    </button>
                  </div>
                </div>
              </div>
            );
          })}

          {providers.length > PAGE_SIZE && (
            <div className="flex items-center justify-between px-1 pt-1">
              <span className="text-xs text-slate-500">
                {isZh ? `第 ${page + 1} / ${totalPages} 页` : `Page ${page + 1} of ${totalPages}`}
              </span>
              <div className="flex gap-1">
                <Button
                  onClick={() => setPage(Math.max(0, page - 1))}
                  disabled={page === 0}
                  variant="outline"
                  size="icon"
                >
                  <ChevronLeft className="h-4 w-4" />
                </Button>
                <Button
                  onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                  disabled={page >= totalPages - 1}
                  variant="outline"
                  size="icon"
                >
                  <ChevronRight className="h-4 w-4" />
                </Button>
              </div>
            </div>
          )}
        </div>
      )}

      <Dialog
        open={testDialogOpen}
        onOpenChange={(open) => {
          if (!open) {
            closeTestDialog();
          } else {
            setTestDialogOpen(true);
          }
        }}
      >
        <DialogContent className="w-[min(92vw,720px)]">
          <DialogHeader>
            <DialogTitle>
              {isZh ? `测试 ${testTarget?.name ?? ""}` : `Test ${testTarget?.name ?? ""}`}
            </DialogTitle>
            <DialogDescription>
              {isZh ? "实时展示 Provider 测试日志" : "Real-time logs for provider testing"}
            </DialogDescription>
          </DialogHeader>
          <div
            ref={logsContainerRef}
            className="h-64 overflow-y-auto rounded-lg border border-emerald-500/30 bg-[#050c1f] p-3 font-mono text-sm text-emerald-300 shadow-inner shadow-black/40"
          >
            {testLogs.length === 0 ? (
              <p className="text-xs text-emerald-400/80">{isZh ? "等待测试开始..." : "Waiting for test to start..."}</p>
            ) : (
              testLogs.map((log, idx) => (
                <p
                  key={`${log.timestamp}-${idx}`}
                  className={
                    log.level === "error"
                      ? "text-red-300"
                      : log.level === "success"
                        ? "text-emerald-300"
                        : "text-emerald-200"
                  }
                >
                  [{log.timestamp}] {log.message}
                </p>
              ))
            )}
          </div>
          <DialogFooter>
            <Button variant="secondary" onClick={closeTestDialog}>
              {isTestRunning
                ? (isZh ? "取消" : "Cancel")
                : (isZh ? "关闭" : "Close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={Boolean(providerToDisable)}
        onOpenChange={(open) => {
          if (!open) setProviderToDisable(null);
        }}
        title={isZh ? "确认禁用供应商" : "Confirm provider disable"}
        description={isZh ? "禁用后，引用该供应商的路由请求将受影响，确认禁用？" : "After disabling, route requests referencing this provider will be affected. Confirm disable?"}
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "禁用" : "Disable"}
        onConfirm={() => {
          if (!providerToDisable) return;
          toggleEnabledMut.mutate({ id: providerToDisable.id, is_enabled: false });
          setProviderToDisable(null);
        }}
      />
      <ConfirmDialog
        open={Boolean(providerToCopy)}
        onOpenChange={(open) => {
          if (!open && !copyMut.isPending) setProviderToCopy(null);
        }}
        title={isZh ? "确认复制提供商" : "Confirm provider copy"}
        description={
          providerToCopy
            ? (isZh
              ? `确认复制「${providerToCopy.name}」吗？新提供商名称将为「${nextProviderCopyName(providers, providerToCopy.name)}」。`
              : `Copy "${providerToCopy.name}"? The new provider name will be "${nextProviderCopyName(providers, providerToCopy.name)}".`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={copyMut.isPending ? (isZh ? "复制中..." : "Copying...") : (isZh ? "确认复制" : "Copy")}
        confirmClassName="bg-slate-900 text-white hover:bg-slate-800"
        onConfirm={() => {
          if (!providerToCopy || copyMut.isPending) return;
          copyMut.mutate(providerToCopy.id, {
            onSuccess: () => setProviderToCopy(null),
          });
        }}
      />
      <ConfirmDialog
        open={Boolean(providerToDelete)}
        onOpenChange={(open) => {
          if (!open) setProviderToDelete(null);
        }}
        title={isZh ? "确认删除提供商" : "Confirm provider deletion"}
        description={
          providerToDelete
            ? (isZh
              ? `此操作不可撤销。确认删除「${providerToDelete.name}」吗？`
              : `This action cannot be undone. Delete "${providerToDelete.name}"?`)
            : undefined
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "删除" : "Delete"}
        onConfirm={() => {
          if (!providerToDelete) return;
          deleteMut.mutate(providerToDelete.id);
          setProviderToDelete(null);
        }}
      />
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
