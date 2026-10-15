import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode, type SVGProps } from "react";
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
  Pencil,
  ChevronLeft,
  ChevronRight,
  Eye,
  EyeOff,
  X,
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { NyroIcon } from "@/components/ui/nyro-icon";
import { RowActionMenu } from "@/components/ui/row-action-menu";
import { NyroSelect } from "@/components/ui/nyro-select";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { Drawer, Modal } from "@/components/ui/overlays";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { CopyValue } from "@/components/ui/copy-value";
import { DetailList } from "@/components/ui/detail-list";
import { copyableTipProps } from "@/lib/copyable";
import { formatLogTime } from "@/lib/format";
import { resolveProtocol, PROTOCOL_TABLE } from "@/lib/protocol";
import { openExternalUrl } from "@/lib/open-external";
import { localizedMessage, type MessageKey } from "@/lib/messages";


function HeartbeatIcon(props: SVGProps<SVGSVGElement>) {
  return (
    <svg viewBox="0 0 24 24" fill="none" aria-hidden="true" {...props}>
      <path
        d="M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2"
        stroke="currentColor"
        strokeWidth={1.7}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

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
};
const PAGE_SIZE = 7;
const DEFAULT_PRESET_ID = "nyro";
const protocolOptions = [
  { label: "OpenAI Compatible", value: "openai-compatible" },
  { label: "OpenAI Responses",  value: "openai-responses"  },
  { label: "Anthropic Messages", value: "anthropic-messages" },
  { label: "Google Gemini", value: "google-gemini" },
] as const satisfies ReadonlyArray<{ label: string; value: ProviderProtocol }>;

function validateProviderEndpoint(
  protocol: string | undefined,
  baseUrl: string | undefined,
  isZh: boolean,
): string | null {
  if (!protocol?.trim()) {
    return localizedMessage(isZh, "providers.protocolIsRequired");
  }
  const trimmed = baseUrl?.trim() ?? "";
  if (!trimmed) {
    return localizedMessage(isZh, "providers.baseURLIsRequired");
  }
  try {
    new URL(trimmed);
  } catch {
    return localizedMessage(isZh, "providers.invalidBaseURL", { a: baseUrl ?? "" });
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

  if (protocol === "google-gemini") {
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

// Field label. Explanations use the nyro help tip instead of Tooltip.
function FieldLabel({ children, info, required }: { children: string; info?: string; required?: boolean }) {
  return (
    <span className="field-label">
      {required ? <span className="required" aria-hidden="true">*</span> : null}
      <span>{children}</span>
      {info ? (
        <span className="help" aria-label={info}>
          ?
          <span className="help-tip" role="tooltip">{info}</span>
        </span>
      ) : null}
    </span>
  );
}

type ModelsMode = "url" | "static";

function pickModelsMode(current: ModelsMode, modelsSource?: string, staticModels?: string): ModelsMode {
  if (modelsSource?.trim()) return "url";
  if (staticModels?.trim()) return "static";
  return current;
}

function modelsFromText(value?: string) {
  if (!value) return [];
  const seen = new Set<string>();
  const models: string[] = [];
  for (const part of value.split(/[\n,]/)) {
    const item = part.trim();
    if (!item || seen.has(item)) continue;
    seen.add(item);
    models.push(item);
  }
  return models;
}

function ProviderFormSection({
  title,
  description,
  plain = false,
  children,
}: {
  title?: string;
  description?: string;
  plain?: boolean;
  children: ReactNode;
}) {
  return (
    <section className={plain ? "form-section form-section-plain" : "form-section"}>
      {title ? <h3 className="form-section-title">{title}</h3> : null}
      {description ? <p className="form-section-desc">{description}</p> : null}
      <div className="form-grid">{children}</div>
    </section>
  );
}

function ModelTagInput({
  value,
  onChange,
  isZh,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  isZh: boolean;
}) {
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const helpId = useId();

  function commit(tags: string[]) {
    const next = modelsFromText([...value, ...tags].join("\n"));
    if (next.length !== value.length || next.some((model, index) => model !== value[index])) onChange(next);
    setDraft("");
  }

  function onKeyDown(event: ReactKeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing) return;
    if (event.key === "Enter" || event.key === ",") {
      event.preventDefault();
      commit(modelsFromText(draft));
      return;
    }
    if (event.key === "Backspace" && !draft && value.length > 0) {
      event.preventDefault();
      onChange(value.slice(0, -1));
    }
  }

  return (
    <>
      <div className="field-control tags-control model-tag-input" onClick={() => inputRef.current?.focus()}>
        <ul className="model-tag-list" aria-label={localizedMessage(isZh, "providers.manualModelList")}>
          {value.map((model, index) => (
            <li key={model} className="tag">
              <code>{model}</code>
              <button
                type="button"
                className="tag-remove"
                aria-label={localizedMessage(isZh, "providers.remove", { a: model })}
                onClick={(event) => {
                  event.stopPropagation();
                  onChange(value.filter((_, itemIndex) => itemIndex !== index));
                  inputRef.current?.focus();
                }}
              >
                <X aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
        <input
          ref={inputRef}
          type="text"
          className="tag-input"
          aria-label={localizedMessage(isZh, "providers.enterModels")}
          aria-describedby={helpId}
          placeholder={value.length === 0 ? (localizedMessage(isZh, "providers.enterAModelID")) : undefined}
          value={draft}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={onKeyDown}
          onBlur={() => commit(modelsFromText(draft))}
          onPaste={(event) => {
            const pasted = event.clipboardData.getData("text");
            if (!pasted) return;
            event.preventDefault();
            commit(modelsFromText(`${draft}\n${pasted}`));
          }}
        />
      </div>
      <p id={helpId} className="field-hint">
        {localizedMessage(isZh, "providers.pressEnterOrCommaToAddYouCanPasteSeveralModelsAtOnce")}
      </p>
    </>
  );
}

function ModelDiscoveryField({
  isZh,
  mode,
  onMode,
  source,
  onSource,
  modelsText,
  onModelsText,
}: {
  isZh: boolean;
  mode: ModelsMode;
  onMode: (mode: ModelsMode) => void;
  source: string;
  onSource: (value: string) => void;
  modelsText: string;
  onModelsText: (value: string) => void;
}) {
  const help = localizedMessage(isZh, "providers.autoFetchTheAvailableModelListOrMaintainTheListManually");
  return (
    <div className="field full">
      <div className="field-label discover-label">
        <span>
          <span className="required" aria-hidden="true">*</span>
          {localizedMessage(isZh, "providers.modelDiscovery")}
        </span>
        <span className="help" aria-label={help}>
          ?
          <span className="help-tip" role="tooltip">{help}</span>
        </span>
        <div className="discover-switch" role="radiogroup" aria-label={localizedMessage(isZh, "providers.modelDiscovery")}>
          <button type="button" role="radio" aria-checked={mode === "url"} className={mode === "url" ? "selected" : ""} onClick={() => onMode("url")}>
            {localizedMessage(isZh, "providers.autoDiscovery")}
          </button>
          <button type="button" role="radio" aria-checked={mode === "static"} className={mode === "static" ? "selected" : ""} onClick={() => onMode("static")}>
            {localizedMessage(isZh, "providers.manualEntry")}
          </button>
        </div>
      </div>
      {mode === "url" ? (
        <input
          className="field-control"
          placeholder={localizedMessage(isZh, "providers.eGHttpsApiOpenaiComV1Models")}
          aria-label={localizedMessage(isZh, "providers.modelDiscovery")}
          value={source}
          onChange={(event) => onSource(event.target.value)}
        />
      ) : (
        <ModelTagInput
          isZh={isZh}
          value={modelsFromText(modelsText)}
          onChange={(models) => onModelsText(models.join("\n"))}
        />
      )}
    </div>
  );
}

function PresetMark({ preset }: { preset: ProviderPreset }) {
  if (preset.icon === "nyro" || preset.icon === "custom") {
    return <NyroIcon size={20} />;
  }
  return <ProviderIcon name={preset.icon ?? preset.label.en} size={20} />;
}

// Provider preset picker. Clicking the current selection again does not reapply the preset.
function PresetPick({
  presets,
  value,
  isZh,
  onSelect,
  isItemDisabled,
}: {
  presets: ProviderPreset[];
  value: string;
  isZh: boolean;
  onSelect: (id: string) => void;
  isItemDisabled?: (preset: ProviderPreset) => boolean;
}) {
  const ordered = [...presets].sort((a, b) => (a.id === DEFAULT_PRESET_ID ? -1 : b.id === DEFAULT_PRESET_ID ? 1 : 0));
  return (
    <div className="provider-pick">
      {ordered.map((preset) => {
        const disabled = isItemDisabled?.(preset) ?? false;
        const selected = preset.id === value;
        return (
          <button
            key={preset.id}
            type="button"
            className={selected ? "provider-pick-item selected" : "provider-pick-item"}
            aria-label={presetLabel(preset, isZh)}
            aria-pressed={selected}
            disabled={disabled}
            onClick={() => {
              if (disabled || selected) return;
              onSelect(preset.id);
            }}
          >
            <PresetMark preset={preset} />
            <span className={presetLabelClass(preset, isZh)}>{presetLabel(preset, isZh)}</span>
          </button>
        );
      })}
    </div>
  );
}

// Channel picker. Clicking the current selection again does not switch.
function ChannelPick({
  channels,
  value,
  isZh,
  onSelect,
  isItemDisabled,
}: {
  channels: ProviderChannelPreset[];
  value: string;
  isZh: boolean;
  onSelect: (id: string) => void;
  isItemDisabled?: (channel: ProviderChannelPreset) => boolean;
}) {
  return (
    <div className="provider-pick">
      {channels.map((channel) => {
        const disabled = isItemDisabled?.(channel) ?? false;
        const selected = channel.id === value;
        return (
          <button
            key={channel.id}
            type="button"
            className={selected ? "provider-pick-item selected" : "provider-pick-item"}
            disabled={disabled}
            aria-pressed={selected}
            onClick={() => {
              if (disabled || selected) return;
              onSelect(channel.id);
            }}
          >
            <span>{channelLabel(channel, isZh)}</span>
          </button>
        );
      })}
    </div>
  );
}

type TestLogLevel = "info" | "success" | "error";

type TestLogEntry = {
  timestamp: string;
  level: TestLogLevel;
  message: string;
};

function nowTimestamp() {
  const now = new Date();
  const hh = String(now.getHours()).padStart(2, "0");
  const mm = String(now.getMinutes()).padStart(2, "0");
  const ss = String(now.getSeconds()).padStart(2, "0");
  return `${hh}:${mm}:${ss}`;
}

export default function ProvidersPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  const qc = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [testingId, setTestingId] = useState<string | null>(null);
  const [testDialogOpen, setTestDialogOpen] = useState(false);
  const [testLogs, setTestLogs] = useState<TestLogEntry[]>([]);
  const [isTestRunning, setIsTestRunning] = useState(false);
  const [testTarget, setTestTarget] = useState<Provider | null>(null);
  const [selectedProvider, setSelectedProvider] = useState<Provider | null>(null);
  const [providerToDelete, setProviderToDelete] = useState<Provider | null>(null);
  const [providerToCopy, setProviderToCopy] = useState<Provider | null>(null);
  const [appendTargets, setAppendTargets] = useState(false);
  const [selectedPresetId, setSelectedPresetId] = useState(DEFAULT_PRESET_ID);
  const [showCreateApiKey, setShowCreateApiKey] = useState(true);
  const [showEditApiKey, setShowEditApiKey] = useState(false);
  const [modelsMode, setModelsMode] = useState<ModelsMode>("url");
  const [editModelsMode, setEditModelsMode] = useState<ModelsMode>("url");
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
      showErrorDialog("providers.failedToCreateProvider", error);
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
      showErrorDialog("providers.failedToCreateOAuthProvider", error);
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
      showErrorDialog("providers.failedToRefreshOAuthCredential", error);
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
      showErrorDialog("providers.failedToDisconnectOAuth", error);
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
      showErrorDialog("providers.failedToSaveProvider", err);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_provider", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("providers.failedToDeleteProvider", error);
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
      showErrorDialog("providers.failedToCopyProvider", error);
    },
  });

  const [providerToDisable, setProviderToDisable] = useState<Provider | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_provider", { id, input: { is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("common.operationFailed", error);
    },
  });

  function appendTestLog(level: TestLogLevel, message: string) {
    setTestLogs((prev) => [...prev, { timestamp: nowTimestamp(), level, message }]);
  }

  function normalizeErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleKey: MessageKey, error: unknown) {
    setErrorDialog({
      title: localizedMessage(isZh, titleKey),
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
        title: localizedMessage(isZh, "providers.cannotStartOAuth"),
        description: localizedMessage(isZh, "providers.pleaseSelectAnOAuthProviderPresetFirst"),
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
                message: localizedMessage(isZh, "providers.oauthSessionTimedOutPleaseStartAgain"),
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
        message: localizedMessage(isZh, "providers.cancelled"),
      });
      return;
    }
    try {
      await backend("cancel_oauth_session", { sessionId });
      setCreateOAuthStatus({
        status: "error",
        code: "OAUTH_CANCELLED",
        message: localizedMessage(isZh, "providers.cancelled"),
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
        message: localizedMessage(isZh, "providers.pasteTheFullCallbackURLOrEnterTheAuthorizationCode"),
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
                message: localizedMessage(isZh, "providers.oauthSessionTimedOut"),
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
        message: localizedMessage(isZh, "providers.pasteTheFullCallbackURLOrEnterTheAuthorizationCode"),
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

    const finish = (finalMessage: string, level: "success" | "error") => {
      if (isCanceled()) return;
      appendTestLog(level, finalMessage);
      void qc.invalidateQueries({ queryKey: ["providers"] });
      setIsTestRunning(false);
      setTestingId(null);
    };

    try {
      const protocol = (resolveProtocol(provider.protocol || "openai") ?? "openai-compatible") as ProviderProtocol;
      const baseUrl = provider.base_url?.trim() ?? "";

      appendTestLog("info", localizedMessage(isZh, "providers.startTesting", { a: provider.name }));
      appendTestLog("info", localizedMessage(isZh, "providers.connectivityCheck"));
      appendTestLog("info", `→ [${protocol}] ${baseUrl}`);

      const connectivity = await backend<TestResult>("test_provider", { id: provider.id });
      if (isCanceled()) return;

      if (!connectivity.success) {
        const reason = connectivity.error ?? (localizedMessage(isZh, "providers.connectivityCheckFailed"));
        finish(
          `${localizedMessage(isZh, "providers.connectivityCheckFailed2")}: ${reason}`,
          "error",
        );
        return;
      }

      appendTestLog(
        "success",
        `${localizedMessage(isZh, "providers.connectivityOkLatency")} ${connectivity.latency_ms}ms`,
      );

      const modelsSource = provider.models_source?.trim();
      if (!modelsSource) {
        finish(
          localizedMessage(isZh, "providers.modelDiscoverySourceNotConfiguredTestFinished"),
          "success",
        );
        return;
      }

      appendTestLog("info", localizedMessage(isZh, "providers.fetchModelList"));
      appendTestLog("info", `→ ${modelsSource}`);

      const models = await backend<string[]>("test_provider_models", { id: provider.id });
      if (isCanceled()) return;

      if (!models.length) {
        finish(
          localizedMessage(isZh, "providers.modelListIsEmptyOrMalformed2"),
          "error",
        );
        return;
      }

      appendTestLog(
        "success",
        `${localizedMessage(isZh, "providers.authValidFetched")} ${models.length} ${localizedMessage(isZh, "providers.models")}`,
      );
      models.forEach((model) => appendTestLog("info", `· ${model}`));

      finish(
        localizedMessage(isZh, "providers.testCompleted"),
        "success",
      );
    } catch (error: unknown) {
      if (isCanceled()) return;
      const message = normalizeErrorMessage(error);
      finish(
        `${localizedMessage(isZh, "providers.testFailed")}: ${message}`,
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
      auth_mode: normalizeAuthMode(p.auth_mode),
    });
    setEditModelsMode(pickModelsMode("url", p.models_source ?? "", p.static_models ?? ""));
  }

  function handlePresetChange(nextPresetId: string) {
    if (!nextPresetId) return;
    resetCreateOAuthState(true);
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
      name: "",
    });
    setModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));
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
    setModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));
  }

  function handleEditPresetChange(nextPresetId: string) {
    if (!nextPresetId) return;
    const preset = providerPresets.find((item) => item.id === nextPresetId);
    if (!preset) return;

    const nextChannelId = preset.channels?.[0]?.id ?? "";
    const nextAuthMode = presetChannelAuthMode(preset, nextChannelId);
    if (nextAuthMode === "oauth" && normalizeAuthMode(editingProvider?.auth_mode) !== "oauth") {
      setEditError(
        localizedMessage(isZh, "providers.existingProvidersCannotSwitchDirectlyToAnOAuthChannelWhi"),
      );
      return;
    }

    setEditError(null);
    const nextProtocol = resolvePresetProtocol(
      preset,
      nextChannelId,
      (editForm.protocol as ProviderProtocol) || (resolveProtocol(preset.defaultProtocol) ?? "openai-compatible") as ProviderProtocol,
    );
    const config = resolvePresetConfig(preset, nextProtocol, nextChannelId);
    const nextBaseUrl = config.baseUrl || protocolUrl(nextProtocol);
    const baseUrl = isVertexProviderSelection(editForm)
      ? (nextBaseUrl || defaultVertexBaseUrl(nextProtocol))
      : nextBaseUrl;
    setEditModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));
    setEditForm((prev) =>
      prev
        ? {
            ...prev,
            vendor: preset.id === DEFAULT_PRESET_ID ? undefined : preset.id,
            preset_key: preset.id,
            channel: nextChannelId,
            protocol: nextProtocol,
            base_url: baseUrl,
            models_source: config.modelsSource,
            static_models: config.staticModels,
            api_key: config.apiKey || prev.api_key,
          }
        : prev,
    );
  }

  function closeCreateForm() {
    resetCreateOAuthState(true);
    setShowForm(false);
    setShowCreateApiKey(true);
    setSelectedPresetId(DEFAULT_PRESET_ID);
    setModelsMode("url");
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
    return () => {
      stopCreateOAuthPolling();
    };
  }, []);

  // Derived fields for the edit drawer, computed the same way as the old inline list form.
  const editingPresetId = editForm.preset_key || DEFAULT_PRESET_ID;
  const editingPreset =
    providerPresets.find((preset) => preset.id === editingPresetId) ?? providerPresets[0] ?? null;
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
  const currentProviderIsOAuth = Boolean(
    editingProvider && (
      normalizeAuthMode(editingProvider.auth_mode) === "oauth"
      || normalizeAuthMode(editForm.auth_mode) === "oauth"
    ),
  );
  const editRequiresNewOAuthProvider = editingResolvedAuthMode === "oauth" && !currentProviderIsOAuth;
  const editOAuthStatus = editOAuthStatusQuery.data;

  return (
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "common.providers")}
          description={localizedMessage(isZh, "providers.manageYourLLMProviderConnections")}
          actions={(
            <button
              type="button"
              className="button button-primary"
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
            >
              <Plus />
              {localizedMessage(isZh, "providers.addProvider")}
            </button>
          )}
        />
      )}
    >
      {isLoading ? (
        <div className="card">
          <div className="card-header">
            <h2 className="card-title">{localizedMessage(isZh, "providers.providerList")}</h2>
          </div>
          <div className="card-body">
            <div className="empty">
              <span className="spinner" />
              <strong>{localizedMessage(isZh, "common.loading")}</strong>
            </div>
          </div>
        </div>
      ) : providers.length === 0 ? (
        <div className="card">
          <div className="card-header">
            <h2 className="card-title">{localizedMessage(isZh, "providers.providerList")}</h2>
          </div>
          <div className="card-body">
            <div className="empty">
              <Server />
              <strong>{localizedMessage(isZh, "providers.noProvidersConfiguredYet")}</strong>
              <p>{localizedMessage(isZh, "providers.addAProviderToGetStarted")}</p>
            </div>
          </div>
        </div>
      ) : (
        <div className="card table-card">
          <div className="card-header">
            <h2 className="card-title">{localizedMessage(isZh, "providers.providerList")}</h2>
          </div>
          <div className="table-toolbar">
            <span className="table-summary">
              {localizedMessage(isZh, "providers.providers", { a: providers.length })}
            </span>
          </div>
          <div className="table-scroll">
            <table className="table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "common.provider")}</th>
                  <th>{localizedMessage(isZh, "common.protocol")}</th>
                  <th>{localizedMessage(isZh, "providers.baseURL")}</th>
                  <th>{localizedMessage(isZh, "providers.health")}</th>
                  <th>{localizedMessage(isZh, "common.enabled")}</th>
                  <th className="cell-actions">{localizedMessage(isZh, "common.actions")}</th>
                </tr>
              </thead>
              <tbody>
                {pagedProviders.map((p) => {
                  const healthLabel = p.last_test_success === true
                    ? localizedMessage(isZh, "providers.testPassed")
                    : p.last_test_success === false
                      ? localizedMessage(isZh, "providers.testFailed2")
                      : localizedMessage(isZh, "providers.notTested");
                  const healthClass = p.last_test_success === true
                    ? "health"
                    : p.last_test_success === false
                      ? "health danger"
                      : "health is-idle";
                  const testedAt = p.last_test_at?.trim();
                  const rowPreset = providerPresets.find((preset) => preset.id === (p.preset_key || p.vendor || ""));
                  const selectedProviderName = rowPreset
                    ? presetLabel(rowPreset, isZh)
                    : (p.vendor || p.preset_key || p.name);
                  const protocolLabels = [(resolveProtocol(p.protocol || "openai") ?? "openai-compatible") as ProviderProtocol];
                  return (
                    <tr
                      key={p.id}
                      className="clickable"
                      onClick={() => {
                        setShowForm(false);
                        setEditingId(null);
                        setSelectedProvider(p);
                      }}
                    >
                      <td>
                        <div className="provider-name">
                          <ProviderIcon
                            name={p.name}
                            protocol={p.protocol}
                            baseUrl={p.base_url}
                            size={28}
                          />
                          <div>
                            <strong className="name-ellipsis" {...copyableTipProps(p.name)}>{p.name}</strong>
                            <small>{selectedProviderName}</small>
                          </div>
                        </div>
                      </td>
                      <td>
                        {protocolLabels.map((protocol) => (
                          <span key={`${p.id}-${protocol}`}>
                            {PROTOCOL_TABLE.find((pt) => pt.id === protocol)?.displayName ?? protocol}
                            {isGlobalProxyEnabled && p.use_proxy ? (localizedMessage(isZh, "providers.proxy")) : ""}
                            {normalizeAuthMode(p.auth_mode) === "oauth" ? " · OAuth" : ""}
                          </span>
                        ))}
                      </td>
                      <td>
                        <div className="cell-stack">
                          <span className="provider-link" {...copyableTipProps(p.base_url)}>{p.base_url || "—"}</span>
                        </div>
                      </td>
                      <td>
                        <span
                          className={healthClass}
                          data-tip={testedAt ? formatLogTime(testedAt) : undefined}
                          aria-label={healthLabel}
                        >
                          <span className="mini-dot" />
                          {healthLabel}
                        </span>
                      </td>
                      <td className="cell-switch">
                        <button
                          type="button"
                          role="switch"
                          className={p.is_enabled ? "switch-control sm on" : "switch-control sm"}
                          aria-checked={p.is_enabled}
                          data-tip={p.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                          aria-label={p.is_enabled ? (localizedMessage(isZh, "common.disable")) : (localizedMessage(isZh, "common.enable"))}
                          onClick={(event) => {
                            event.stopPropagation();
                            if (p.is_enabled) {
                              setProviderToDisable(p);
                            } else {
                              toggleEnabledMut.mutate({ id: p.id, is_enabled: true });
                            }
                          }}
                        />
                      </td>
                      <td className="cell-actions">
                        <div className="row-actions">
                          <button
                            type="button"
                            className="icon-action"
                            data-tip={localizedMessage(isZh, "providers.healthTest")}
                            aria-label={localizedMessage(isZh, "providers.healthTest")}
                            onClick={(event) => {
                              event.stopPropagation();
                              handleTest(p);
                            }}
                            disabled={Boolean(testingId)}
                          >
                            {testingId === p.id ? <span className="spinner" /> : <HeartbeatIcon />}
                          </button>
                          <button
                            type="button"
                            className="icon-action"
                            data-tip={localizedMessage(isZh, "common.edit")}
                            aria-label={localizedMessage(isZh, "common.edit")}
                            onClick={(event) => {
                              event.stopPropagation();
                              if (editingId === p.id) return;
                              setSelectedProvider(null);
                              startEdit(p);
                            }}
                          >
                            <Pencil aria-hidden="true" />
                          </button>
                          <RowActionMenu label={localizedMessage(isZh, "providers.more")}>
                            <button
                              type="button"
                              disabled={copyMut.isPending}
                              onClick={() => {
                                setAppendTargets(false);
                                setProviderToCopy(p);
                              }}
                            >
                              {localizedMessage(isZh, "common.copy")}
                            </button>
                            <button
                              type="button"
                              className="danger"
                              onClick={() => setProviderToDelete(p)}
                            >
                              {localizedMessage(isZh, "common.delete")}
                            </button>
                          </RowActionMenu>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
          {providers.length > PAGE_SIZE ? (
            <div className="table-footer">
              <span className="table-summary">
                {localizedMessage(isZh, "common.pageOf", { a: page + 1, b: totalPages })}
              </span>
              <div className="pager">
                <button
                  type="button"
                  className="button button-sm"
                  onClick={() => setPage(Math.max(0, page - 1))}
                  disabled={page === 0}
                  data-tip={localizedMessage(isZh, "common.previousPage")}
                  aria-label={localizedMessage(isZh, "common.previousPage")}
                >
                  <ChevronLeft aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="button button-sm"
                  onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                  disabled={page >= totalPages - 1}
                  data-tip={localizedMessage(isZh, "common.nextPage")}
                  aria-label={localizedMessage(isZh, "common.nextPage")}
                >
                  <ChevronRight aria-hidden="true" />
                </button>
              </div>
            </div>
          ) : null}
        </div>
      )}

      <Drawer
        open={Boolean(selectedProvider)}
        title={selectedProvider?.name ?? localizedMessage(isZh, "providers.providerDetails")}
        description={selectedProvider?.id}
        onClose={() => setSelectedProvider(null)}
        footerStart={selectedProvider ? (
          <button
            type="button"
            className="button button-secondary button-danger-text"
            onClick={() => {
              const provider = selectedProvider;
              setSelectedProvider(null);
              setProviderToDelete(provider);
            }}
          >
            {localizedMessage(isZh, "common.delete")}
          </button>
        ) : undefined}
        footer={selectedProvider ? (
          <>
            <button type="button" className="button button-secondary" onClick={() => setSelectedProvider(null)}>
              {localizedMessage(isZh, "common.close")}
            </button>
            <button
              type="button"
              className="button button-secondary"
              onClick={() => {
                const provider = selectedProvider;
                setSelectedProvider(null);
                void handleTest(provider);
              }}
            >
              {localizedMessage(isZh, "providers.healthTest")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                const provider = selectedProvider;
                setSelectedProvider(null);
                startEdit(provider);
              }}
            >
              {localizedMessage(isZh, "common.edit")}
            </button>
          </>
        ) : undefined}
      >
        {selectedProvider ? (
          <DetailList
            items={[
              { label: localizedMessage(isZh, "common.name"), value: <CopyValue value={selectedProvider.name} isZh={isZh} /> },
              { label: localizedMessage(isZh, "common.protocol"), value: PROTOCOL_TABLE.find((item) => item.id === (resolveProtocol(selectedProvider.protocol || "openai") ?? "openai-compatible"))?.displayName ?? selectedProvider.protocol },
              { label: localizedMessage(isZh, "providers.baseURL"), value: <CopyValue value={selectedProvider.base_url} isZh={isZh} /> },
              { label: localizedMessage(isZh, "providers.health"), value: selectedProvider.last_test_success === true ? localizedMessage(isZh, "providers.testPassed") : selectedProvider.last_test_success === false ? localizedMessage(isZh, "providers.testFailed2") : localizedMessage(isZh, "providers.notTested") },
              { label: localizedMessage(isZh, "providers.lastTested"), value: selectedProvider.last_test_at ? formatLogTime(selectedProvider.last_test_at) : "—" },
              { label: localizedMessage(isZh, "providers.authMode"), value: normalizeAuthMode(selectedProvider.auth_mode) === "oauth" ? "OAuth" : "API Key" },
              { label: localizedMessage(isZh, "providers.useLocalProxy"), value: selectedProvider.use_proxy ? localizedMessage(isZh, "common.enabled2") : localizedMessage(isZh, "common.disabled") },
              { label: localizedMessage(isZh, "providers.modelDiscovery"), value: selectedProvider.models_source ? <CopyValue value={selectedProvider.models_source} isZh={isZh} /> : "—" },
              { label: localizedMessage(isZh, "common.enabled"), value: selectedProvider.is_enabled ? localizedMessage(isZh, "common.enabled2") : localizedMessage(isZh, "common.disabled") },
              { label: localizedMessage(isZh, "common.createdAt"), value: selectedProvider.created_at },
              { label: localizedMessage(isZh, "common.updatedAt"), value: selectedProvider.updated_at },
            ]}
          />
        ) : null}
      </Drawer>

      {/* Create form: right-side drawer */}
      <Drawer
        open={showForm}
        title={localizedMessage(isZh, "providers.newProvider")}
        description={localizedMessage(isZh, "providers.configureConnectionCredentialsAndModelDiscovery")}
        onClose={closeCreateForm}
        footer={(
          <>
            <button type="button" className="button button-secondary" onClick={closeCreateForm}>
              {localizedMessage(isZh, "common.cancel")}
            </button>
            <button
              type="button"
              className="button button-primary"
              onClick={() => {
                const protocol = form.protocol || "openai-compatible";
                const baseUrl = toGatewayBaseUrl(form.base_url ?? "");
                const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                if (validation) {
                  setErrorDialog({
                    title: localizedMessage(isZh, "providers.failedToCreateProvider"),
                    description: validation,
                  });
                  return;
                }
                const input: CreateProvider = {
                  ...form,
                  protocol,
                  base_url: baseUrl,
                  models_source: createResolvedAuthMode === "oauth" || modelsMode === "url" ? (form.models_source ?? "") : "",
                  static_models: createResolvedAuthMode === "oauth" || modelsMode === "static" ? (form.static_models ?? "") : "",
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
                ? (localizedMessage(isZh, "common.creating"))
                : (localizedMessage(isZh, "common.create"))}
            </button>
          </>
        )}
      >
        <div className="add-provider-form">
          <ProviderFormSection plain>
            <div className="field full">
              <span className="field-label">{localizedMessage(isZh, "common.provider")}</span>
              {hasCreatePresets ? (
                <PresetPick
                  presets={providerPresets}
                  value={selectedPresetId}
                  isZh={isZh}
                  onSelect={handlePresetChange}
                />
              ) : (
                <div className="empty">
                  <strong>
                    {localizedMessage(isZh, "providers.noProviderPresetsAreAvailable")}
                  </strong>
                </div>
              )}
            </div>
            {createChannelOptions.length > 1 ? (
              <div className="field full">
                <span className="field-label">{localizedMessage(isZh, "providers.channel")}</span>
                <ChannelPick
                  channels={createChannelOptions}
                  value={createChannelValue}
                  isZh={isZh}
                  onSelect={(value) => {
                    if (!selectedPreset?.channels?.length) return;
                    handlePresetChannelChange(value);
                  }}
                />
              </div>
            ) : null}
            <label className="field">
              <FieldLabel required>{localizedMessage(isZh, "common.name")}</FieldLabel>
              <input
                className="field-control"
                placeholder={localizedMessage(isZh, "providers.eGOpenAIProduction")}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </label>
            {createResolvedAuthMode !== "oauth" ? (
              <div className="field">
                <FieldLabel required>{localizedMessage(isZh, "common.protocol")}</FieldLabel>
                <NyroSelect
                  ariaLabel={localizedMessage(isZh, "common.protocol")}
                  value={form.protocol}
                  options={createProtocolOptions}
                  onChange={(value) => {
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
                />
              </div>
            ) : null}
            {createResolvedAuthMode !== "oauth" ? (
              <label className="field">
                <FieldLabel required>Base URL</FieldLabel>
                <input
                  className="field-control"
                  placeholder={localizedMessage(isZh, "providers.eGHttpsApiOpenaiComV1")}
                  value={form.base_url}
                  onChange={(e) => setForm({ ...form, base_url: e.target.value })}
                />
              </label>
            ) : null}
            {isGlobalProxyEnabled && createResolvedAuthMode !== "oauth" ? (
              <div className="field full">
                <div className="switch-row">
                  <div className="switch-copy">
                    <strong>{localizedMessage(isZh, "providers.useLocalProxy")}</strong>
                    <span>{localizedMessage(isZh, "providers.routeRequestsViaTheLocalProxyFromSettings")}</span>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    className={form.use_proxy ? "switch-control on" : "switch-control"}
                    aria-checked={Boolean(form.use_proxy)}
                    onClick={() => setForm({ ...form, use_proxy: !form.use_proxy })}
                  />
                </div>
              </div>
            ) : null}
          </ProviderFormSection>
          <ProviderFormSection
            title={localizedMessage(isZh, "providers.credentials")}
            description={localizedMessage(isZh, "providers.authenticationValuesUsedToAccessTheUpstream")}
          >
          {showCreateOAuthGuide ? (
            <div className="field full">
              <div className="switch-inline">
                <span>
                  <strong>{localizedMessage(isZh, "providers.oauthAuthorization")}</strong>
                  <span className="field-hint">
                    {localizedMessage(isZh, "providers.followTheStepsBelowToFinishAuthorizationThisPanelCollaps")}
                  </span>
                </span>
                <span className="badge">
                  {createOAuthBusy
                    ? (localizedMessage(isZh, "providers.inProgress"))
                    : createOAuthStatus?.status === "error"
                      ? "Error"
                      : (localizedMessage(isZh, "providers.pending"))}
                </span>
              </div>
              <div className="form-grid">
                <div className="field">
                  <span className="field-label">
                    <span className="badge">1</span>
                    {localizedMessage(isZh, "providers.openAuthorizationPage")}
                  </span>
                  <span className="field-hint">
                    {localizedMessage(isZh, "providers.openTheBrowserAndCompleteSignInFirst")}
                  </span>
                  <div className="page-header-actions">
                    {!createOAuthSession ? (
                      <button
                        type="button"
                        className="button button-primary button-sm"
                        onClick={startCreateOAuth}
                        disabled={createOAuthBusy || !selectedPreset}
                      >
                        {createOAuthBusy ? (localizedMessage(isZh, "providers.initializing")) : (localizedMessage(isZh, "providers.startAuthorization"))}
                      </button>
                    ) : (
                      <>
                        <button type="button" className="button button-primary button-sm" onClick={reopenCreateOAuthPage}>
                          {localizedMessage(isZh, "providers.openAuthorizationPage")}
                        </button>
                        <button
                          type="button"
                          className="button button-secondary button-sm"
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
                          {createOAuthCopied ? (localizedMessage(isZh, "providers.copied")) : (localizedMessage(isZh, "providers.copyLink"))}
                        </button>
                      </>
                    )}
                  </div>
                  {createOAuthCopied ? (
                    <p className="field-hint success">
                      {localizedMessage(isZh, "providers.linkCopiedPleaseOpenItInYourBrowserAndCompleteTheAuthori")}
                    </p>
                  ) : null}
                  {createOAuthCopyFailed && createOAuthSession ? (
                    <div className="field">
                      <p className="field-hint error">{localizedMessage(isZh, "providers.copyFailedPleaseManuallyCopyTheLinkBelowAndOpenItInYourB")}</p>
                      <div className="code-block">
                        {createOAuthSession.auth_url || createOAuthSession.verification_uri_complete}
                      </div>
                    </div>
                  ) : null}
                </div>
                <div className="field">
                  <span className="field-label">
                    <span className="badge">2</span>
                    {localizedMessage(isZh, "providers.pasteCallbackResult")}
                  </span>
                  <span className="field-hint">
                    {localizedMessage(isZh, "providers.afterBrowserAuthorizationPasteTheCallbackURLOrAuthorizat")}
                  </span>
                  <label className="field">
                    <FieldLabel>{localizedMessage(isZh, "providers.callbackURL")}</FieldLabel>
                    <input
                      className="field-control"
                      placeholder={localizedMessage(isZh, "providers.forExampleHttpLocalhost1455AuthCallbackCode")}
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
                            const desc = parsed.searchParams.get("error_description") || parsed.searchParams.get("error") || localizedMessage(isZh, "providers.callbackContainsError");
                            setCreateOAuthCallbackError(localizedMessage(isZh, "providers.authorizationFailed", { a: desc }));
                          } else {
                            setCreateOAuthCallbackError(
                              localizedMessage(isZh, "providers.noCodeParameterFoundInURLPleaseConfirmYouPastedTheCorrec"),
                            );
                          }
                        } catch {
                          setCreateOAuthCallbackError(
                            localizedMessage(isZh, "providers.inputIsNotAValidURLPleasePasteTheCompleteCallbackURL"),
                          );
                        }
                      }}
                      disabled={!createOAuthSession || createOAuthBusy}
                    />
                    {createOAuthCallbackError ? <p className="field-hint error">{createOAuthCallbackError}</p> : null}
                  </label>
                  <label className="field">
                    <FieldLabel>{localizedMessage(isZh, "providers.authorizationCode")}</FieldLabel>
                    <input
                      className="field-control"
                      placeholder="code..."
                      value={createOAuthCode}
                      onChange={(e) => setCreateOAuthCode(e.target.value)}
                      disabled={!createOAuthSession || createOAuthBusy}
                    />
                  </label>
                  <div className="page-header-actions">
                    <button
                      type="button"
                      className="button button-primary button-sm"
                      onClick={completeCreateOAuth}
                      disabled={createOAuthBusy || !createOAuthSession || !!createOAuthCallbackError}
                    >
                      {createOAuthBusy ? (localizedMessage(isZh, "providers.submitting")) : (localizedMessage(isZh, "providers.submit"))}
                    </button>
                    <button
                      type="button"
                      className="button button-secondary button-sm"
                      onClick={cancelCreateOAuth}
                      disabled={!createOAuthSession}
                    >
                      {localizedMessage(isZh, "common.cancel")}
                    </button>
                  </div>
                </div>
              </div>
              {createOAuthStatus?.status === "error" ? (
                <p className="field-hint error">{createOAuthStatus.code}: {createOAuthStatus.message}</p>
              ) : createOAuthSession ? (
                <p className="field-hint">
                  {createOAuthRequiresManualCode
                    ? (localizedMessage(isZh, "providers.finishStep1ThenCompleteStep2"))
                    : (localizedMessage(isZh, "providers.waitingForBrowserAuthorizationToComplete"))}
                </p>
              ) : (
                <p className="field-hint">{localizedMessage(isZh, "providers.startWithStep1")}</p>
              )}
            </div>
          ) : null}
          {createResolvedAuthMode === "oauth" && createOAuthReady ? (
            <div className="field full alert alert-success">
              <div>
                <strong>{localizedMessage(isZh, "providers.oauthAuthorizationCompleted")}</strong>
                <span className="field-hint">
                  {localizedMessage(isZh, "providers.authorizationIsReadyContinueWithTheConfigurationBelowAnd")}
                </span>
              </div>
            </div>
          ) : null}
            {createResolvedAuthMode !== "oauth" ? (
              <div className={createUsesVertexServiceAccount ? "field full input-mono" : "field full"}>
                <FieldLabel
                  required
                  info={
                    createUsesVertexServiceAccount
                      ? (localizedMessage(isZh, "providers.pasteTheGoogleCloudServiceAccountJSONWithProjectIdClient"))
                      : undefined
                  }
                >
                  {createUsesVertexServiceAccount ? (localizedMessage(isZh, "providers.serviceAccountJSON")) : "API Key"}
                </FieldLabel>
                {createUsesVertexServiceAccount ? (
                  <textarea
                    placeholder={localizedMessage(isZh, "providers.projectIdClientEmailPrivateKeyBEGINPRIVATEKEYN")}
                    value={form.api_key}
                    rows={8}
                    className="field-control"
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                  />
                ) : (
                  <span className="field-control secret-control">
                    <input
                      placeholder="sk-..."
                      type={showCreateApiKey ? "text" : "password"}
                      value={form.api_key}
                      autoComplete="off"
                      aria-label="API Key"
                      onChange={(e) => setForm({ ...form, api_key: e.target.value })}
                    />
                    <button
                      type="button"
                      className="secret-toggle"
                      onClick={() => setShowCreateApiKey((prev) => !prev)}
                      data-tip={showCreateApiKey ? (localizedMessage(isZh, "providers.hideAPIKey")) : (localizedMessage(isZh, "providers.showAPIKey"))}
                      aria-label={showCreateApiKey ? (localizedMessage(isZh, "providers.hideAPIKey")) : (localizedMessage(isZh, "providers.showAPIKey"))}
                    >
                      {showCreateApiKey ? <EyeOff /> : <Eye />}
                    </button>
                  </span>
                )}
              </div>
            ) : null}
          </ProviderFormSection>
          {createResolvedAuthMode !== "oauth" ? (
            <ProviderFormSection
              title={localizedMessage(isZh, "providers.modelDiscovery")}
              description={localizedMessage(isZh, "providers.discoverModelsAutomaticallyOrMaintainAManualList")}
            >
              <ModelDiscoveryField
                isZh={isZh}
                mode={modelsMode}
                onMode={setModelsMode}
                source={form.models_source ?? ""}
                onSource={(value) => setForm({ ...form, models_source: value })}
                modelsText={form.static_models ?? ""}
                onModelsText={(value) => setForm({ ...form, static_models: value })}
              />
            </ProviderFormSection>
          ) : null}
        </div>
      </Drawer>

      {/* Edit form: right-side drawer */}
      <Drawer
        open={Boolean(editingProvider)}
        title={localizedMessage(isZh, "providers.editProvider")}
        description={editingProvider?.name}
        onClose={() => setEditingId(null)}
        footer={editingProvider ? (
          <>
            <button
              type="button"
              className="button button-secondary"
              onClick={() => { setEditingId(null); setEditError(null); }}
            >
              {localizedMessage(isZh, "common.cancel")}
            </button>
            <button
              type="button"
              className="button button-primary"
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
                  models_source: editingResolvedAuthMode === "oauth" || editModelsMode === "url" ? (editForm.models_source ?? "") : "",
                  static_models: editingResolvedAuthMode === "oauth" || editModelsMode === "static" ? (editForm.static_models || undefined) : undefined,
                  api_key: editForm.api_key || undefined,
                };
                updateMut.mutate({ id: editForm.id, ...input });
              }}
              disabled={updateMut.isPending || editRequiresNewOAuthProvider || reconnectOAuthMut.isPending || logoutOAuthMut.isPending}
            >
              {updateMut.isPending ? (localizedMessage(isZh, "common.saving")) : (localizedMessage(isZh, "common.save"))}
            </button>
          </>
        ) : undefined}
      >
        {editingProvider ? (
          <div className="add-provider-form">
            <ProviderFormSection plain>
            <div className="field full">
              <span className="field-label">
                {localizedMessage(isZh, "common.provider")}
                <span className="help" aria-label={localizedMessage(isZh, "providers.nonOAuthPresetsCanStillBeChangedWhileEditing")}>
                  ?
                  <span className="help-tip" role="tooltip">
                    {localizedMessage(isZh, "providers.oauthPresetsCannotBeSelectedWhileEditingCreateANewProvid")}
                  </span>
                </span>
              </span>
              <PresetPick
                presets={providerPresets}
                value={editingPresetId}
                isZh={isZh}
                onSelect={handleEditPresetChange}
                isItemDisabled={(preset) =>
                  presetChannelAuthMode(preset, preset.channels?.[0]?.id ?? "") === "oauth" && !currentProviderIsOAuth
                }
              />
            </div>
            {editingChannelOptions.length > 1 ? (
              <div className="field full">
                <span className="field-label">{localizedMessage(isZh, "providers.channel")}</span>
                <ChannelPick
                  channels={editingChannelOptions}
                  value={editingChannelValue}
                  isZh={isZh}
                  isItemDisabled={(channel) =>
                    presetChannelAuthMode(editingPreset, channel.id) === "oauth" && !currentProviderIsOAuth
                  }
                  onSelect={(value) => {
                    if (!value || !editingPreset?.channels?.length || !editingPreset) return;
                    const nextAuthMode = presetChannelAuthMode(editingPreset, value);
                    if (nextAuthMode === "oauth" && !currentProviderIsOAuth) {
                      setEditError(
                        localizedMessage(isZh, "providers.existingProvidersCannotSwitchDirectlyToAnOAuthChannelWhi"),
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
                    setEditModelsMode(pickModelsMode("url", config.modelsSource, config.staticModels));
                    setEditForm({
                      ...editForm,
                      channel: value,
                      protocol: resolvedProtocol,
                      base_url: baseUrl,
                      models_source: config.modelsSource,
                      static_models: config.staticModels,
                    });
                  }}
                />
              </div>
            ) : null}
            <label className="field">
              <FieldLabel required>{localizedMessage(isZh, "common.name")}</FieldLabel>
              <input
                className="field-control"
                placeholder={localizedMessage(isZh, "providers.eGOpenAIProduction")}
                value={editForm.name ?? ""}
                onChange={(e) => setEditForm({ ...editForm, name: e.target.value })}
              />
            </label>
            {editingResolvedAuthMode !== "oauth" ? (
              <div className="field">
                <FieldLabel required>{localizedMessage(isZh, "common.protocol")}</FieldLabel>
                <NyroSelect
                  ariaLabel={localizedMessage(isZh, "common.protocol")}
                  value={editForm.protocol ?? ""}
                  options={editingProtocolOptions}
                  onChange={(value) => {
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
                />
              </div>
            ) : null}
            {editingResolvedAuthMode !== "oauth" ? (
              <label className="field">
                <FieldLabel required>Base URL</FieldLabel>
                <input
                  className="field-control"
                  placeholder={localizedMessage(isZh, "providers.eGHttpsApiOpenaiComV1")}
                  value={editForm.base_url ?? ""}
                  onChange={(e) => setEditForm({ ...editForm, base_url: e.target.value })}
                />
              </label>
            ) : null}
            {isGlobalProxyEnabled && editingResolvedAuthMode !== "oauth" ? (
              <div className="field full">
                <div className="switch-row">
                  <div className="switch-copy">
                    <strong>{localizedMessage(isZh, "providers.useLocalProxy")}</strong>
                    <span>{localizedMessage(isZh, "providers.routeRequestsViaTheLocalProxyFromSettings")}</span>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    className={editForm.use_proxy ? "switch-control on" : "switch-control"}
                    aria-checked={Boolean(editForm.use_proxy)}
                    onClick={() => setEditForm({ ...editForm, use_proxy: !editForm.use_proxy })}
                  />
                </div>
              </div>
            ) : null}
            </ProviderFormSection>
            <ProviderFormSection
              title={localizedMessage(isZh, "providers.credentials")}
              description={localizedMessage(isZh, "providers.authenticationValuesUsedToAccessTheUpstream")}
            >
            {editingResolvedAuthMode === "oauth" ? (
              <details className="field full" open={editOAuthStatus?.status !== "connected"}>
                <summary className="switch-inline">
                  <span>
                    <strong>{localizedMessage(isZh, "providers.oauthAuthorization")}</strong>
                    {editOAuthStatus?.status === "connected" ? (
                      <span className="field-hint">{localizedMessage(isZh, "providers.clickToExpand")}</span>
                    ) : null}
                  </span>
                  <span className="badge">
                    {editOAuthStatusQuery.isLoading
                      ? (localizedMessage(isZh, "providers.loading"))
                      : editOAuthStatus?.status || (localizedMessage(isZh, "providers.unknown"))}
                  </span>
                </summary>
                {editRequiresNewOAuthProvider ? (
                  <div className="alert alert-warning">
                    <span>
                      {localizedMessage(isZh, "providers.existingProvidersCannotSwitchDirectlyToAnOAuthChannelWhi2")}
                      <button
                        type="button"
                        className="button button-sm"
                        onClick={() => {
                          setEditingId(null);
                          setShowForm(true);
                          handlePresetChange("openai");
                        }}
                      >
                        {localizedMessage(isZh, "providers.createNewOAuthProvider")}
                      </button>
                    </span>
                  </div>
                ) : editOAuthStatus?.status === "connected" ? (
                  <div className="form-stack">
                    <div className="alert alert-success">
                      <div>
                        <strong>{localizedMessage(isZh, "providers.authorizationStatus")}</strong>
                        <span className="field-hint">
                          {editOAuthStatus?.expires_at && new Date(editOAuthStatus.expires_at).getTime() < Date.now()
                            ? (localizedMessage(isZh, "providers.accessTokenHasExpiredPleaseRenew"))
                            : (localizedMessage(isZh, "providers.authorizationIsValidTheProviderIsReadyToUse"))}
                        </span>
                      </div>
                    </div>
                    <div className="page-header-actions">
                      <button
                        type="button"
                        className="button button-secondary button-sm"
                        onClick={() => reconnectOAuthMut.mutate(editingProvider.id)}
                        disabled={reconnectOAuthMut.isPending || logoutOAuthMut.isPending}
                      >
                        {reconnectOAuthMut.isPending ? (localizedMessage(isZh, "providers.renewing")) : (localizedMessage(isZh, "providers.renewToken"))}
                      </button>
                      <button
                        type="button"
                        className="button button-secondary button-sm"
                        onClick={() => logoutOAuthMut.mutate(editingProvider.id)}
                        disabled={logoutOAuthMut.isPending || reconnectOAuthMut.isPending}
                      >
                        {logoutOAuthMut.isPending ? (localizedMessage(isZh, "providers.disconnecting")) : (localizedMessage(isZh, "providers.disconnect"))}
                      </button>
                    </div>
                  </div>
                ) : (
                  <div className="form-stack">
                    <div className="alert alert-warning">
                      <div>
                        <strong>{localizedMessage(isZh, "providers.authorizationStatus")}</strong>
                        <span className="field-hint">
                          {editOAuthStatus?.status === "error"
                            ? (localizedMessage(isZh, "providers.authorizationFailedRefreshTokenMayHaveExpiredPleaseReAut"))
                            : (localizedMessage(isZh, "providers.notAuthorizedPleaseCompleteOAuthAuthorization"))}
                        </span>
                      </div>
                    </div>
                    {editOAuthStatus?.last_error ? (
                      <p className="field-hint error">{editOAuthStatus.last_error}</p>
                    ) : null}
                    {!showEditReauth ? (
                      <button
                        type="button"
                        className="button button-primary button-sm"
                        onClick={() => startEditReauth(editingProvider.id, editingProvider.vendor || editingProvider.preset_key || "", editingProvider.use_proxy)}
                      >
                        {localizedMessage(isZh, "providers.startAuthorization")}
                      </button>
                    ) : (
                      <div className="form-stack">
                        <div className="form-grid">
                          <div className="field">
                            <span className="field-label">
                              <span className="badge">1</span>
                              {localizedMessage(isZh, "providers.openAuthorizationPage")}
                            </span>
                            <span className="field-hint">
                              {localizedMessage(isZh, "providers.openTheBrowserAndCompleteSignInFirst")}
                            </span>
                            {editOAuthSession ? (
                              <div className="code-block">
                                <div>{localizedMessage(isZh, "providers.authorizationURL")}</div>
                                <div>{editOAuthSession.auth_url || editOAuthSession.verification_uri_complete}</div>
                              </div>
                            ) : null}
                          </div>
                          <div className="field">
                            <span className="field-label">
                              <span className="badge">2</span>
                              {localizedMessage(isZh, "providers.pasteCallbackResult")}
                            </span>
                            <span className="field-hint">
                              {localizedMessage(isZh, "providers.afterBrowserAuthorizationPasteTheCallbackURLOrAuthorizat")}
                            </span>
                            <label className="field">
                              <FieldLabel>{localizedMessage(isZh, "providers.callbackURL")}</FieldLabel>
                              <input
                                className="field-control"
                                placeholder={localizedMessage(isZh, "providers.forExampleHttpLocalhost1455AuthCallbackCode")}
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
                            </label>
                            <label className="field">
                              <FieldLabel>{localizedMessage(isZh, "providers.authorizationCode")}</FieldLabel>
                              <input
                                className="field-control"
                                placeholder="code..."
                                value={editOAuthCode}
                                onChange={(e) => setEditOAuthCode(e.target.value)}
                                disabled={!editOAuthSession || editOAuthBusy}
                              />
                            </label>
                            <div className="page-header-actions">
                              <button
                                type="button"
                                className="button button-primary button-sm"
                                onClick={() => completeEditReauth(editingProvider.id)}
                                disabled={editOAuthBusy || !editOAuthSession}
                              >
                                {editOAuthBusy ? (localizedMessage(isZh, "providers.submitting")) : (localizedMessage(isZh, "providers.submit"))}
                              </button>
                              <button
                                type="button"
                                className="button button-secondary button-sm"
                                onClick={() => { resetEditOAuthState(); setShowEditReauth(false); }}
                              >
                                {localizedMessage(isZh, "common.cancel")}
                              </button>
                            </div>
                          </div>
                        </div>
                        {editOAuthSessionStatus?.status === "error" ? (
                          <p className="field-hint error">{editOAuthSessionStatus.code}: {editOAuthSessionStatus.message}</p>
                        ) : editOAuthSession ? (
                          <p className="field-hint">
                            {editOAuthSession.requires_manual_code
                              ? (localizedMessage(isZh, "providers.finishStep1ThenCompleteStep2"))
                              : (localizedMessage(isZh, "providers.waitingForBrowserAuthorizationToComplete"))}
                          </p>
                        ) : null}
                      </div>
                    )}
                  </div>
                )}
              </details>
            ) : null}
              {editingResolvedAuthMode !== "oauth" ? (
                <div className={editUsesVertexServiceAccount ? "field full input-mono" : "field full"}>
                  <FieldLabel
                    info={
                      editUsesVertexServiceAccount
                        ? (localizedMessage(isZh, "providers.pasteTheGoogleCloudServiceAccountJSONWithProjectIdClient"))
                        : undefined
                    }
                  >
                    {editUsesVertexServiceAccount ? (localizedMessage(isZh, "providers.serviceAccountJSON")) : "API Key"}
                  </FieldLabel>
                  {editUsesVertexServiceAccount ? (
                    <textarea
                      placeholder={localizedMessage(isZh, "providers.projectIdClientEmailPrivateKeyBEGINPRIVATEKEYN")}
                      value={editForm.api_key ?? ""}
                      rows={8}
                      className="field-control"
                      autoCapitalize="none"
                      autoCorrect="off"
                      spellCheck={false}
                      onChange={(e) => setEditForm({ ...editForm, api_key: e.target.value })}
                    />
                  ) : (
                    <span className="field-control secret-control">
                      <input
                        placeholder="sk-..."
                        type={showEditApiKey ? "text" : "password"}
                        value={editForm.api_key ?? ""}
                        autoComplete="off"
                        aria-label="API Key"
                        onChange={(e) => setEditForm({ ...editForm, api_key: e.target.value })}
                      />
                      <button
                        type="button"
                        className="secret-toggle"
                        onClick={() => setShowEditApiKey((prev) => !prev)}
                        data-tip={showEditApiKey ? (localizedMessage(isZh, "providers.hideAPIKey")) : (localizedMessage(isZh, "providers.showAPIKey"))}
                        aria-label={showEditApiKey ? (localizedMessage(isZh, "providers.hideAPIKey")) : (localizedMessage(isZh, "providers.showAPIKey"))}
                      >
                        {showEditApiKey ? <EyeOff /> : <Eye />}
                      </button>
                    </span>
                  )}
                </div>
              ) : null}
            </ProviderFormSection>
            {editingResolvedAuthMode !== "oauth" ? (
              <ProviderFormSection
                title={localizedMessage(isZh, "providers.modelDiscovery")}
                description={localizedMessage(isZh, "providers.discoverModelsAutomaticallyOrMaintainAManualList")}
              >
                <ModelDiscoveryField
                  isZh={isZh}
                  mode={editModelsMode}
                  onMode={setEditModelsMode}
                  source={editForm.models_source ?? ""}
                  onSource={(value) => setEditForm({ ...editForm, models_source: value })}
                  modelsText={editForm.static_models ?? ""}
                  onModelsText={(value) => setEditForm({ ...editForm, static_models: value })}
                />
              </ProviderFormSection>
            ) : null}
            {editError ? <p className="provider-form-error">{editError}</p> : null}
          </div>
        ) : null}
      </Drawer>

      {/* Probe result: centered modal */}
      <Modal
        open={testDialogOpen}
        title={localizedMessage(isZh, "providers.test", { a: testTarget?.name ?? "" })}
        description={localizedMessage(isZh, "providers.realTimeLogsForProviderTesting")}
        onClose={closeTestDialog}
        footer={(
          <button type="button" className="button" onClick={closeTestDialog}>
            {isTestRunning
              ? (localizedMessage(isZh, "common.cancel"))
              : (localizedMessage(isZh, "common.close"))}
          </button>
        )}
      >
        <div className="probe-panel">
          <div className="code-output">
            <div className="code-output-bar">
              <span className="code-output-lang">{localizedMessage(isZh, "providers.probeLog")}</span>
              {isTestRunning ? <span className="code-output-meta">{localizedMessage(isZh, "providers.running")}</span> : null}
            </div>
            <div ref={logsContainerRef} className="code-output-body provider-test-log">
              {testLogs.length === 0 ? (
                <div className="provider-test-empty">{localizedMessage(isZh, "providers.waitingForTestToStart")}</div>
              ) : (
                testLogs.map((log, idx) => (
                  <p key={`${log.timestamp}-${idx}`} data-log-level={log.level}>
                    <time>{log.timestamp}</time>
                    <span>{log.message}</span>
                  </p>
                ))
              )}
            </div>
          </div>
        </div>
      </Modal>

      {/* Error details: centered modal */}
      <Modal
        open={Boolean(errorDialog)}
        title={errorDialog?.title ?? ""}
        onClose={() => setErrorDialog(null)}
        footer={(
          <button type="button" className="button button-primary" onClick={() => setErrorDialog(null)}>
            {localizedMessage(isZh, "common.ok")}
          </button>
        )}
      >
        {errorDialog?.description ? <p className="modal-desc">{errorDialog.description}</p> : null}
      </Modal>

      <ConfirmDialog
        open={Boolean(providerToDisable)}
        onOpenChange={(open) => {
          if (!open) setProviderToDisable(null);
        }}
        title={localizedMessage(isZh, "providers.confirmProviderDisable")}
        description={localizedMessage(isZh, "providers.afterDisablingModelRequestsReferencingThisProviderWillBe")}
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={localizedMessage(isZh, "common.disable")}
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
        title={localizedMessage(isZh, "providers.confirmProviderCopy")}
        description={
          providerToCopy
            ? (localizedMessage(isZh, "providers.copyAsDisabledByDefault", { a: providerToCopy.name, b: nextProviderCopyName(providers, providerToCopy.name) }))
            : undefined
        }
        content={(
          <label className="field">
            <span className="field-label">
              <input
                type="checkbox"
                checked={appendTargets}
                onChange={(event) => setAppendTargets(event.target.checked)}
                disabled={copyMut.isPending}
                aria-label={localizedMessage(isZh, "providers.appendModelTargets")}
              />
              {localizedMessage(isZh, "providers.appendModelTargets")}
            </span>
            <span className="field-hint">
              {localizedMessage(isZh, "providers.appendTargetsPointingToTheNewProviderInExistingModelsTha")}
            </span>
          </label>
        )}
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={copyMut.isPending ? (localizedMessage(isZh, "providers.copying")) : (localizedMessage(isZh, "providers.copy"))}
        confirmClassName="button-primary"
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
        title={localizedMessage(isZh, "providers.confirmProviderDeletion")}
        description={
          providerToDelete
            ? (localizedMessage(isZh, "common.thisActionCannotBeUndoneDelete", { a: providerToDelete.name }))
            : undefined
        }
        cancelText={localizedMessage(isZh, "common.cancel")}
        confirmText={localizedMessage(isZh, "common.delete")}
        onConfirm={() => {
          if (!providerToDelete) return;
          deleteMut.mutate(providerToDelete.id);
          setProviderToDelete(null);
        }}
      />
    </PageLayout>
  );
}
