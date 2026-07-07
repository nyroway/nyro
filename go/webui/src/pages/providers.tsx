import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { backend, streamProviderDraftHealth, streamProviderEditDraftHealth, streamProviderHealth, streamProviderRouteImport } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  Upstream,
  CreateUpstream,
  UpdateUpstream,
  ProviderPresetDTO,
  TestResult,
  ProviderHealthEvent,
  RouteImportEvent,
  RouteImportPreview,
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
  Route as RouteIcon,
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
import { resolveProtocol, PROTOCOL_TABLE, protocolDisplayName } from "@/lib/protocol";
import {
  isCustomProviderPreset,
  withCustomProviderPreset,
} from "@/lib/provider-presets";

function protocolUrl(protocol: string) {
  return PROTOCOL_TABLE.find((p) => p.id === resolveProtocol(protocol))?.defaultBaseUrl
    ?? "https://api.openai.com/v1";
}

// ---------------------------------------------------------------------------
// UI-local form-state shapes and backend DTO <-> UI conversion helpers.
//
// The Go backend's `Upstream`/`CreateUpstream`/`UpdateUpstream` (see
// lib/types.ts) treat `credentials` as an opaque JSON blob and `models` as a
// `string[]`. This page's create/edit forms instead work with a flattened,
// page-local shape (`api_key: string`, `credentials: Record<string,string>`,
// `models` as a newline-joined textarea string, and a derived `is_enabled`
// boolean) — that flattening is purely a display/editing concern of this
// page, not a network DTO, so it's kept local here rather than exported.
// These helpers convert across that boundary in both directions: reading a
// backend `Upstream` to populate the edit form, and serializing this page's
// form state into `CreateUpstream`/`UpdateUpstream` before submitting.
type ProviderFormState = {
  name: string;
  provider: string;
  protocol: string;
  base_url: string;
  proxy_url?: string;
  models_url?: string;
  models?: string;
  api_key: string;
  credentials?: Record<string, string>;
};

type ProviderFormUpdate = {
  name?: string;
  provider?: string;
  protocol?: string;
  base_url?: string;
  proxy_url?: string;
  models_url?: string;
  models?: string;
  api_key?: string;
  credentials?: Record<string, string>;
  is_enabled?: boolean;
};

function parseJSONRecord(value: unknown): Record<string, unknown> {
  if (!value) return {};
  if (typeof value === "object") return value as Record<string, unknown>;
  if (typeof value !== "string") return {};
  try {
    const parsed = JSON.parse(value);
    return parsed && typeof parsed === "object" ? (parsed as Record<string, unknown>) : {};
  } catch {
    return {};
  }
}

function apiKeyFromCredentials(value: unknown): string {
  const raw = parseJSONRecord(value).api_key;
  return typeof raw === "string" ? raw : "";
}

// credentialsRecord flattens an upstream's opaque credentials JSON blob into a
// string-keyed record for editing in the WebUI's dynamic credential-field
// form. Non-string values (should not normally occur) are stringified rather
// than dropped, so round-tripping through the form never silently loses data.
function credentialsRecord(value: unknown): Record<string, string> {
  const parsed = parseJSONRecord(value);
  const out: Record<string, string> = {};
  for (const [key, raw] of Object.entries(parsed)) {
    if (typeof raw === "string") out[key] = raw;
    else if (raw != null) out[key] = String(raw);
  }
  return out;
}

function modelsArrayFromText(text?: string): string[] | undefined {
  if (!text) return undefined;
  const lines = text.split("\n").map((line) => line.trim()).filter(Boolean);
  return lines.length ? lines : undefined;
}

// buildCreateUpstreamInput serializes this page's create-form state into the
// `CreateUpstream` body sent to `POST /api/v1/upstreams`.
function buildCreateUpstreamInput(input: ProviderFormState): CreateUpstream {
  const credentials =
    input.credentials && Object.keys(input.credentials).length > 0
      ? input.credentials
      : { api_key: input.api_key };
  return {
    name: input.name,
    provider: input.provider || "custom",
    protocol: input.protocol,
    base_url: input.base_url,
    credentials,
    models: modelsArrayFromText(input.models ?? undefined),
    models_url: input.models_url || undefined,
    proxy_url: input.proxy_url?.trim() ?? "",
    enabled: true,
  };
}

// buildUpdateUpstreamInput serializes this page's edit-form state into the
// `UpdateUpstream` body sent to `PUT /api/v1/upstreams/{id}`. Only fields
// explicitly present on `input` are included, so unrelated fields are left
// unchanged server-side.
function buildUpdateUpstreamInput(input: ProviderFormUpdate): UpdateUpstream {
  const out: UpdateUpstream = {};
  if (input.name !== undefined) out.name = input.name;
  if (input.provider !== undefined) out.provider = input.provider ?? undefined;
  if (input.protocol !== undefined) out.protocol = input.protocol;
  if (input.base_url !== undefined) out.base_url = input.base_url;
  if (input.credentials !== undefined) {
    out.credentials = input.credentials;
  } else if (input.api_key !== undefined) {
    out.credentials = { api_key: input.api_key };
  }
  if (input.proxy_url !== undefined) out.proxy_url = input.proxy_url.trim();
  if (input.is_enabled !== undefined) out.enabled = input.is_enabled;
  if (input.models !== undefined) out.models = modelsArrayFromText(input.models ?? undefined) ?? [];
  if (input.models_url !== undefined) out.models_url = input.models_url ?? "";
  return out;
}

// providerPresetFromDTO adapts the Go backend's raw provider preset shape
// (`ProviderPresetDTO`: snake_case, `protocols: Array<{id, base_url}>`,
// `credentials.fields[]`) into the UI-facing `ProviderPreset` shape used by
// the rest of this page (camelCase, `channels: ProviderChannelPreset[]`,
// `credentialFields`). Presets no longer carry a static model list from the
// backend — only an optional default discovery URL — so `staticModels` is
// intentionally left unset on the synthesized channel.
function providerPresetFromDTO(preset: ProviderPresetDTO): ProviderPreset {
  const channels: ProviderChannelPreset[] = preset.protocols.map((protocol) => ({
    id: protocol.id,
    baseUrls: { [protocol.id]: protocol.base_url ?? "" },
    modelsSource: preset.models_url,
    modelsEndpoint: preset.models_url,
  }));
  return {
    id: preset.id,
    name: preset.name,
    icon: preset.id,
    priority: preset.priority,
    defaultProtocol: preset.default_protocol,
    channels,
    credentialFields: preset.credentials?.fields ?? [],
  };
}

