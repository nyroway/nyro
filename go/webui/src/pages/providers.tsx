import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  Provider,
  CreateProvider,
  UpdateProvider,
  TestResult,
  ProviderPreset,
  ProviderChannelPreset,
  ProviderCredentialField,
  ProviderProtocol,
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
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { NyroIcon } from "@/components/ui/nyro-icon";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { Checkbox } from "@/components/ui/checkbox";
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

function protocolUrl(protocol: string) {
  return PROTOCOL_TABLE.find((p) => p.id === resolveProtocol(protocol))?.defaultBaseUrl
    ?? "https://api.openai.com/v1";
}

const emptyCreate: CreateProvider = {
  name: "",
  vendor: undefined,
  protocol: "openai-compatible",
  base_url: "https://api.openai.com/v1",
  use_proxy: false,
  auth_mode: "apikey",
  preset_key: "",
  channel: "",
  models_source: "",
  static_models: "",
  api_key: "",
  credentials: {},
};
const PAGE_SIZE = 7;
const DEFAULT_PRESET_ID = "nyro";
// Only protocols with a registered codec are user-selectable here.
// gemini-interactions/bedrock-converse/azure-inference are declared in
// ProviderProtocol (and on the backend, go/internal/protocol/ids) but have
// no codec yet — offering them would let a user pick a protocol that fails
// at request time.
const protocolOptions = [
  { label: "Anthropic Messages API", value: "anthropic-messages" },
  { label: "OpenAI Compatible API", value: "openai-compatible" },
  { label: "OpenAI Responses API", value: "openai-responses" },
  { label: "Gemini Content API", value: "gemini-content" },
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
  const known = new Set<ProviderProtocol>(protocolOptions.map((item) => item.value));
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
  const canonicalDefault = (resolveProtocol(preset.defaultProtocol) ?? "openai-compatible") as ProviderProtocol;
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

  if (protocol === "openai-compatible" || protocol === "openai-responses" || protocol === "anthropic-messages") {
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

  if (protocol === "gemini-content") {
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
  return protocol === "openai-compatible" ? `${base}/endpoints/openapi` : base;
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
    defaultProtocol: "openai-compatible",
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

// The single-field fallback used for presets whose `credentials.fields[]` is
// empty or absent (should not normally happen for real backend presets, but
// covers the offline fallbackProviderPreset() and any future preset with no
// declared credential schema).
const DEFAULT_CREDENTIAL_FIELDS: ProviderCredentialField[] = [
  { name: "api_key", type: "secret", required: true },
];

function credentialFieldsForPreset(preset?: ProviderPreset | null): ProviderCredentialField[] {
  return preset?.credentialFields?.length ? preset.credentialFields : DEFAULT_CREDENTIAL_FIELDS;
}

// isCredentialFieldRequired resolves a field's `required`/`required_when`
// gate against the currently entered credential values. `required_when`
// values may be a single string or a list of acceptable strings (see e.g.
// azurefoundry.go's client_id field, required when credential_source is
// either "client_secret" or "managed_identity").
function isCredentialFieldRequired(field: ProviderCredentialField, values: Record<string, string>): boolean {
  if (field.required) return true;
  if (!field.required_when) return false;
  return Object.entries(field.required_when).every(([key, expected]) => {
    const actual = values[key] ?? "";
    return Array.isArray(expected) ? expected.includes(actual) : actual === expected;
  });
}

function missingRequiredCredentials(fields: ProviderCredentialField[], values: Record<string, string>): boolean {
  return fields.some((field) => isCredentialFieldRequired(field, values) && !(values[field.name] ?? "").trim());
}

// mergeCredentialValues carries over already-typed credential values when the
// user switches presets mid-edit/mid-create: a field name that exists in both
// the old and new preset keeps its typed value, while a field new to this
// preset falls back to its declared default.
function mergeCredentialValues(
  fields: ProviderCredentialField[],
  prevValues: Record<string, string>,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const field of fields) {
    const prevValue = prevValues[field.name];
    if (prevValue) {
      out[field.name] = prevValue;
    } else if (field.default) {
      out[field.name] = field.default;
    }
  }
  return out;
}

function credentialFieldLabel(field: ProviderCredentialField): string {
  return field.name
    .split("_")
    .map((part) => (part ? part.charAt(0).toUpperCase() + part.slice(1) : part))
    .join(" ");
}

// CredentialFieldInput renders one input for a provider credential field,
// keyed by the Go backend's field `type` ("string" | "secret" | "enum").
// Secret fields whose name looks like a JSON blob (e.g. gcp-vertex's
// `service_account_json`) get a multi-line textarea instead of a single-line
// password input, since pasting a service-account JSON document into a
// one-line field is unusable. Each instance owns its own show/hide toggle so
// the parent form doesn't need one boolean per field.
function CredentialFieldInput({
  field,
  value,
  onChange,
  isZh,
}: {
  field: ProviderCredentialField;
  value: string;
  onChange: (value: string) => void;
  isZh: boolean;
}) {
  const [reveal, setReveal] = useState(false);
  const label = credentialFieldLabel(field);
  const isSecret = field.type === "secret";
  const isJsonBlob = isSecret && /json/i.test(field.name);

  if (field.type === "enum" && field.values?.length) {
    return (
      <div className="space-y-2">
        <FieldLabel>{label}</FieldLabel>
        <Select value={value || field.default || field.values[0]} onValueChange={onChange}>
          <SelectTrigger>
            <SelectValue placeholder={label} />
          </SelectTrigger>
          <SelectContent>
            {field.values.map((option) => (
              <SelectItem key={option} value={option}>
                {option}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    );
  }

  if (isJsonBlob) {
    return (
      <div className="col-span-2 space-y-2">
        <FieldLabel>{label}</FieldLabel>
        <textarea
          placeholder={isZh ? "粘贴 JSON 内容" : "Paste JSON content"}
          value={value}
          rows={8}
          className="min-h-32 w-full resize-y rounded-md border border-border bg-background px-3 py-2 font-mono text-xs text-foreground outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
    );
  }

  return (
    <div className="space-y-2">
      <FieldLabel>{label}</FieldLabel>
      {isSecret ? (
        <div className="relative">
          <Input
            placeholder={field.name === "api_key" ? "sk-..." : label}
            type={reveal ? "text" : "password"}
            value={value}
            className="pr-10"
            onChange={(e) => onChange(e.target.value)}
          />
          <button
            type="button"
            onClick={() => setReveal((prev) => !prev)}
            className="absolute top-1/2 right-3 -translate-y-1/2 text-slate-400 hover:text-slate-600 cursor-pointer"
            aria-label={reveal ? (isZh ? "隐藏" : "Hide") : (isZh ? "显示" : "Show")}
          >
            {reveal ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
      ) : (
        <Input value={value} placeholder={label} onChange={(e) => onChange(e.target.value)} />
      )}
    </div>
  );
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
  const [appendTargets, setAppendTargets] = useState(false);
  const [selectedPresetId, setSelectedPresetId] = useState(DEFAULT_PRESET_ID);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);
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
    credentials: {},
    auth_mode: "apikey",
  });
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

  const [editError, setEditError] = useState<string | null>(null);

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
    mutationFn: ({ id, appendTargets }: { id: string; appendTargets: boolean }) =>
      backend<Provider>("copy_provider", { id, options: { append_targets: appendTargets } }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      qc.invalidateQueries({ queryKey: ["routes"] });
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
      const protocol = (resolveProtocol(provider.protocol || "openai") ?? "openai-compatible") as ProviderProtocol;
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

      finish(
        {
          success: true,
          latency_ms: connectivity.latency_ms,
          model: undefined,
          error: undefined,
        },
        isZh ? "✓ 连通性测试完成" : "✓ Connectivity test completed",
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
    const presetForEdit = providerPresets.find(
      (item) => item.id === (p.preset_key || DEFAULT_PRESET_ID),
    );
    const channel = p.channel || "default";
    const savedProtocol = (resolveProtocol(p.protocol) ?? "openai-compatible") as ProviderProtocol;
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
      credentials: p.credentials ?? {},
      auth_mode: normalizeAuthMode(p.auth_mode),
    });
  }

  function handlePresetChange(nextPresetId: string) {
    if (!nextPresetId) return;
    setSelectedPresetId(nextPresetId);
    const preset = providerPresets.find((item) => item.id === nextPresetId);
    if (!preset) return;

    const nextChannelId = preset.channels?.[0]?.id ?? "";
    const nextProtocol = resolvePresetProtocol(preset, nextChannelId, (resolveProtocol(preset.defaultProtocol) ?? "openai-compatible") as ProviderProtocol);
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
      credentials: mergeCredentialValues(credentialFieldsForPreset(preset), form.credentials ?? {}),
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
              (prev.protocol as ProviderProtocol) || (resolveProtocol(preset.defaultProtocol) ?? "openai-compatible") as ProviderProtocol,
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
              credentials: mergeCredentialValues(credentialFieldsForPreset(preset), prev.credentials ?? {}),
            };
          })()
        : prev,
    );
  }

  function closeCreateForm() {
    setShowForm(false);
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
  const createUsesVertexServiceAccount = isVertexProviderSelection(form);
  const createCredentialFields = credentialFieldsForPreset(selectedPreset);
  const createPresetBaseUrl = selectedPreset
    ? resolvePresetConfig(selectedPreset, (form.protocol as ProviderProtocol) || "openai-compatible", createChannelValue).baseUrl
    : "";
  const createBaseUrlMissing = !createPresetBaseUrl && !form.base_url?.trim();

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
              <div className="space-y-2">
                <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                <Input
                  placeholder={isZh ? "例如 OpenAI 生产" : "e.g. OpenAI Production"}
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </div>
              {createCredentialFields.map((field) => (
                <CredentialFieldInput
                  key={field.name}
                  field={field}
                  value={form.credentials?.[field.name] ?? ""}
                  onChange={(value) =>
                    setForm((prev) => ({
                      ...prev,
                      credentials: { ...(prev.credentials ?? {}), [field.name]: value },
                    }))
                  }
                  isZh={isZh}
                />
              ))}
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
              <div className="space-y-2">
                <FieldLabel>Base URL</FieldLabel>
                <Input
                  placeholder={isZh ? "输入上游基础地址" : "Enter upstream base URL"}
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <FieldLabel
                  info={
                    isZh
                      ? "用于创建模型时自动获取可用模型列表"
                      : "Used to auto-fetch available model list when creating models"
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
              {isGlobalProxyEnabled && (
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
                    const protocol = form.protocol || "openai-compatible";
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
                    createMut.mutate(input);
                  }}
                  disabled={
                    createMut.isPending
                    || !form.name.trim()
                    || missingRequiredCredentials(createCredentialFields, form.credentials ?? {})
                    || createBaseUrlMissing
                  }
                >
                  {createMut.isPending
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
            const protocolLabels = [(resolveProtocol(p.protocol || "openai") ?? "openai-compatible") as ProviderProtocol];
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
              const editUsesVertexServiceAccount = isVertexProviderSelection(editForm);
              const editCredentialFields = credentialFieldsForPreset(editingPreset);
              const editPresetBaseUrl = editingPreset
                ? resolvePresetConfig(editingPreset, (editForm.protocol as ProviderProtocol) || "openai-compatible", editingChannelValue).baseUrl
                : "";
              const editBaseUrlMissing = !editPresetBaseUrl && !editForm.base_url?.trim();
              const currentProviderIsOAuth =
                normalizeAuthMode(p.auth_mode) === "oauth"
                || normalizeAuthMode(editForm.auth_mode) === "oauth";
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
                            (editForm.protocol as ProviderProtocol) || (resolveProtocol(editingPreset.defaultProtocol) ?? "openai-compatible") as ProviderProtocol,
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
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "名称" : "Name"}</FieldLabel>
                      <Input
                        placeholder={isZh ? "提供商名称" : "Provider name"}
                        value={editForm.name ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, name: e.target.value })}
                      />
                    </div>
                    {editCredentialFields.map((field) => (
                      <CredentialFieldInput
                        key={field.name}
                        field={field}
                        value={editForm.credentials?.[field.name] ?? ""}
                        onChange={(value) =>
                          setEditForm((prev) => ({
                            ...prev,
                            credentials: { ...(prev.credentials ?? {}), [field.name]: value },
                          }))
                        }
                        isZh={isZh}
                      />
                    ))}
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
                    <div className="space-y-2">
                      <FieldLabel>Base URL</FieldLabel>
                      <Input
                        placeholder={isZh ? "输入上游基础地址" : "Enter upstream base URL"}
                        value={editForm.base_url ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, base_url: e.target.value })}
                      />
                    </div>
                    <div className="space-y-2">
                      <FieldLabel
                        info={
                          isZh
                            ? "用于创建模型时自动获取可用模型列表"
                            : "Used to auto-fetch available model list when creating models"
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
                    {isGlobalProxyEnabled && (
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
                        const protocol = editForm.protocol || "openai-compatible";
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
                          credentials: editForm.credentials && Object.keys(editForm.credentials).length
                            ? editForm.credentials
                            : undefined,
                        };
                        updateMut.mutate({ id: editForm.id, ...input });
                      }}
                      disabled={
                        updateMut.isPending
                        || missingRequiredCredentials(editCredentialFields, editForm.credentials ?? {})
                        || editBaseUrlMissing
                      }
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
                              protocol === "anthropic-messages"
                                ? "warning"
                                : protocol === "gemini-content"
                                  ? "secondary"
                                  : "success"
                            }
                            className={`connect-label-badge ${protocol === "gemini-content" ? "bg-violet-50 text-violet-700" : ""}`}
                          >
                            {PROTOCOL_TABLE.find((pt) => pt.id === protocol)?.fullName ?? protocol}
                          </Badge>
                        ))}
                        {isGlobalProxyEnabled && p.use_proxy && (
                          <Badge variant="success" className="connect-label-badge">
                            {isZh ? "本地代理" : "Proxy"}
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
        description={isZh ? "禁用后，引用该供应商的模型请求将受影响，确认禁用？" : "After disabling, model requests referencing this provider will be affected. Confirm disable?"}
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
          if (!open && !copyMut.isPending) {
            setProviderToCopy(null);
            setAppendTargets(false);
          }
        }}
        title={isZh ? "确认复制提供商" : "Confirm provider copy"}
        description={
          providerToCopy
            ? (isZh
              ? `复制「${providerToCopy.name}」为「${nextProviderCopyName(providers, providerToCopy.name)}」，新提供商默认禁用。`
              : `Copy "${providerToCopy.name}" as "${nextProviderCopyName(providers, providerToCopy.name)}" (disabled by default).`)
            : undefined
        }
        content={
          <label className="flex cursor-pointer items-start gap-3 rounded-lg border border-slate-200 bg-slate-50 p-3 text-sm text-slate-700">
            <Checkbox
              checked={appendTargets}
              onCheckedChange={(checked) => setAppendTargets(checked === true)}
              disabled={copyMut.isPending}
              aria-label={isZh ? "追加模型目标" : "Append model targets"}
              className="mt-0.5"
            />
            <span className="space-y-1">
              <span className="block font-medium text-slate-800">
                {isZh ? "追加模型目标" : "Append model targets"}
              </span>
              <span className="block text-xs text-slate-500">
                {isZh
                  ? "在引用该提供商的现有模型中追加指向新提供商的目标。"
                  : "Append targets pointing to the new provider in existing models that reference this provider."}
              </span>
            </span>
          </label>
        }
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={copyMut.isPending ? (isZh ? "复制中..." : "Copying...") : (isZh ? "确认复制" : "Copy")}
        confirmClassName="bg-slate-900 text-white hover:bg-slate-800"
        onConfirm={() => {
          if (!providerToCopy || copyMut.isPending) return;
          copyMut.mutate({ id: providerToCopy.id, appendTargets }, {
            onSuccess: () => {
              setProviderToCopy(null);
              setAppendTargets(false);
            },
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