const emptyCreate: ProviderFormState = {
  name: "",
  provider: "custom",
  protocol: "openai-chat",
  base_url: "https://api.openai.com/v1",
  proxy_url: "",
  models_url: "",
  models: "",
  api_key: "",
  credentials: {},
};
const PAGE_SIZE = 7;
const protocolOptions = [
  { label: "Anthropic Messages API", value: "anthropic-messages" },
  { label: "OpenAI Compatible API", value: "openai-chat" },
  { label: "OpenAI Responses API", value: "openai-responses" },
  { label: "Google Gemini API", value: "google-gemini" },
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

function availableProtocolsForPreset(preset?: ProviderPreset | null): ProviderProtocol[] {
  if (!preset || isCustomProviderPreset(preset.id)) {
    return protocolOptions.map((item) => item.value);
  }

  const collectKeys = (channels: ProviderChannelPreset[]) =>
    channels.flatMap((channel) => Object.keys(channel.baseUrls ?? {}));
  const rawKeys = collectKeys(preset.channels ?? []);

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
  preferred?: ProviderProtocol,
): ProviderProtocol {
  const available = availableProtocolsForPreset(preset);
  const canonicalDefault = (resolveProtocol(preset.defaultProtocol) ?? "openai-chat") as ProviderProtocol;
  if (preferred && available.includes(preferred)) return preferred;
  if (available.includes(canonicalDefault)) return canonicalDefault;
  return available[0] ?? canonicalDefault;
}

function presetLabel(preset: ProviderPreset) {
  return preset.name;
}

function presetLabelClass(preset: ProviderPreset) {
  const len = presetLabel(preset).trim().length;
  if (len >= 16) return "provider-preset-label provider-preset-label-micro";
  if (len >= 12) return "provider-preset-label provider-preset-label-compact";
  return "provider-preset-label";
}

function toGatewayBaseUrl(url: string) {
  const normalized = url.trim().replace(/\/+$/, "");
  return normalized;
}

function joinStaticModels(models?: string[]) {
  return models?.join("\n") ?? "";
}

function fallbackChannelPreset(): ProviderChannelPreset {
  return {
    id: "default",
    baseUrls: {},
  };
}

function presetChannels(preset?: ProviderPreset | null) {
  return preset?.channels?.length ? preset.channels : [fallbackChannelPreset()];
}

function resolvePresetConfig(
  preset: ProviderPreset,
  protocol: ProviderProtocol,
) {
  const channel =
    presetChannels(preset).find((item) =>
      Object.keys(item.baseUrls ?? {}).some((key) => resolveProtocol(key) === protocol),
    ) ?? presetChannels(preset)[0];
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
// empty or absent (including the frontend-only Custom preset and any future
// preset with no declared credential schema).
const DEFAULT_CREDENTIAL_FIELDS: ProviderCredentialField[] = [
  { name: "api_key", type: "secret", required: true },
];

function credentialFieldsForPreset(preset?: ProviderPreset | null): ProviderCredentialField[] {
  return preset?.credentialFields?.length ? preset.credentialFields : DEFAULT_CREDENTIAL_FIELDS;
}

function splitApiKeyCredentialField(fields: ProviderCredentialField[]) {
  const apiKeyField = fields.find((field) => field.name === "api_key") ?? null;
  return {
    apiKeyField,
    otherFields: fields.filter((field) => field.name !== "api_key"),
  };
}

// Model discovery is either a remote URL or a static list — mutually
// exclusive in the UI even though both fields exist independently on the
// wire. When a preset/protocol change fills one of them, switch the segmented
// control to match; if neither is filled, leave the user's current choice as-is.
type ModelsMode = "url" | "static";

function pickModelsMode(current: ModelsMode, modelsSource?: string, staticModels?: string): ModelsMode {
  if (modelsSource && modelsSource.trim()) return "url";
  if (staticModels && staticModels.trim()) return "static";
  return current;
}

// autoGrowTextarea sizes a manual-model-list textarea to exactly fit its
// content (no internal scrollbar, no user-draggable resize handle — see
// the `resize-none` class on the element): height tracks line count only,
// growing as the user adds lines and shrinking as they remove them.
// Resetting to "auto" before reading scrollHeight is required so a shrink
// (fewer lines) is measured correctly, not clamped to the previous height.
function autoGrowTextarea(el: HTMLTextAreaElement | null) {
  if (!el) return;
  el.style.height = "auto";
  el.style.height = `${el.scrollHeight}px`;
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

function defaultCredentialValues(fields: ProviderCredentialField[]): Record<string, string> {
  return mergeCredentialValues(fields, {});
}

const CREDENTIAL_LABEL_ACRONYMS: Record<string, string> = { api: "API", url: "URL", id: "ID" };

function credentialFieldLabel(field: ProviderCredentialField): string {
  return field.name
    .split("_")
    .map((part) => CREDENTIAL_LABEL_ACRONYMS[part.toLowerCase()] ?? (part ? part.charAt(0).toUpperCase() + part.slice(1) : part))
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
  const credentialPlaceholder = field.name === "api_key"
    ? (isZh ? "如：sk-..." : "e.g. sk-...")
    : (isZh ? `请输入 ${label}` : `Enter ${label}`);

  if (field.type === "enum" && field.values?.length) {
    return (
      <div className="space-y-2">
        <FieldLabel required={field.required}>{label}</FieldLabel>
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
        <FieldLabel required={field.required}>{label}</FieldLabel>
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
      <FieldLabel required={field.required}>{label}</FieldLabel>
      {isSecret ? (
        <div className="relative">
          <Input
            placeholder={credentialPlaceholder}
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
        <Input value={value} placeholder={credentialPlaceholder} onChange={(e) => onChange(e.target.value)} />
      )}
    </div>
  );
}

function FieldLabel({
  children,
  info,
  required,
}: {
  children: string;
  info?: string;
  required?: boolean;
}) {
  return (
    <label className="ml-1 inline-flex items-center gap-1 text-xs leading-none font-normal text-slate-900">
      <span>{children}</span>
      {required ? (
        <span className="text-red-500">
          <span aria-hidden="true">*</span>
          <span className="sr-only">required</span>
        </span>
      ) : null}
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
  const [routeImportingId, setRouteImportingId] = useState<string | null>(null);
  const [testResult, setTestResult] = useState<Record<string, TestResult>>(loadProviderTestResults);
  const [testDialogOpen, setTestDialogOpen] = useState(false);
  const [testLogs, setTestLogs] = useState<TestLogEntry[]>([]);
  const [isTestRunning, setIsTestRunning] = useState(false);
  const [testTarget, setTestTarget] = useState<Upstream | null>(null);
  const [testDialogMode, setTestDialogMode] = useState<"provider" | "create" | "edit" | "route_import">("provider");
  const [pendingCreateInput, setPendingCreateInput] = useState<CreateUpstream | null>(null);
  const [createHealthPassed, setCreateHealthPassed] = useState(false);
  const [pendingUpdateInput, setPendingUpdateInput] = useState<(UpdateUpstream & { id: string }) | null>(null);
  const [editHealthPassed, setEditHealthPassed] = useState(false);
  const [providerToDelete, setProviderToDelete] = useState<Upstream | null>(null);
  const [routeImportPreview, setRouteImportPreview] = useState<{ provider: Upstream; preview: RouteImportPreview } | null>(null);
  const [selectedPresetId, setSelectedPresetId] = useState("");
  const [modelsMode, setModelsMode] = useState<ModelsMode>("url");
  const [editModelsMode, setEditModelsMode] = useState<ModelsMode>("url");
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);
  const activeTestRunRef = useRef(0);
  const activeTestAbortRef = useRef<AbortController | null>(null);
  const logsContainerRef = useRef<HTMLDivElement | null>(null);
  const modelsTextareaRef = useRef<HTMLTextAreaElement | null>(null);
  const editModelsTextareaRef = useRef<HTMLTextAreaElement | null>(null);

  const { data: providers = [], isLoading } = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => backend("list_upstreams"),
  });
  const { data: providerPresetsRaw = [] } = useQuery<ProviderPresetDTO[]>({
    queryKey: ["provider-presets"],
    queryFn: () => backend("get_provider_presets"),
  });
  const providerPresets = useMemo(
    () => withCustomProviderPreset(providerPresetsRaw.map(providerPresetFromDTO)),
    [providerPresetsRaw],
  );
  const [form, setForm] = useState<ProviderFormState>(emptyCreate);
  const selectedPreset = useMemo(
    () => providerPresets.find((preset) => preset.id === selectedPresetId) ?? null,
    [providerPresets, selectedPresetId],
  );

  const [editForm, setEditForm] = useState<ProviderFormUpdate & { id: string }>({
    id: "",
    name: "",
    provider: "custom",
    protocol: "",
    base_url: "",
    proxy_url: "",
    models_url: "",
    models: "",
    api_key: "",
    credentials: {},
  });
  const createMut = useMutation({
    mutationFn: (input: CreateUpstream) => backend<Upstream>("create_upstream", { input }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      setPendingCreateInput(null);
      setCreateHealthPassed(false);
      setTestDialogOpen(false);
      closeCreateForm();
    },
    onError: (error: unknown) => {
      showErrorDialog("创建提供商失败", "Failed to create provider", error);
    },
  });

  const [editError, setEditError] = useState<string | null>(null);

  const updateMut = useMutation({
    mutationFn: ({ id, ...input }: UpdateUpstream & { id: string }) =>
      backend("update_upstream", { id, input }),
    onSuccess: () => {
      setEditError(null);
      qc.invalidateQueries({ queryKey: ["providers"] });
      setEditingId(null);
      setPendingUpdateInput(null);
      setEditHealthPassed(false);
      setTestDialogOpen(false);
    },
    onError: (err: Error) => {
      setEditError(String(err));
      showErrorDialog("保存提供商失败", "Failed to save provider", err);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_upstream", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("删除提供商失败", "Failed to delete provider", error);
    },
  });

  const [providerToDisable, setProviderToDisable] = useState<Upstream | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_upstream", { id, input: { enabled: is_enabled } }),
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
    activeTestAbortRef.current?.abort();
    activeTestAbortRef.current = null;
    setIsTestRunning(false);
    setTestingId(null);
    setRouteImportingId(null);
    setTestDialogOpen(false);
    setPendingCreateInput(null);
    setCreateHealthPassed(false);
    setPendingUpdateInput(null);
    setEditHealthPassed(false);
  }

  async function handleTest(provider: Upstream) {
    const runId = activeTestRunRef.current + 1;
    activeTestRunRef.current = runId;
    const abortController = new AbortController();
    activeTestAbortRef.current = abortController;
    const isCanceled = () => activeTestRunRef.current !== runId;

    setTestingId(provider.id);
    setTestTarget(provider);
    setTestDialogMode("provider");
    setTestLogs([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);
    setTestResult((prev) => {
      const next = { ...prev };
      delete next[provider.id];
      return next;
    });

    const finish = (result: TestResult) => {
      if (isCanceled()) return;
      setTestResult((prev) => ({ ...prev, [provider.id]: result }));
      setIsTestRunning(false);
      setTestingId(null);
    };

    let modelResult: TestResult = { success: false, latency_ms: 0 };
    let completed = false;

    try {
      appendTestLog("info", isZh ? `开始测试 ${provider.name}...` : `Start testing ${provider.name}...`);
      appendTestLog("info", isZh ? "会向上游发送一次最小模型请求用于验证模型可用性" : "A minimal upstream model request will be sent to verify model availability.");

      await streamProviderHealth(provider.id, (event) => {
        if (isCanceled()) return;
        appendHealthEvent(event);
        if (event.type === "check" && event.check === "model_request" && (event.status === "passed" || event.status === "failed")) {
          modelResult = {
            success: event.status === "passed",
            latency_ms: event.latency_ms ?? 0,
            model: event.model,
            error: event.status === "failed" ? event.error ?? event.message : undefined,
          };
        }
        if (event.type === "complete") {
          completed = true;
          finish({
            ...modelResult,
            success: event.success === true,
            error: event.success ? undefined : event.error ?? modelResult.error,
          });
        }
      }, abortController.signal);
      if (!completed && !isCanceled() && !abortController.signal.aborted) {
        const message = isZh ? "测试未返回完成事件" : "Health check did not return a completion event";
        appendTestLog("error", `✗ ${message}`);
        finish({ success: false, latency_ms: modelResult.latency_ms, model: modelResult.model, error: message });
      }
    } catch (error: unknown) {
      if (isCanceled() || abortController.signal.aborted) return;
      const message = normalizeErrorMessage(error);
      appendTestLog("error", `${isZh ? "✗ 测试失败" : "✗ Test failed"}: ${message}`);
      finish({ success: false, latency_ms: 0, model: undefined, error: message });
    } finally {
      if (!isCanceled()) {
        activeTestAbortRef.current = null;
      }
    }
  }

  function healthCheckName(check: ProviderHealthEvent["check"]) {
    switch (check) {
      case "config":
        return isZh ? "配置校验" : "Configuration validation";
      case "credentials":
        return isZh ? "凭证校验" : "Credential validation";
      case "models":
        return isZh ? "模型发现" : "Model discovery";
      case "model_request":
        return isZh ? "模型测试" : "Model request test";
      default:
        return isZh ? "健康检查" : "Health check";
    }
  }

  function routeImportStageName(stage: RouteImportEvent["stage"]) {
    switch (stage) {
      case "models":
        return isZh ? "模型发现" : "Model discovery";
      case "creating":
        return isZh ? "路由导入" : "Route import";
      default:
        return isZh ? "导入" : "Import";
    }
  }

  function appendHealthEvent(event: ProviderHealthEvent, mode: "provider" | "create" | "edit" = "provider") {
    if (event.type === "complete") {
      appendTestLog(
        event.success ? "success" : "error",
        event.success
          ? (mode === "create"
            ? (isZh ? "✓ 测试全部通过，点击完成创建" : "✓ All checks passed. Click Create Provider to finish.")
            : mode === "edit"
              ? (isZh ? "✓ 测试全部通过，点击完成保存" : "✓ All checks passed. Click Save Provider to finish.")
              : (isZh ? "✓ 测试全部通过" : "✓ All checks passed."))
          : `${isZh ? "✗ 测试未通过" : "✗ Checks failed"}${event.error ? `: ${event.error}` : ""}`,
      );
      return;
    }

    const name = healthCheckName(event.check);
    if (event.status === "running") {
      appendTestLog("info", `▶ ${name}${event.model ? ` (${event.model})` : ""}`);
      return;
    }
    if (event.status === "passed") {
      const latency = event.latency_ms != null ? ` ${event.latency_ms}ms` : "";
      appendTestLog(
        "success",
        `✓ ${name}${event.model ? ` (${event.model})` : ""}${latency}`,
      );
      return;
    }
    if (event.status === "failed") {
      appendTestLog("error", `✗ ${name}: ${event.error ?? event.message ?? (isZh ? "失败" : "failed")}`);
    }
  }

  function appendRouteImportEvent(event: RouteImportEvent) {
    if (event.type === "complete") {
      const summary = isZh
        ? `发现 ${event.discovered ?? 0} 个，创建 ${event.created ?? 0} 个，跳过 ${event.skipped ?? 0} 个，失败 ${event.failed ?? 0} 个`
        : `Discovered ${event.discovered ?? 0}, created ${event.created ?? 0}, skipped ${event.skipped ?? 0}, failed ${event.failed ?? 0}`;
      appendTestLog(event.success ? "success" : "error", `${event.success ? "✓" : "✗"} ${summary}`);
      return;
    }
    if (event.type === "stage") {
      const name = routeImportStageName(event.stage);
      if (event.status === "running") {
        appendTestLog("info", `▶ ${name}`);
      } else if (event.status === "passed") {
        const count = event.count != null ? ` (${event.count})` : "";
        appendTestLog("success", `✓ ${name}${count}`);
      } else if (event.status === "failed") {
        appendTestLog("error", `✗ ${name}: ${event.error ?? event.message ?? (isZh ? "失败" : "failed")}`);
      }
      return;
    }
    if (event.type === "route") {
      if (event.status === "created") {
        appendTestLog("success", `✓ ${event.model ?? ""}`);
      } else if (event.status === "skipped") {
        appendTestLog("info", `- ${event.model ?? ""} ${isZh ? "已存在，跳过" : "already exists, skipped"}`);
      } else if (event.status === "failed") {
        appendTestLog("error", `✗ ${event.model ?? ""}: ${event.error ?? event.reason ?? (isZh ? "失败" : "failed")}`);
      }
    }
  }

  async function handleImportRoutes(provider: Upstream) {
    const runId = activeTestRunRef.current + 1;
    activeTestRunRef.current = runId;
    const abortController = new AbortController();
    activeTestAbortRef.current = abortController;
    const isCanceled = () => activeTestRunRef.current !== runId;

    setRouteImportingId(provider.id);
    setTestingId(null);
    setTestTarget(provider);
    setTestDialogMode("route_import");
    setPendingCreateInput(null);
    setCreateHealthPassed(false);
    setTestLogs([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", isZh ? `开始导入 ${provider.name} 的模型路由...` : `Start importing routes for ${provider.name}...`);
    appendTestLog("info", isZh ? "已有同名路由会自动跳过，不会修改现有路由。" : "Existing routes with the same name are skipped; existing routes are not modified.");

    try {
      await streamProviderRouteImport(provider.id, (event) => {
        if (isCanceled()) return;
        appendRouteImportEvent(event);
        if (event.type === "complete") {
          setIsTestRunning(false);
          setRouteImportingId(null);
          qc.invalidateQueries({ queryKey: ["routes"] });
        }
      }, abortController.signal);
    } catch (error: unknown) {
      if (isCanceled() || abortController.signal.aborted) return;
      const message = normalizeErrorMessage(error);
      appendTestLog("error", `${isZh ? "✗ 导入失败" : "✗ Import failed"}: ${message}`);
      setIsTestRunning(false);
      setRouteImportingId(null);
    } finally {
      if (!isCanceled()) {
        activeTestAbortRef.current = null;
      }
    }
  }

  async function handlePreviewRouteImport(provider: Upstream) {
    setRouteImportingId(provider.id);
    try {
      const preview = await backend<RouteImportPreview>("preview_provider_route_import", { id: provider.id });
      setRouteImportPreview({ provider, preview });
    } catch (error: unknown) {
      showErrorDialog("预览导入失败", "Failed to preview route import", error);
    } finally {
      setRouteImportingId(null);
    }
  }

  async function handleCreateHealthCheck(input: CreateUpstream) {
    const runId = activeTestRunRef.current + 1;
    activeTestRunRef.current = runId;
    const abortController = new AbortController();
    activeTestAbortRef.current = abortController;
    const isCanceled = () => activeTestRunRef.current !== runId;

    setTestingId(null);
    setTestTarget(null);
    setTestDialogMode("create");
    setPendingCreateInput(input);
    setCreateHealthPassed(false);
    setTestLogs([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", isZh ? `开始创建前测试 ${input.name}...` : `Start pre-create checks for ${input.name}...`);
    appendTestLog("info", isZh ? "会向上游发送一次最小模型请求用于验证模型可用性" : "A minimal upstream model request will be sent to verify model availability.");

    try {
      await streamProviderDraftHealth(input, (event) => {
        if (isCanceled()) return;
        appendHealthEvent(event, "create");
        if (event.type === "complete") {
          setCreateHealthPassed(event.success === true);
          setIsTestRunning(false);
        }
      }, abortController.signal);
    } catch (error: unknown) {
      if (isCanceled() || abortController.signal.aborted) return;
      const message = normalizeErrorMessage(error);
      appendTestLog("error", `${isZh ? "✗ 流式测试失败" : "✗ Streaming health check failed"}: ${message}`);
      setCreateHealthPassed(false);
      setIsTestRunning(false);
    } finally {
      if (!isCanceled()) {
        activeTestAbortRef.current = null;
      }
    }
  }

  async function handleUpdateHealthCheck(draft: CreateUpstream, update: UpdateUpstream & { id: string }) {
    const runId = activeTestRunRef.current + 1;
    activeTestRunRef.current = runId;
    const abortController = new AbortController();
    activeTestAbortRef.current = abortController;
    const isCanceled = () => activeTestRunRef.current !== runId;

    setTestingId(null);
    setTestTarget(null);
    setTestDialogMode("edit");
    setPendingUpdateInput(update);
    setEditHealthPassed(false);
    setTestLogs([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", isZh ? `开始保存前测试 ${draft.name}...` : `Start pre-save checks for ${draft.name}...`);
    appendTestLog("info", isZh ? "会向上游发送一次最小模型请求用于验证模型可用性" : "A minimal upstream model request will be sent to verify model availability.");

    try {
      await streamProviderEditDraftHealth(update.id, draft, (event) => {
        if (isCanceled()) return;
        appendHealthEvent(event, "edit");
        if (event.type === "complete") {
          setEditHealthPassed(event.success === true);
          setIsTestRunning(false);
        }
      }, abortController.signal);
    } catch (error: unknown) {
      if (isCanceled() || abortController.signal.aborted) return;
      const message = normalizeErrorMessage(error);
      appendTestLog("error", `${isZh ? "✗ 流式测试失败" : "✗ Streaming health check failed"}: ${message}`);
      setEditHealthPassed(false);
      setIsTestRunning(false);
    } finally {
      if (!isCanceled()) {
        activeTestAbortRef.current = null;
      }
    }
  }

  function startEdit(p: Upstream) {
    setEditingId(p.id);
    setEditError(null);
    const protocol = (resolveProtocol(p.protocol) ?? "openai-chat") as ProviderProtocol;
    const presetForEdit = p.provider
      ? providerPresets.find((item) => item.id === p.provider) ?? null
      : null;
    const modelsText = joinStaticModels(p.models ?? undefined);
    setEditModelsMode(pickModelsMode("url", p.models_url ?? undefined, modelsText || undefined));
    setEditForm({
      id: p.id,
      name: p.name,
      provider: presetForEdit ? presetForEdit.id : (p.provider ?? "custom"),
      protocol,
      base_url: p.base_url ?? "",
      proxy_url: p.proxy_url ?? "",
      models_url: p.models_url ?? "",
      models: modelsText,
      api_key: apiKeyFromCredentials(p.credentials),
      credentials: credentialsRecord(p.credentials),
    });
  }

  function handleProtocolChange(nextProtocol: string) {
    const protocol = resolveProtocol(nextProtocol) as ProviderProtocol | null;
    if (!protocol) return;
    const preset = selectedPreset
      && !isCustomProviderPreset(selectedPreset.id)
      && availableProtocolsForPreset(selectedPreset).includes(protocol)
      ? selectedPreset
      : null;
    if (!preset && selectedPresetId && !isCustomProviderPreset(selectedPresetId)) setSelectedPresetId("");
    const config = preset ? resolvePresetConfig(preset, protocol) : null;
    if (config) setModelsMode((prev) => pickModelsMode(prev, config.modelsSource, config.staticModels));
    setForm((prev) => ({
      ...prev,
      protocol,
      base_url: config?.baseUrl || protocolUrl(protocol) || prev.base_url,
      models_url: config?.modelsSource ?? prev.models_url,
      models: config?.staticModels ?? prev.models,
      api_key: config?.apiKey || prev.api_key,
      credentials: preset
        ? mergeCredentialValues(credentialFieldsForPreset(preset), prev.credentials ?? {})
        : prev.credentials,
    }));
  }

  function handleTemplateChange(nextPresetId: string) {
    setSelectedPresetId(nextPresetId);
    if (!nextPresetId) return; // "none" — leave current form values as the user typed them.
    const preset = providerPresets.find((item) => item.id === nextPresetId);
    if (!preset) return;
    const protocol = isCustomProviderPreset(preset.id) ? protocolOptions[0].value : resolvePresetProtocol(preset);
    const config = resolvePresetConfig(preset, protocol);
    setModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));
    setForm({
      ...emptyCreate,
      name: isCustomProviderPreset(preset.id) ? "" : preset.name,
      protocol,
      base_url: config.baseUrl || protocolUrl(protocol),
      models_url: config.modelsSource,
      models: config.staticModels,
      api_key: config.apiKey || "",
      provider: isCustomProviderPreset(preset.id) ? "custom" : preset.id,
      credentials: defaultCredentialValues(credentialFieldsForPreset(preset)),
    });
  }

  function handleEditProtocolChange(nextProtocol: string) {
    const protocol = resolveProtocol(nextProtocol) as ProviderProtocol | null;
    if (!protocol) return;
    const currentPreset = editForm.provider && editForm.provider !== "custom"
      ? providerPresets.find((item) => item.id === editForm.provider) ?? null
      : null;
    const preset = currentPreset && availableProtocolsForPreset(currentPreset).includes(protocol)
      ? currentPreset
      : null;
    const config = preset ? resolvePresetConfig(preset, protocol) : null;
    if (config) setEditModelsMode((prevMode) => pickModelsMode(prevMode, config.modelsSource, config.staticModels));
    setEditForm((prev) => ({
      ...prev,
      provider: preset ? prev.provider : "custom",
      protocol,
      base_url: config?.baseUrl || (preset ? "" : protocolUrl(protocol)) || prev.base_url,
      models_url: config?.modelsSource ?? prev.models_url,
      models: config?.staticModels ?? prev.models,
      api_key: config?.apiKey || prev.api_key,
      credentials: preset
        ? mergeCredentialValues(credentialFieldsForPreset(preset), prev.credentials ?? {})
        : prev.credentials,
    }));
  }

  // Always keep a valid quickselect option selected, defaulting to the
  // highest-priority backend preset and falling back to Custom whenever the
  // current selection is empty or no longer valid (e.g. right after opening
  // the create form, or if the preset list changes underneath it).
  useEffect(() => {
    if (providerPresets.some((preset) => preset.id === selectedPresetId)) return;
    const fallback = providerPresets[0];
    if (fallback) handleTemplateChange(fallback.id);
  }, [providerPresets, selectedPresetId]);

  function closeCreateForm() {
    setShowForm(false);
    setSelectedPresetId("");
    setModelsMode("url");
    setForm(emptyCreate);
  }

  const totalPages = Math.max(1, Math.ceil(providers.length / PAGE_SIZE));
  const pagedProviders = providers.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const createCredentialFields = credentialFieldsForPreset(selectedPreset);
  const createCredentialLayout = splitApiKeyCredentialField(createCredentialFields);
  const createPresetBaseUrl = selectedPreset
    ? resolvePresetConfig(selectedPreset, (form.protocol as ProviderProtocol) || "openai-chat").baseUrl
    : "";
  const createBaseUrlMissing = !createPresetBaseUrl && !form.base_url?.trim();
  const createProtocolOptions = availableProtocolsForPreset(selectedPreset);

  useEffect(() => {
    if (page > totalPages - 1) {
      setPage(0);
    }
  }, [page, totalPages]);

  useEffect(() => {
    if (!logsContainerRef.current) return;
    logsContainerRef.current.scrollTop = logsContainerRef.current.scrollHeight;
  }, [testLogs]);

  // Auto-grow the manual model-list textareas to fit their content (see
  // autoGrowTextarea) — re-measured whenever the text changes (typing,
  // preset/protocol fill-in) or the segmented control switches into
  // "static"/manual mode (the edit form's textarea isn't in the DOM at all
  // while in "url"/auto mode, so it needs re-measuring the moment it mounts).
  useLayoutEffect(() => {
    autoGrowTextarea(modelsTextareaRef.current);
  }, [form.models, modelsMode]);

  useLayoutEffect(() => {
    autoGrowTextarea(editModelsTextareaRef.current);
  }, [editForm.models, editModelsMode]);

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
            setSelectedPresetId("");
            setModelsMode("url");
            setForm(emptyCreate);
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
            <ToggleGroup
              type="single"
              value={selectedPresetId}
              onValueChange={(value) => {
                if (value) handleTemplateChange(value);
              }}
              className="provider-preset-group"
            >
              {providerPresets.map((preset) => (
                <ToggleGroupItem
                  key={preset.id}
                  value={preset.id}
                  variant="outline"
                  size="lg"
                  className="provider-preset-card h-auto w-full flex-col gap-3 px-4 py-5"
                  aria-label={presetLabel(preset)}
                >
                  <ProviderIcon
                    iconKey={preset.icon}
                    name={preset.icon ?? preset.name}
                    size={26}
                    className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                  />
                  <ProviderIcon
                    iconKey={preset.icon}
                    name={preset.icon ?? preset.name}
                    size={26}
                    monochrome
                    className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                  />
                  <span className={presetLabelClass(preset)}>{presetLabel(preset)}</span>
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>
          <div className="h-px bg-slate-200/70" />
          <div className="space-y-4">
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
                <Input
                  placeholder={isZh ? "如：OpenAI 生产环境" : "e.g. OpenAI Production"}
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <FieldLabel required>{isZh ? "协议" : "Protocol"}</FieldLabel>
                <Select value={form.protocol} onValueChange={(value) => handleProtocolChange(value)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {createProtocolOptions.map((protocol) => (
                      <SelectItem key={protocol} value={protocol}>
                        {protocolDisplayName(protocol) ?? protocol}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {createCredentialLayout.apiKeyField ? (
                <CredentialFieldInput
                  field={createCredentialLayout.apiKeyField}
                  value={form.credentials?.[createCredentialLayout.apiKeyField.name] ?? ""}
                  onChange={(value) =>
                    setForm((prev) => ({
                      ...prev,
                      credentials: { ...(prev.credentials ?? {}), [createCredentialLayout.apiKeyField!.name]: value },
                    }))
                  }
                  isZh={isZh}
                />
              ) : (
                <div aria-hidden="true" />
              )}
              <div className="space-y-2">
                <FieldLabel required>Base URL</FieldLabel>
                <Input
                  placeholder={isZh ? "如：https://api.openai.com/v1" : "e.g. https://api.openai.com/v1"}
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                />
              </div>
              <div className="space-y-2">
                <FieldLabel
                  required
                  info={
                    isZh
                      ? "用于创建模型时自动获取可用模型列表"
                      : "Used to auto-fetch available model list when creating models"
                  }
                >
                  {isZh ? "模型发现" : "Model Discovery"}
                </FieldLabel>
                {modelsMode === "url" ? (
                  <Input
                    placeholder={isZh ? "如：https://api.openai.com/v1/models" : "e.g. https://api.openai.com/v1/models"}
                    value={form.models_url ?? ""}
                    onChange={(e) => setForm({ ...form, models_url: e.target.value })}
                  />
                ) : (
                  <textarea
                    ref={modelsTextareaRef}
                    rows={1}
                    className="model-textarea nyro-shadcn-input flex min-h-[40px] w-full resize-none overflow-hidden rounded-md border border-border bg-background px-3 text-sm text-foreground transition-[border-color,background-color,color] outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300 disabled:cursor-not-allowed disabled:opacity-50"
                    placeholder={isZh ? "每行一个模型名，如：gpt-4o" : "One model per line, e.g. gpt-4o"}
                    value={form.models ?? ""}
                    onChange={(e) => {
                      setForm({ ...form, models: e.target.value });
                      autoGrowTextarea(e.target);
                    }}
                  />
                )}
                <ToggleGroup
                  type="single"
                  value={modelsMode}
                  onValueChange={(value) => {
                    if (!value) return;
                    const mode = value as ModelsMode;
                    setModelsMode(mode);
                    setForm((prev) => ({
                      ...prev,
                      models_url: mode === "url" ? prev.models_url : "",
                      models: mode === "static" ? prev.models : "",
                    }));
                  }}
                  className="provider-region-group"
                >
                  <ToggleGroupItem value="url" variant="outline" size="sm">
                    {isZh ? "自动发现" : "Auto Discovery"}
                  </ToggleGroupItem>
                  <ToggleGroupItem value="static" variant="outline" size="sm">
                    {isZh ? "手动填写" : "Manual Entry"}
                  </ToggleGroupItem>
                </ToggleGroup>
              </div>
              <div className="space-y-2">
                <FieldLabel>{isZh ? "代理地址" : "Proxy URL"}</FieldLabel>
                <Input
                  placeholder={isZh ? "如：http://127.0.0.1:7890" : "e.g. http://127.0.0.1:7890"}
                  value={form.proxy_url ?? ""}
                  onChange={(e) => setForm({ ...form, proxy_url: e.target.value })}
                />
              </div>
              {createCredentialLayout.otherFields.map((field) => (
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
            </div>
              <div className="flex gap-3">
                <Button
                  onClick={() => {
                    const protocol = form.protocol || "openai-chat";
                    const baseUrl = toGatewayBaseUrl(form.base_url ?? "");
                    const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                    if (validation) {
                      setErrorDialog({
                        title: isZh ? "创建提供商失败" : "Failed to create provider",
                        description: validation,
                      });
                      return;
                    }
                    const input: CreateUpstream = buildCreateUpstreamInput({
                      ...form,
                      protocol,
                      base_url: baseUrl,
                    });
                    void handleCreateHealthCheck(input);
                  }}
                  disabled={
                    createMut.isPending
                    || isTestRunning
                    || !form.name.trim()
                    || missingRequiredCredentials(createCredentialFields, form.credentials ?? {})
                    || createBaseUrlMissing
                  }
                >
                  {isTestRunning
                    ? (isZh ? "测试中..." : "Testing...")
                    : (isZh ? "测试并创建" : "Test & Create")}
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
            const protocolLabels = [(resolveProtocol(p.protocol || "openai") ?? "openai-chat") as ProviderProtocol];
            const selectedPreset = providerPresets.find((preset) => preset.id === (p.provider || ""));
            const selectedProviderName = selectedPreset
              ? presetLabel(selectedPreset)
              : (p.provider || p.name);

            if (isEditing) {
              const editingPresetId = editForm.provider ?? "";
              const editingPreset = editingPresetId
                ? providerPresets.find((preset) => preset.id === editingPresetId) ?? null
                : null;
              const editCredentialFields = credentialFieldsForPreset(editingPreset);
              const editCredentialLayout = splitApiKeyCredentialField(editCredentialFields);
              const editPresetBaseUrl = editingPreset
                ? resolvePresetConfig(editingPreset, (editForm.protocol as ProviderProtocol) || "openai-chat").baseUrl
                : "";
              const editBaseUrlMissing = !editPresetBaseUrl && !editForm.base_url?.trim();
              const editProtocolOptions = availableProtocolsForPreset(editingPreset);
              // provider is fixed at creation time and can't be changed on
              // edit (it anchors the persisted credential/auth-scheme
              // lookup) — the quickselect shows only the assigned preset,
              // locked. Falls back to the full list only if the provider id
              // doesn't match any known preset (e.g. legacy/"custom" data),
              // so the picker is never left empty.
              const editLockedPresets = editingPreset ? [editingPreset] : providerPresets;
              return (
                <div key={p.id} className="glass rounded-2xl p-5 space-y-4">
                  <div className="flex items-center justify-between">
                    <h3 className="text-sm font-semibold text-slate-900">{isZh ? "编辑提供商" : "Edit Provider"}</h3>
                    <button onClick={() => setEditingId(null)} className="p-1 text-slate-400 hover:text-slate-600 cursor-pointer">
                      <X className="h-4 w-4" />
                    </button>
                  </div>
                  <div className="space-y-3">
                    <FieldLabel
                      info={
                        isZh
                          ? "创建后不可更改提供商预设"
                          : "The provider preset can't be changed after creation"
                      }
                    >
                      {isZh ? "提供商" : "Provider"}
                    </FieldLabel>
                    <ToggleGroup type="single" value={editingPresetId} className="provider-preset-group">
                      {editLockedPresets.map((preset) => (
                        <ToggleGroupItem
                          key={preset.id}
                          value={preset.id}
                          variant="outline"
                          size="lg"
                          disabled
                          className="provider-preset-card h-auto w-full flex-col gap-3 px-4 py-5"
                          aria-label={presetLabel(preset)}
                        >
                          <ProviderIcon
                            iconKey={preset.icon}
                            name={preset.icon ?? preset.name}
                            size={26}
                            className="provider-preset-icon provider-preset-icon-colored rounded-none border-0 bg-transparent"
                          />
                          <ProviderIcon
                            iconKey={preset.icon}
                            name={preset.icon ?? preset.name}
                            size={26}
                            monochrome
                            className="provider-preset-icon provider-preset-icon-mono rounded-none border-0 bg-transparent"
                          />
                          <span className={presetLabelClass(preset)}>{presetLabel(preset)}</span>
                        </ToggleGroupItem>
                      ))}
                    </ToggleGroup>
                  </div>
                  <div className="grid grid-cols-2 gap-4">
                    <div className="space-y-2">
                      <FieldLabel required>{isZh ? "名称" : "Name"}</FieldLabel>
                      <Input
                        placeholder={isZh ? "如：OpenAI 生产环境" : "e.g. OpenAI Production"}
                        value={editForm.name ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, name: e.target.value })}
                      />
                    </div>
                    <div className="space-y-2">
                      <FieldLabel required>{isZh ? "协议" : "Protocol"}</FieldLabel>
                      <Select
                        value={editForm.protocol ?? ""}
                        onValueChange={(value) => handleEditProtocolChange(value)}
                      >
                        <SelectTrigger>
                          <SelectValue />
                        </SelectTrigger>
                        <SelectContent>
                          {editProtocolOptions.map((protocol) => (
                            <SelectItem key={protocol} value={protocol}>
                              {protocolDisplayName(protocol) ?? protocol}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    </div>
                    {editCredentialLayout.apiKeyField ? (
                      <CredentialFieldInput
                        field={editCredentialLayout.apiKeyField}
                        value={editForm.credentials?.[editCredentialLayout.apiKeyField.name] ?? ""}
                        onChange={(value) =>
                          setEditForm((prev) => ({
                            ...prev,
                            credentials: { ...(prev.credentials ?? {}), [editCredentialLayout.apiKeyField!.name]: value },
                          }))
                        }
                        isZh={isZh}
                      />
                    ) : (
                      <div aria-hidden="true" />
                    )}
                    <div className="space-y-2">
                      <FieldLabel required>Base URL</FieldLabel>
                      <Input
                        placeholder={isZh ? "如：https://api.openai.com/v1" : "e.g. https://api.openai.com/v1"}
                        value={editForm.base_url ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, base_url: e.target.value })}
                      />
                    </div>
                    <div className="space-y-2">
                      <FieldLabel
                        required
                        info={
                          isZh
                            ? "用于创建模型时自动获取可用模型列表"
                            : "Used to auto-fetch available model list when creating models"
                        }
                      >
                        {isZh ? "模型发现" : "Model Discovery"}
                      </FieldLabel>
                      {editModelsMode === "url" ? (
                        <Input
                          placeholder={isZh ? "如：https://api.openai.com/v1/models" : "e.g. https://api.openai.com/v1/models"}
                          value={editForm.models_url ?? ""}
                          onChange={(e) => setEditForm({ ...editForm, models_url: e.target.value })}
                        />
                      ) : (
                        <textarea
                          ref={editModelsTextareaRef}
                          rows={1}
                          className="model-textarea nyro-shadcn-input flex min-h-[40px] w-full resize-none overflow-hidden rounded-md border border-border bg-background px-3 text-sm text-foreground transition-[border-color,background-color,color] outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300 disabled:cursor-not-allowed disabled:opacity-50"
                          placeholder={isZh ? "每行一个模型名，如：gpt-4o" : "One model per line, e.g. gpt-4o"}
                          value={editForm.models ?? ""}
                          onChange={(e) => {
                            setEditForm({ ...editForm, models: e.target.value });
                            autoGrowTextarea(e.target);
                          }}
                        />
                      )}
                      <ToggleGroup
                        type="single"
                        value={editModelsMode}
                        onValueChange={(value) => {
                          if (!value) return;
                          const mode = value as ModelsMode;
                          setEditModelsMode(mode);
                          setEditForm((prev) => ({
                            ...prev,
                            models_url: mode === "url" ? prev.models_url : "",
                            models: mode === "static" ? prev.models : "",
                          }));
                        }}
                        className="provider-region-group"
                      >
                        <ToggleGroupItem value="url" variant="outline" size="sm">
                          {isZh ? "自动发现" : "Auto Discovery"}
                        </ToggleGroupItem>
                        <ToggleGroupItem value="static" variant="outline" size="sm">
                          {isZh ? "手动填写" : "Manual Entry"}
                        </ToggleGroupItem>
                      </ToggleGroup>
                    </div>
                    <div className="space-y-2">
                      <FieldLabel>{isZh ? "代理地址" : "Proxy URL"}</FieldLabel>
                      <Input
                        placeholder={isZh ? "如：http://127.0.0.1:7890" : "e.g. http://127.0.0.1:7890"}
                        value={editForm.proxy_url ?? ""}
                        onChange={(e) => setEditForm({ ...editForm, proxy_url: e.target.value })}
                      />
                    </div>
                    {editCredentialLayout.otherFields.map((field) => (
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
                  </div>
                  <div className="flex gap-3">
                    <Button
                      onClick={() => {
                        setEditError(null);
                        const protocol = editForm.protocol || "openai-chat";
                        const baseUrl = toGatewayBaseUrl(editForm.base_url ?? "");
                        const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                        if (validation) {
                          setEditError(validation);
                          return;
                        }
                        const update: UpdateUpstream = buildUpdateUpstreamInput({
                          name: editForm.name || undefined,
                          provider: editForm.provider || undefined,
                          protocol,
                          base_url: baseUrl,
                          proxy_url: editForm.proxy_url ?? "",
                          models_url: editForm.models_url ?? "",
                          models: editForm.models ?? "",
                          credentials: editForm.credentials && Object.keys(editForm.credentials).length
                            ? editForm.credentials
                            : undefined,
                        });
                        const draft: CreateUpstream = buildCreateUpstreamInput({
                          name: editForm.name ?? "",
                          provider: editForm.provider || "custom",
                          protocol,
                          base_url: baseUrl,
                          proxy_url: editForm.proxy_url ?? "",
                          models_url: editForm.models_url ?? "",
                          models: editForm.models ?? "",
                          api_key: editForm.api_key ?? "",
                          credentials: editForm.credentials ?? {},
                        });
                        void handleUpdateHealthCheck(draft, { id: editForm.id, ...update });
                      }}
                      disabled={
                        updateMut.isPending
                        || isTestRunning
                        || missingRequiredCredentials(editCredentialFields, editForm.credentials ?? {})
                        || editBaseUrlMissing
                      }
                    >
                      {isTestRunning
                        ? (isZh ? "测试中..." : "Testing...")
                        : (isZh ? "测试并保存" : "Test & Save")}
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
                        iconKey={selectedPreset?.icon}
                        name={p.name}
                        protocol={p.protocol}
                        baseUrl={p.base_url}
                        size={30}
                        className="provider-preset-icon provider-preset-icon-colored rounded-xl border border-slate-300/70 bg-transparent"
                      />
                      <ProviderIcon
                        iconKey={selectedPreset?.icon}
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
                                : protocol === "google-gemini"
                                  ? "secondary"
                                  : "success"
                            }
                            className={`connect-label-badge ${protocol === "google-gemini" ? "bg-violet-50 text-violet-700" : ""}`}
                          >
                            {PROTOCOL_TABLE.find((pt) => pt.id === protocol)?.displayName ?? protocol}
                          </Badge>
                        ))}
                        {p.proxy_url && (
                          <Badge variant="success" className="connect-label-badge">
                            {isZh ? "代理" : "Proxy"}
                          </Badge>
                        )}
                        {!p.enabled && (
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
                        if (p.enabled) {
                          setProviderToDisable(p);
                        } else {
                          toggleEnabledMut.mutate({ id: p.id, is_enabled: true });
                        }
                      }}
                      title={p.enabled ? (isZh ? "禁用" : "Disable") : (isZh ? "启用" : "Enable")}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-slate-100 hover:text-slate-600 cursor-pointer"
                    >
                      {p.enabled ? (
                        <ToggleRight className="h-4 w-4 text-green-500" />
                      ) : (
                        <ToggleLeft className="h-4 w-4 text-slate-400" />
                      )}
                    </button>
                    <button
                      onClick={() => handleTest(p)}
                      disabled={Boolean(testingId) || Boolean(routeImportingId)}
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
                      onClick={() => handlePreviewRouteImport(p)}
                      disabled={Boolean(testingId) || Boolean(routeImportingId)}
                      title={isZh ? "导入模型路由" : "Import model routes"}
                      className="rounded-lg p-2 text-slate-400 transition-colors hover:bg-emerald-50 hover:text-emerald-600 cursor-pointer disabled:opacity-50"
                    >
                      {routeImportingId === p.id ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <RouteIcon className="h-3.5 w-3.5" />
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
              {testDialogMode === "create"
                ? (isZh ? `创建前测试 ${pendingCreateInput?.name ?? ""}` : `Pre-create test ${pendingCreateInput?.name ?? ""}`)
                : testDialogMode === "edit"
                  ? (isZh ? `保存前测试 ${editForm.name ?? ""}` : `Pre-save test ${editForm.name ?? ""}`)
                  : testDialogMode === "route_import"
                    ? (isZh ? `导入模型路由 ${testTarget?.name ?? ""}` : `Import routes for ${testTarget?.name ?? ""}`)
                    : (isZh ? `测试 ${testTarget?.name ?? ""}` : `Test ${testTarget?.name ?? ""}`)}
            </DialogTitle>
            <DialogDescription>
              {testDialogMode === "create"
                ? (isZh ? "实时展示创建前验证流水线" : "Real-time pre-create validation pipeline")
                : testDialogMode === "edit"
                  ? (isZh ? "实时展示保存前验证流水线" : "Real-time pre-save validation pipeline")
                  : testDialogMode === "route_import"
                    ? (isZh ? "实时展示模型路由导入进度" : "Real-time progress for route import")
                    : (isZh ? "实时展示 Provider 测试日志" : "Real-time logs for provider testing")}
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
            {testDialogMode === "create" && createHealthPassed && pendingCreateInput ? (
              <Button onClick={() => createMut.mutate(pendingCreateInput)} disabled={createMut.isPending}>
                {createMut.isPending
                  ? (isZh ? "创建中..." : "Creating...")
                  : (isZh ? "完成创建" : "Create Provider")}
              </Button>
            ) : testDialogMode === "edit" && editHealthPassed && pendingUpdateInput ? (
              <Button onClick={() => updateMut.mutate(pendingUpdateInput)} disabled={updateMut.isPending}>
                {updateMut.isPending
                  ? (isZh ? "保存中..." : "Saving...")
                  : (isZh ? "完成保存" : "Save Provider")}
              </Button>
            ) : (
              <Button variant="secondary" onClick={closeTestDialog}>
                {isTestRunning
                  ? (isZh ? "取消" : "Cancel")
                  : (isZh ? "关闭" : "Close")}
              </Button>
            )}
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
        open={Boolean(routeImportPreview)}
        onOpenChange={(open) => {
          if (!open) setRouteImportPreview(null);
        }}
        title={isZh ? "确认导入模型路由" : "Confirm route import"}
        description={
          routeImportPreview
            ? (isZh
              ? `将从「${routeImportPreview.provider.name}」导入当前模型列表。`
              : `Import the current model list from "${routeImportPreview.provider.name}".`)
            : undefined
        }
        content={routeImportPreview ? (
          <div className="space-y-3 text-sm text-slate-700">
            <div className="grid grid-cols-3 gap-2">
              <div className="rounded-lg border border-slate-200 bg-slate-50 px-3 py-2">
                <div className="text-xs text-slate-500">{isZh ? "发现" : "Discovered"}</div>
                <div className="mt-1 text-lg font-semibold text-slate-900">{routeImportPreview.preview.discovered}</div>
              </div>
              <div className="rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-2">
                <div className="text-xs text-emerald-700">{isZh ? "将创建" : "Create"}</div>
                <div className="mt-1 text-lg font-semibold text-emerald-800">{routeImportPreview.preview.create.length}</div>
              </div>
              <div className="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2">
                <div className="text-xs text-amber-700">{isZh ? "跳过" : "Skip"}</div>
                <div className="mt-1 text-lg font-semibold text-amber-800">{routeImportPreview.preview.skip.length}</div>
              </div>
            </div>
            <div className="rounded-lg border border-slate-200 bg-white p-3">
              <div className="text-xs font-medium text-slate-600">{isZh ? "将创建的路由" : "Routes to create"}</div>
              <div className="mt-2 flex flex-wrap gap-1.5">
                {routeImportPreview.preview.create.slice(0, 8).map((model) => (
                  <Badge key={model} variant="success" className="connect-label-badge">{model}</Badge>
                ))}
                {routeImportPreview.preview.create.length > 8 && (
                  <Badge variant="secondary" className="connect-label-badge">+{routeImportPreview.preview.create.length - 8}</Badge>
                )}
                {routeImportPreview.preview.create.length === 0 && (
                  <span className="text-xs text-slate-500">{isZh ? "没有需要创建的路由" : "No routes need to be created"}</span>
                )}
              </div>
            </div>
            {routeImportPreview.preview.skip.length > 0 && (
              <p className="text-xs text-slate-500">
                {isZh
                  ? `已有 ${routeImportPreview.preview.skip.length} 个同名路由会被跳过，不会修改现有路由。`
                  : `${routeImportPreview.preview.skip.length} existing routes will be skipped; existing routes are not modified.`}
              </p>
            )}
          </div>
        ) : undefined}
        cancelText={isZh ? "取消" : "Cancel"}
        confirmText={isZh ? "确认导入" : "Import"}
        confirmClassName="bg-emerald-600 text-white hover:bg-emerald-500"
        onConfirm={() => {
          if (!routeImportPreview) return;
          const provider = routeImportPreview.provider;
          setRouteImportPreview(null);
          void handleImportRoutes(provider);
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
