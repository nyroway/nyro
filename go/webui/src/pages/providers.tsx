import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, type RefObject, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
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
  Plus,
  Trash2,
  Zap,
  Pencil,
  ChevronLeft,
  ChevronRight,
  Eye,
  EyeOff,
  Info,
  Route as RouteIcon,
  Search,
  RefreshCw,
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { FilterBar } from "@/components/v2/filter-bar";
import { Inspector } from "@/components/v2/inspector";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDialog } from "@/components/v2/resource-editor-dialog";
import { Status } from "@/components/v2/status";
import { Surface } from "@/components/v2/surface";
import { filterProviders, type ProviderFilters } from "@/features/providers/provider-view-model";
import { localizedMessage, type MessageKey } from "@/lib/messages";

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
  protocol: "openai-chatcompletions",
  base_url: "https://api.openai.com/v1",
  proxy_url: "",
  models_url: "",
  models: "",
  api_key: "",
  credentials: {},
};
const PAGE_SIZE = 7;
// Labels mirror go/llm/spec's Protocol.DisplayName(). TODO: fold into
// PROTOCOL_TABLE once the protocol table is served by the admin API.
const protocolOptions = [
  { label: "Anthropic Messages", value: "anthropic-messages" },
  { label: "OpenAI Chat Completions", value: "openai-chatcompletions" },
  { label: "OpenAI Responses", value: "openai-responses" },
  { label: "Gemini generateContent", value: "gemini-generatecontent" },
] as const satisfies ReadonlyArray<{ label: string; value: ProviderProtocol }>;

type ProviderDetailContentProps = {
  provider: Upstream;
  result?: TestResult;
  onTest: () => void;
  onImport: () => void;
  onEdit: () => void;
  onDelete: () => void;
};

export function ProviderDetailContent({
  provider,
  result,
  onTest,
  onImport,
  onEdit,
  onDelete,
}: ProviderDetailContentProps) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const healthLabel = !result
    ? localizedMessage(isZh, "v2.providers.notTested")
    : result.success
      ? localizedMessage(isZh, "v2.providers.healthy")
      : localizedMessage(isZh, "v2.providers.failed2");
  const healthTone = !result ? "neutral" : result.success ? "success" : "danger";

  return (
    <div className="v2-provider-detail">
      <div className="v2-provider-detail-summary">
        <div><span>{localizedMessage(isZh, "v2.providers.health")}</span><Status tone={healthTone}>{healthLabel}</Status></div>
        <div><span>{localizedMessage(isZh, "v2.providers.latency")}</span><strong>{result ? `${result.latency_ms}ms` : "—"}</strong></div>
        <div><span>{localizedMessage(isZh, "v2.providers.models")}</span><strong>{provider.models?.length ?? 0}</strong></div>
      </div>

      <section className="v2-provider-detail-section">
        <header>
          <h3>{localizedMessage(isZh, "v2.providers.connectionSummary")}</h3>
          <p>{localizedMessage(isZh, "v2.providers.connectionSummaryDetail")}</p>
        </header>
        <dl className="v2-provider-kv">
          <div><dt>{localizedMessage(isZh, "v2.providers.protocol")}</dt><dd>{protocolDisplayName(provider.protocol ?? "") ?? provider.protocol ?? "—"}</dd></div>
          <div><dt>Base URL</dt><dd><code>{provider.base_url || "—"}</code></dd></div>
          <div><dt>{localizedMessage(isZh, "v2.providers.credentials")}</dt><dd>{provider.credentials ? localizedMessage(isZh, "v2.providers.configured") : "—"}</dd></div>
          <div><dt>{localizedMessage(isZh, "v2.providers.modelDiscoveryAddress")}</dt><dd><code>{provider.models_url || "—"}</code></dd></div>
          <div><dt>{localizedMessage(isZh, "v2.providers.status")}</dt><dd>{provider.enabled ? localizedMessage(isZh, "v2.providers.enabled") : localizedMessage(isZh, "v2.providers.disabled")}</dd></div>
        </dl>
      </section>

      <div className="v2-provider-detail-actions">
        <button type="button" className="v2-button" onClick={onTest}><Zap />{localizedMessage(isZh, "v2.providers.testConnection")}</button>
        <button type="button" className="v2-button" onClick={onImport}><RouteIcon />{localizedMessage(isZh, "v2.providers.importModelRoutes")}</button>
        <button type="button" className="v2-button v2-button-primary" onClick={onEdit}><Pencil />{localizedMessage(isZh, "v2.providers.editConfiguration")}</button>
        <button type="button" className="v2-button v2-button-danger" onClick={onDelete}><Trash2 />{localizedMessage(isZh, "v2.providers.delete")}</button>
      </div>
    </div>
  );
}

export function ProviderFormSections({
  connection,
  credentials,
  discovery,
}: {
  connection: ReactNode;
  credentials: ReactNode;
  discovery: ReactNode;
}) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  return (
    <div className="v2-provider-form-sections">
      <ProviderFormSection
        name="connection"
        title={localizedMessage(isZh, "v2.providers.connection")}
        description={localizedMessage(isZh, "v2.providers.connectionFormDetail")}
      >
        {connection}
      </ProviderFormSection>
      <ProviderFormSection
        name="credentials"
        title={localizedMessage(isZh, "v2.providers.credentials")}
        description={localizedMessage(isZh, "v2.providers.credentialsFormDetail")}
      >
        {credentials}
      </ProviderFormSection>
      <ProviderFormSection
        name="discovery"
        title={localizedMessage(isZh, "v2.providers.modelDiscoveryAddress")}
        description={localizedMessage(isZh, "v2.providers.discoveryFormDetail")}
      >
        {discovery}
      </ProviderFormSection>
    </div>
  );
}

function ProviderFormSection({
  name,
  title,
  description,
  children,
}: {
  name: "connection" | "credentials" | "discovery";
  title: string;
  description: string;
  children: ReactNode;
}) {
  return (
    <section className="v2-provider-form-section" data-provider-form-section={name}>
      <header><h3>{title}</h3><p>{description}</p></header>
      <div className="v2-provider-form-grid">{children}</div>
    </section>
  );
}

function validateProviderEndpoint(
  protocol: string | undefined,
  baseUrl: string | undefined,
  isZh: boolean,
): string | null {
  if (!protocol?.trim()) {
    return localizedMessage(isZh, "v2.providers.protocolIsRequired");
  }
  const trimmed = baseUrl?.trim() ?? "";
  if (!trimmed) {
    return localizedMessage(isZh, "v2.providers.baseUrlIsRequired");
  }
  try {
    new URL(trimmed);
  } catch {
    return localizedMessage(isZh, "providers.invalidBaseURL", { url: baseUrl ?? "" });
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
  const canonicalDefault = (resolveProtocol(preset.defaultProtocol) ?? "openai-chatcompletions") as ProviderProtocol;
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
    ? (localizedMessage(isZh, "v2.providers.eGSk"))
    : localizedMessage(isZh, "providers.enterCredential", { label });

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
          placeholder={localizedMessage(isZh, "v2.providers.pasteJsonContent")}
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
            aria-label={reveal ? (localizedMessage(isZh, "v2.providers.hide")) : (localizedMessage(isZh, "v2.providers.show"))}
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

export function ProviderTestLog({
  logs,
  emptyLabel,
  containerRef,
}: {
  logs: TestLogEntry[];
  emptyLabel: string;
  containerRef?: RefObject<HTMLDivElement | null>;
}) {
  return (
    <div ref={containerRef} className="v2-provider-test-log">
      {logs.length === 0
        ? <p className="v2-provider-test-empty">{emptyLabel}</p>
        : logs.map((log, index) => (
          <p key={`${log.timestamp}-${index}`} data-log-level={log.level}>
            <time>{log.timestamp}</time><span>{log.message}</span>
          </p>
        ))}
    </div>
  );
}

export function RouteImportSummary({ preview }: { preview: RouteImportPreview }) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  return (
    <div className="v2-provider-import-summary">
      <div className="v2-provider-import-metrics">
        <div><span>{localizedMessage(isZh, "v2.providers.discovered")}</span><strong>{preview.discovered}</strong></div>
        <div><span>{localizedMessage(isZh, "v2.providers.create")}</span><strong>{preview.create.length}</strong></div>
        <div><span>{localizedMessage(isZh, "v2.providers.skip")}</span><strong>{preview.skip.length}</strong></div>
      </div>
      <section>
        <h4>{localizedMessage(isZh, "v2.providers.routesToCreate")}</h4>
        <div className="v2-provider-import-routes">
          {preview.create.slice(0, 8).map((model) => <code key={model}>{model}</code>)}
          {preview.create.length > 8 && <span>+{preview.create.length - 8}</span>}
          {preview.create.length === 0 && <small>{localizedMessage(isZh, "v2.providers.noRoutesNeedToBeCreated")}</small>}
        </div>
      </section>
      {preview.skip.length > 0 && <p>{localizedMessage(isZh, "providers.importSkipped", { count: preview.skip.length })}</p>}
    </div>
  );
}

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
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const location = useLocation();
  const navigate = useNavigate();

  const qc = useQueryClient();
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [selectedProviderId, setSelectedProviderId] = useState<string | null>(null);
  const [page, setPage] = useState(0);
  const [filters, setFilters] = useState<ProviderFilters>({ query: "", protocol: "all", enabled: "all" });
  const [, setTestingId] = useState<string | null>(null);
  const [, setRouteImportingId] = useState<string | null>(null);
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
  const selectedProvider = providers.find((provider) => provider.id === selectedProviderId) ?? null;
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
      showErrorDialog("providers.error.create", error);
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
      showErrorDialog("providers.error.save", err);
    },
  });

  const deleteMut = useMutation({
    mutationFn: (id: string) => backend("delete_upstream", { id }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("providers.error.delete", error);
    },
  });

  const [providerToDisable, setProviderToDisable] = useState<Upstream | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      backend("update_upstream", { id, input: { enabled: is_enabled } }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("providers.error.operation", error);
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
      appendTestLog("info", localizedMessage(isZh, "providers.startTest", { name: provider.name }));
      appendTestLog("info", localizedMessage(isZh, "v2.providers.aMinimalUpstreamModelRequestWillBeSent"));

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
        const message = localizedMessage(isZh, "v2.providers.healthCheckDidNotReturnACompletionEvent");
        appendTestLog("error", `✗ ${message}`);
        finish({ success: false, latency_ms: modelResult.latency_ms, model: modelResult.model, error: message });
      }
    } catch (error: unknown) {
      if (isCanceled() || abortController.signal.aborted) return;
      const message = normalizeErrorMessage(error);
      appendTestLog("error", `${localizedMessage(isZh, "v2.providers.testFailed")}: ${message}`);
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
        return localizedMessage(isZh, "v2.providers.configurationValidation");
      case "credentials":
        return localizedMessage(isZh, "v2.providers.credentialValidation");
      case "models":
        return localizedMessage(isZh, "v2.providers.modelDiscovery");
      case "model_request":
        return localizedMessage(isZh, "v2.providers.modelRequestTest");
      default:
        return localizedMessage(isZh, "v2.providers.healthCheck");
    }
  }

  function routeImportStageName(stage: RouteImportEvent["stage"]) {
    switch (stage) {
      case "models":
        return localizedMessage(isZh, "v2.providers.modelDiscovery");
      case "creating":
        return localizedMessage(isZh, "v2.providers.routeImport");
      default:
        return localizedMessage(isZh, "v2.providers.import");
    }
  }

  function appendHealthEvent(event: ProviderHealthEvent, mode: "provider" | "create" | "edit" = "provider") {
    if (event.type === "complete") {
      appendTestLog(
        event.success ? "success" : "error",
        event.success
          ? (mode === "create"
            ? (localizedMessage(isZh, "v2.providers.allChecksPassedClickCreateProviderToFinish"))
            : mode === "edit"
              ? (localizedMessage(isZh, "v2.providers.allChecksPassedClickSaveProviderToFinish"))
              : (localizedMessage(isZh, "v2.providers.allChecksPassed")))
          : `${localizedMessage(isZh, "v2.providers.checksFailed")}${event.error ? `: ${event.error}` : ""}`,
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
      if (event.check === "models" && event.models && event.models.length > 0) {
        appendTestLog("success", `✓ ${name} (${localizedMessage(isZh, "providers.modelsFound", { count: event.models.length })})`);
        for (const model of event.models) {
          appendTestLog("info", `    - ${model}`);
        }
        return;
      }
      appendTestLog(
        "success",
        `✓ ${name}${event.model ? ` (${event.model})` : ""}${latency}`,
      );
      return;
    }
    if (event.status === "failed") {
      appendTestLog("error", `✗ ${name}: ${event.error ?? event.message ?? (localizedMessage(isZh, "v2.providers.failed"))}`);
    }
  }

  function appendRouteImportEvent(event: RouteImportEvent) {
    if (event.type === "complete") {
      const summary = localizedMessage(isZh, "providers.importSummary", {
        discovered: event.discovered ?? 0,
        created: event.created ?? 0,
        skipped: event.skipped ?? 0,
        failed: event.failed ?? 0,
      });
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
        appendTestLog("error", `✗ ${name}: ${event.error ?? event.message ?? (localizedMessage(isZh, "v2.providers.failed"))}`);
      }
      return;
    }
    if (event.type === "route") {
      if (event.status === "created") {
        appendTestLog("success", `✓ ${event.model ?? ""}`);
      } else if (event.status === "skipped") {
        appendTestLog("info", `- ${event.model ?? ""} ${localizedMessage(isZh, "v2.providers.alreadyExistsSkipped")}`);
      } else if (event.status === "failed") {
        appendTestLog("error", `✗ ${event.model ?? ""}: ${event.error ?? event.reason ?? (localizedMessage(isZh, "v2.providers.failed"))}`);
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

    appendTestLog("info", localizedMessage(isZh, "providers.startImport", { name: provider.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.existingRoutesWithTheSameNameAreSkipped"));

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
      appendTestLog("error", `${localizedMessage(isZh, "v2.providers.importFailed")}: ${message}`);
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
      showErrorDialog("providers.error.previewImport", error);
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

    appendTestLog("info", localizedMessage(isZh, "providers.startPreCreate", { name: input.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.aMinimalUpstreamModelRequestWillBeSent"));

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
      appendTestLog("error", `${localizedMessage(isZh, "v2.providers.streamingHealthCheckFailed")}: ${message}`);
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

    appendTestLog("info", localizedMessage(isZh, "providers.startPreSave", { name: draft.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.aMinimalUpstreamModelRequestWillBeSent"));

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
      appendTestLog("error", `${localizedMessage(isZh, "v2.providers.streamingHealthCheckFailed")}: ${message}`);
      setEditHealthPassed(false);
      setIsTestRunning(false);
    } finally {
      if (!isCanceled()) {
        activeTestAbortRef.current = null;
      }
    }
  }

  const startEdit = useCallback((p: Upstream) => {
    setEditingId(p.id);
    setEditError(null);
    const protocol = (resolveProtocol(p.protocol) ?? "openai-chatcompletions") as ProviderProtocol;
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
  }, [providerPresets]);

  useEffect(() => {
    const params = new URLSearchParams(location.search);
    if (params.get("action") === "create") {
      setEditingId(null);
      setShowForm(true);
      navigate(location.pathname, { replace: true });
      return;
    }
    const focus = params.get("focus");
    if (!focus || providerPresets.length === 0) return;
    const provider = providers.find((item) => item.id === focus);
    if (!provider) return;
    setPage(Math.floor(providers.findIndex((item) => item.id === focus) / PAGE_SIZE));
    startEdit(provider);
    navigate(location.pathname, { replace: true });
  }, [location.pathname, location.search, navigate, providerPresets.length, providers, startEdit]);

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
    // handleTemplateChange is intentionally omitted: this effect is keyed by
    // the preset snapshot and selection, and only calls it to apply fallback.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providerPresets, selectedPresetId]);

  function closeCreateForm() {
    setShowForm(false);
    setSelectedPresetId("");
    setModelsMode("url");
    setForm(emptyCreate);
  }

  const filteredProviders = useMemo(() => filterProviders(providers, filters), [filters, providers]);
  const totalPages = Math.max(1, Math.ceil(filteredProviders.length / PAGE_SIZE));
  const pagedProviders = filteredProviders.slice(page * PAGE_SIZE, page * PAGE_SIZE + PAGE_SIZE);
  const createCredentialFields = credentialFieldsForPreset(selectedPreset);
  const createCredentialLayout = splitApiKeyCredentialField(createCredentialFields);
  const createPresetBaseUrl = selectedPreset
    ? resolvePresetConfig(selectedPreset, (form.protocol as ProviderProtocol) || "openai-chatcompletions").baseUrl
    : "";
  const createBaseUrlMissing = !createPresetBaseUrl && !form.base_url?.trim();
  const createProtocolOptions = availableProtocolsForPreset(selectedPreset);

  useEffect(() => {
    if (page > totalPages - 1) {
      setPage(0);
    }
  }, [page, totalPages]);

  useEffect(() => {
    setPage(0);
  }, [filters]);

  useEffect(() => {
    if (!logsContainerRef.current) return;
    logsContainerRef.current.scrollTop = logsContainerRef.current.scrollHeight;
  }, [testLogs]);

  // Auto-grow the manual model-list textareas to fit their content (see
  // autoGrowTextarea) — re-measured whenever the text changes (typing,
  // preset/protocol fill-in) or the segmented control switches into
  // "static"/manual mode (the edit form's textarea isn't in the DOM at all
  // while in "url"/auto mode, so it needs re-measuring the moment it mounts).
  // showForm/editingId are also deps: closing and reopening the same
  // provider leaves form.models/editForm.models textually unchanged, so
  // without them the effect would see no dependency change and skip
  // re-measuring the freshly (re)mounted textarea, leaving it at its
  // default unsized height.
  useLayoutEffect(() => {
    autoGrowTextarea(modelsTextareaRef.current);
  }, [form.models, modelsMode, showForm]);

  useLayoutEffect(() => {
    autoGrowTextarea(editModelsTextareaRef.current);
  }, [editForm.models, editModelsMode, editingId]);

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

  const providerColumns: DataTableColumn<Upstream>[] = [
    {
      key: "provider",
      header: localizedMessage(isZh, "v2.providers.provider"),
      render: (provider) => {
        const preset = providerPresets.find((item) => item.id === (provider.provider || ""));
        return (
          <div className="v2-provider-cell">
            <ProviderIcon
              iconKey={preset?.icon}
              name={provider.name}
              protocol={provider.protocol}
              baseUrl={provider.base_url}
              size={28}
              className="v2-provider-icon"
            />
            <div><strong>{provider.name}</strong><span>{provider.provider || "custom"}</span></div>
          </div>
        );
      },
    },
    {
      key: "protocol",
      header: localizedMessage(isZh, "v2.providers.protocol"),
      render: (provider) => (
        <code className="v2-code-pill">{protocolDisplayName(provider.protocol ?? "") ?? provider.protocol ?? "—"}</code>
      ),
    },
    {
      key: "connection",
      header: localizedMessage(isZh, "v2.providers.connection"),
      render: (provider) => (
        <div className="v2-connection-cell">
          <span title={provider.base_url}>{provider.base_url || "—"}</span>
          {provider.proxy_url ? <small>{localizedMessage(isZh, "v2.providers.viaProxy")}</small> : null}
        </div>
      ),
    },
    {
      key: "models",
      header: localizedMessage(isZh, "v2.providers.models"),
      className: "v2-table-number",
      render: (provider) => provider.models?.length ?? 0,
    },
    {
      key: "health",
      header: localizedMessage(isZh, "v2.providers.health"),
      render: (provider) => {
        const result = testResult[provider.id];
        if (!result) return <Status tone="neutral">{localizedMessage(isZh, "v2.providers.notTested")}</Status>;
        return result.success
          ? <Status tone="success">{localizedMessage(isZh, "v2.providers.healthy")}</Status>
          : <Status tone="danger">{localizedMessage(isZh, "v2.providers.failed2")}</Status>;
      },
    },
    {
      key: "actions",
      header: localizedMessage(isZh, "v2.providers.status"),
      className: "v2-table-toggle",
      render: (provider) => (
        <button
          type="button"
          role="switch"
          aria-checked={provider.enabled}
          className="v2-provider-toggle"
          data-enabled={provider.enabled}
          onClick={(event) => {
            event.stopPropagation();
            if (provider.enabled) setProviderToDisable(provider);
            else toggleEnabledMut.mutate({ id: provider.id, is_enabled: true });
          }}
          title={provider.enabled ? localizedMessage(isZh, "v2.providers.disable") : localizedMessage(isZh, "v2.providers.enable")}
        >
          <span />
        </button>
      ),
    },
  ];

  return (
    <PageLayout
      header={(
        <PageHeader
          title={t("page.providers.title")}
          description={t("page.providers.subtitle")}
          actions={(
            <Button
              onClick={() => {
                setEditingId(null);
                setShowForm(true);
                setSelectedPresetId("");
                setModelsMode("url");
                setForm(emptyCreate);
              }}
            >
              <Plus className="h-4 w-4" />
              {localizedMessage(isZh, "v2.providers.addProvider")}
            </Button>
          )}
        />
      )}
    >

      {/* Create Form */}
      <ResourceEditorDialog
        open={showForm}
        title={localizedMessage(isZh, "v2.providers.newProvider")}
        description={localizedMessage(isZh, "v2.providers.configureConnectionCredentialsAndModelDiscovery")}
        onClose={closeCreateForm}
        footer={(
          <div className="v2-inspector-footer-actions">
            <Button onClick={closeCreateForm} variant="secondary">
              {localizedMessage(isZh, "v2.providers.cancel")}
            </Button>
            <Button
              onClick={() => {
                const protocol = form.protocol || "openai-chatcompletions";
                const baseUrl = toGatewayBaseUrl(form.base_url ?? "");
                const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                if (validation) {
                  setErrorDialog({
                    title: localizedMessage(isZh, "v2.providers.failedToCreateProvider"),
                    description: validation,
                  });
                  return;
                }
                const input: CreateUpstream = buildCreateUpstreamInput({
                  ...form,
                  protocol,
                  base_url: baseUrl,
                  models_url: modelsMode === "url" ? form.models_url : "",
                  models: modelsMode === "static" ? form.models : "",
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
                ? localizedMessage(isZh, "v2.providers.testing")
                : localizedMessage(isZh, "v2.providers.testCreate")}
            </Button>
          </div>
        )}
      >
        <ProviderFormSections
          connection={(
            <>
              <div className="v2-field-span-2">
                <FieldLabel required>{localizedMessage(isZh, "v2.providers.provider")}</FieldLabel>
                <ToggleGroup type="single" value={selectedPresetId} onValueChange={(value) => { if (value) handleTemplateChange(value); }} className="provider-preset-group">
                  {providerPresets.map((preset) => (
                    <ToggleGroupItem key={preset.id} value={preset.id} variant="outline" size="lg" className="provider-preset-card" aria-label={presetLabel(preset)}>
                      <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={24} className="provider-preset-icon provider-preset-icon-colored" />
                      <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={24} monochrome className="provider-preset-icon provider-preset-icon-mono" />
                      <span className={presetLabelClass(preset)}>{presetLabel(preset)}</span>
                    </ToggleGroupItem>
                  ))}
                </ToggleGroup>
              </div>
              <div><FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGOpenaiProduction")} value={form.name} onChange={(event) => setForm({ ...form, name: event.target.value })} /></div>
              <div><FieldLabel required>{localizedMessage(isZh, "v2.providers.protocol")}</FieldLabel><Select value={form.protocol} onValueChange={handleProtocolChange}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{createProtocolOptions.map((protocol) => <SelectItem key={protocol} value={protocol}>{protocolDisplayName(protocol) ?? protocol}</SelectItem>)}</SelectContent></Select></div>
              <div><FieldLabel required>Base URL</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1")} value={form.base_url} onChange={(event) => setForm({ ...form, base_url: event.target.value })} /></div>
              <div><FieldLabel>{localizedMessage(isZh, "v2.providers.proxyUrl")}</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGHttp1270017890")} value={form.proxy_url ?? ""} onChange={(event) => setForm({ ...form, proxy_url: event.target.value })} /></div>
            </>
          )}
          credentials={(
            <>
              {createCredentialLayout.apiKeyField && <CredentialFieldInput field={createCredentialLayout.apiKeyField} value={form.credentials?.[createCredentialLayout.apiKeyField.name] ?? ""} onChange={(value) => setForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [createCredentialLayout.apiKeyField!.name]: value } }))} isZh={isZh} />}
              {createCredentialLayout.otherFields.map((field) => <CredentialFieldInput key={field.name} field={field} value={form.credentials?.[field.name] ?? ""} onChange={(value) => setForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [field.name]: value } }))} isZh={isZh} />)}
            </>
          )}
          discovery={(
            <div className="v2-field-span-2">
              <FieldLabel required info={localizedMessage(isZh, "v2.providers.usedToAutoFetchAvailableModelListWhen")}>{localizedMessage(isZh, "v2.providers.modelDiscovery2")}</FieldLabel>
              {modelsMode === "url" ? <Input placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1Models")} value={form.models_url ?? ""} onChange={(event) => setForm({ ...form, models_url: event.target.value })} /> : <textarea ref={modelsTextareaRef} rows={1} className="model-textarea nyro-shadcn-input flex min-h-[40px] w-full resize-none overflow-hidden rounded-md border border-border bg-background px-3 text-sm text-foreground transition-[border-color,background-color,color] outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300 disabled:cursor-not-allowed disabled:opacity-50" placeholder={localizedMessage(isZh, "v2.providers.oneModelPerLineEGGpt4o")} value={form.models ?? ""} onChange={(event) => { setForm({ ...form, models: event.target.value }); autoGrowTextarea(event.target); }} />}
              <ToggleGroup type="single" value={modelsMode} onValueChange={(value) => { if (value) setModelsMode(value as ModelsMode); }} className="provider-region-group"><ToggleGroupItem value="url" variant="outline" size="sm">{localizedMessage(isZh, "v2.providers.autoDiscovery")}</ToggleGroupItem><ToggleGroupItem value="static" variant="outline" size="sm">{localizedMessage(isZh, "v2.providers.manualEntry")}</ToggleGroupItem></ToggleGroup>
            </div>
          )}
        />
      </ResourceEditorDialog>

      <FilterBar summary={localizedMessage(isZh, "common.showing", { visible: filteredProviders.length, total: providers.length })}>
        <label className="v2-search-field">
          <Search aria-hidden="true" />
          <Input
            aria-label={localizedMessage(isZh, "v2.providers.searchProviders")}
            placeholder={localizedMessage(isZh, "v2.providers.searchNameProtocolOrEndpoint")}
            value={filters.query}
            onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
          />
        </label>
        <Select value={filters.protocol} onValueChange={(protocol) => setFilters((current) => ({ ...current, protocol }))}>
          <SelectTrigger aria-label={localizedMessage(isZh, "v2.providers.filterByProtocol")}><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{localizedMessage(isZh, "v2.providers.allProtocols")}</SelectItem>
            {protocolOptions.map((protocol) => <SelectItem key={protocol.value} value={protocol.value}>{protocol.label}</SelectItem>)}
          </SelectContent>
        </Select>
        <Select
          value={filters.enabled}
          onValueChange={(enabled) => setFilters((current) => ({ ...current, enabled: enabled as ProviderFilters["enabled"] }))}
        >
          <SelectTrigger aria-label={localizedMessage(isZh, "v2.providers.filterByStatus")}><SelectValue /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{localizedMessage(isZh, "v2.providers.allStatuses")}</SelectItem>
            <SelectItem value="enabled">{localizedMessage(isZh, "v2.providers.enabled")}</SelectItem>
            <SelectItem value="disabled">{localizedMessage(isZh, "v2.providers.disabled")}</SelectItem>
          </SelectContent>
        </Select>
        <button
          type="button"
          className="v2-button v2-filter-refresh"
          onClick={() => void qc.invalidateQueries({ queryKey: ["providers"] })}
        >
          <RefreshCw aria-hidden="true" />
          {localizedMessage(isZh, "v2.providers.refreshStatus")}
        </button>
      </FilterBar>

      <Surface
        className="v2-table-surface"
        title={localizedMessage(isZh, "v2.providers.upstreamProviders")}
        description={localizedMessage(isZh, "v2.providers.upstreamProvidersDetail")}
        actions={(
          <Status tone="success">
            {localizedMessage(isZh, "v2.providers.availableCount", {
              count: filteredProviders.filter((provider) => provider.enabled && testResult[provider.id]?.success).length,
            })}
          </Status>
        )}
        footer={(
          <>
            <span>{localizedMessage(isZh, "v2.providers.healthStatusNotice")}</span>
            <span>{localizedMessage(isZh, "v2.providers.credentialsPlaintextNotice")}</span>
          </>
        )}
      >
        <DataTable
          columns={providerColumns}
          rows={pagedProviders}
          rowKey={(provider) => provider.id}
          onRowClick={(provider) => setSelectedProviderId(provider.id)}
          loading={isLoading}
          empty={(
            <EmptyState
              title={providers.length === 0
                ? (localizedMessage(isZh, "v2.providers.noProvidersConfigured"))
                : (localizedMessage(isZh, "v2.providers.noProvidersMatchTheseFilters"))}
              description={providers.length === 0
                ? (localizedMessage(isZh, "v2.providers.addYourFirstModelServiceConnectionToStart"))
                : (localizedMessage(isZh, "v2.providers.tryAdjustingTheSearchOrFilters"))}
              action={providers.length === 0 ? <Button onClick={() => setShowForm(true)}><Plus />{localizedMessage(isZh, "v2.providers.addProvider")}</Button> : undefined}
            />
          )}
        />
        {filteredProviders.length > PAGE_SIZE && (
          <div className="v2-pagination">
            <span>{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span>
            <div>
              <Button onClick={() => setPage(Math.max(0, page - 1))} disabled={page === 0} variant="outline" size="icon"><ChevronLeft /></Button>
              <Button onClick={() => setPage(Math.min(totalPages - 1, page + 1))} disabled={page >= totalPages - 1} variant="outline" size="icon"><ChevronRight /></Button>
            </div>
          </div>
        )}
      </Surface>

      <Inspector
        open={Boolean(selectedProvider)}
        title={selectedProvider?.name ?? localizedMessage(isZh, "v2.providers.providerDetails")}
        description={selectedProvider?.id}
        onClose={() => setSelectedProviderId(null)}
      >
        {selectedProvider && (
          <ProviderDetailContent
            provider={selectedProvider}
            result={testResult[selectedProvider.id]}
            onTest={() => {
              setSelectedProviderId(null);
              void handleTest(selectedProvider);
            }}
            onImport={() => {
              setSelectedProviderId(null);
              void handlePreviewRouteImport(selectedProvider);
            }}
            onEdit={() => {
              setSelectedProviderId(null);
              startEdit(selectedProvider);
            }}
            onDelete={() => {
              setSelectedProviderId(null);
              setProviderToDelete(selectedProvider);
            }}
          />
        )}
      </Inspector>

      {pagedProviders.map((provider) => {
        if (editingId !== provider.id) return null;
        const editingPresetId = editForm.provider ?? "";
        const editingPreset = editingPresetId ? providerPresets.find((preset) => preset.id === editingPresetId) ?? null : null;
        const editCredentialFields = credentialFieldsForPreset(editingPreset);
        const editCredentialLayout = splitApiKeyCredentialField(editCredentialFields);
        const editPresetBaseUrl = editingPreset ? resolvePresetConfig(editingPreset, (editForm.protocol as ProviderProtocol) || "openai-chatcompletions").baseUrl : "";
        const editBaseUrlMissing = !editPresetBaseUrl && !editForm.base_url?.trim();
        const editProtocolOptions = availableProtocolsForPreset(editingPreset);
        const editLockedPresets = editingPreset ? [editingPreset] : providerPresets;

        return (
          <ResourceEditorDialog
            key={provider.id}
            open
            title={localizedMessage(isZh, "v2.providers.editProvider")}
            description={provider.name}
            onClose={() => { setEditingId(null); setEditError(null); }}
            footer={(
              <div className="v2-inspector-footer-actions">
                <Button onClick={() => { setEditingId(null); setEditError(null); }} variant="secondary">{localizedMessage(isZh, "v2.providers.cancel")}</Button>
                <Button
                  onClick={() => {
                    setEditError(null);
                    const protocol = editForm.protocol || "openai-chatcompletions";
                    const baseUrl = toGatewayBaseUrl(editForm.base_url ?? "");
                    const validation = validateProviderEndpoint(protocol, baseUrl, isZh);
                    if (validation) { setEditError(validation); return; }
                    const editModelsUrl = editModelsMode === "url" ? (editForm.models_url ?? "") : "";
                    const editModels = editModelsMode === "static" ? (editForm.models ?? "") : "";
                    const update: UpdateUpstream = buildUpdateUpstreamInput({ name: editForm.name || undefined, provider: editForm.provider || undefined, protocol, base_url: baseUrl, proxy_url: editForm.proxy_url ?? "", models_url: editModelsUrl, models: editModels, credentials: editForm.credentials && Object.keys(editForm.credentials).length ? editForm.credentials : undefined });
                    const draft: CreateUpstream = buildCreateUpstreamInput({ name: editForm.name ?? "", provider: editForm.provider || "custom", protocol, base_url: baseUrl, proxy_url: editForm.proxy_url ?? "", models_url: editModelsUrl, models: editModels, api_key: editForm.api_key ?? "", credentials: editForm.credentials ?? {} });
                    void handleUpdateHealthCheck(draft, { id: editForm.id, ...update });
                  }}
                  disabled={updateMut.isPending || isTestRunning || missingRequiredCredentials(editCredentialFields, editForm.credentials ?? {}) || editBaseUrlMissing}
                >
                  {isTestRunning ? localizedMessage(isZh, "v2.providers.testing") : localizedMessage(isZh, "v2.providers.testSave")}
                </Button>
              </div>
            )}
          >
            <ProviderFormSections
              connection={(
                <>
                  <div className="v2-field-span-2">
                    <FieldLabel info={localizedMessage(isZh, "v2.providers.theProviderPresetCanTBeChangedAfter")}>{localizedMessage(isZh, "v2.providers.provider")}</FieldLabel>
                    <ToggleGroup type="single" value={editingPresetId} className="provider-preset-group">
                      {editLockedPresets.map((preset) => (
                        <ToggleGroupItem key={preset.id} value={preset.id} variant="outline" size="lg" disabled className="provider-preset-card" aria-label={presetLabel(preset)}>
                          <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={24} className="provider-preset-icon provider-preset-icon-colored" />
                          <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={24} monochrome className="provider-preset-icon provider-preset-icon-mono" />
                          <span className={presetLabelClass(preset)}>{presetLabel(preset)}</span>
                        </ToggleGroupItem>
                      ))}
                    </ToggleGroup>
                  </div>
                  <div><FieldLabel required>{localizedMessage(isZh, "v2.providers.name")}</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGOpenaiProduction")} value={editForm.name ?? ""} onChange={(event) => setEditForm({ ...editForm, name: event.target.value })} /></div>
                  <div><FieldLabel required>{localizedMessage(isZh, "v2.providers.protocol")}</FieldLabel><Select value={editForm.protocol ?? ""} onValueChange={handleEditProtocolChange}><SelectTrigger><SelectValue /></SelectTrigger><SelectContent>{editProtocolOptions.map((protocol) => <SelectItem key={protocol} value={protocol}>{protocolDisplayName(protocol) ?? protocol}</SelectItem>)}</SelectContent></Select></div>
                  <div><FieldLabel required>Base URL</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1")} value={editForm.base_url ?? ""} onChange={(event) => setEditForm({ ...editForm, base_url: event.target.value })} /></div>
                  <div><FieldLabel>{localizedMessage(isZh, "v2.providers.proxyUrl")}</FieldLabel><Input placeholder={localizedMessage(isZh, "v2.providers.eGHttp1270017890")} value={editForm.proxy_url ?? ""} onChange={(event) => setEditForm({ ...editForm, proxy_url: event.target.value })} /></div>
                </>
              )}
              credentials={(
                <>
                  {editCredentialLayout.apiKeyField && <CredentialFieldInput field={editCredentialLayout.apiKeyField} value={editForm.credentials?.[editCredentialLayout.apiKeyField.name] ?? ""} onChange={(value) => setEditForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [editCredentialLayout.apiKeyField!.name]: value } }))} isZh={isZh} />}
                  {editCredentialLayout.otherFields.map((field) => <CredentialFieldInput key={field.name} field={field} value={editForm.credentials?.[field.name] ?? ""} onChange={(value) => setEditForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [field.name]: value } }))} isZh={isZh} />)}
                </>
              )}
              discovery={(
                <div className="v2-field-span-2">
                  <FieldLabel required info={localizedMessage(isZh, "v2.providers.usedToAutoFetchAvailableModelListWhen")}>{localizedMessage(isZh, "v2.providers.modelDiscovery2")}</FieldLabel>
                  {editModelsMode === "url" ? <Input placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1Models")} value={editForm.models_url ?? ""} onChange={(event) => setEditForm({ ...editForm, models_url: event.target.value })} /> : <textarea ref={editModelsTextareaRef} rows={1} className="model-textarea nyro-shadcn-input flex min-h-[40px] w-full resize-none overflow-hidden rounded-md border border-border bg-background px-3 text-sm text-foreground transition-[border-color,background-color,color] outline-none placeholder:text-muted-foreground focus-visible:ring-2 focus-visible:ring-slate-300 disabled:cursor-not-allowed disabled:opacity-50" placeholder={localizedMessage(isZh, "v2.providers.oneModelPerLineEGGpt4o")} value={editForm.models ?? ""} onChange={(event) => { setEditForm({ ...editForm, models: event.target.value }); autoGrowTextarea(event.target); }} />}
                  <ToggleGroup type="single" value={editModelsMode} onValueChange={(value) => { if (value) setEditModelsMode(value as ModelsMode); }} className="provider-region-group"><ToggleGroupItem value="url" variant="outline" size="sm">{localizedMessage(isZh, "v2.providers.autoDiscovery")}</ToggleGroupItem><ToggleGroupItem value="static" variant="outline" size="sm">{localizedMessage(isZh, "v2.providers.manualEntry")}</ToggleGroupItem></ToggleGroup>
                </div>
              )}
            />
            {editError && <p className="v2-provider-form-error">{editError}</p>}
          </ResourceEditorDialog>
        );
      })}

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
        <DialogContent className="v2-provider-test-dialog">
          <DialogHeader>
            <DialogTitle>
              {testDialogMode === "create"
                ? localizedMessage(isZh, "providers.testDialog.create", { name: pendingCreateInput?.name ?? "" })
                : testDialogMode === "edit"
                  ? localizedMessage(isZh, "providers.testDialog.edit", { name: editForm.name ?? "" })
                  : testDialogMode === "route_import"
                    ? localizedMessage(isZh, "providers.testDialog.import", { name: testTarget?.name ?? "" })
                    : localizedMessage(isZh, "providers.testDialog.test", { name: testTarget?.name ?? "" })}
            </DialogTitle>
            <DialogDescription>
              {testDialogMode === "create"
                ? (localizedMessage(isZh, "v2.providers.realTimePreCreateValidationPipeline"))
                : testDialogMode === "edit"
                  ? (localizedMessage(isZh, "v2.providers.realTimePreSaveValidationPipeline"))
                  : testDialogMode === "route_import"
                    ? (localizedMessage(isZh, "v2.providers.realTimeProgressForRouteImport"))
                    : (localizedMessage(isZh, "v2.providers.realTimeLogsForProviderTesting"))}
            </DialogDescription>
          </DialogHeader>
          <ProviderTestLog
            logs={testLogs}
            emptyLabel={localizedMessage(isZh, "v2.providers.waitingForTestToStart")}
            containerRef={logsContainerRef}
          />
          <DialogFooter>
            {testDialogMode === "create" && createHealthPassed && pendingCreateInput ? (
              <Button onClick={() => createMut.mutate(pendingCreateInput)} disabled={createMut.isPending}>
                {createMut.isPending
                  ? (localizedMessage(isZh, "v2.providers.creating"))
                  : (localizedMessage(isZh, "v2.providers.createProvider"))}
              </Button>
            ) : testDialogMode === "edit" && editHealthPassed && pendingUpdateInput ? (
              <Button onClick={() => updateMut.mutate(pendingUpdateInput)} disabled={updateMut.isPending}>
                {updateMut.isPending
                  ? (localizedMessage(isZh, "v2.providers.saving"))
                  : (localizedMessage(isZh, "v2.providers.saveProvider"))}
              </Button>
            ) : (
              <Button variant="secondary" onClick={closeTestDialog}>
                {isTestRunning
                  ? (localizedMessage(isZh, "v2.providers.cancel"))
                  : (localizedMessage(isZh, "v2.providers.close"))}
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
        title={localizedMessage(isZh, "v2.providers.confirmProviderDisable")}
        description={localizedMessage(isZh, "v2.providers.afterDisablingModelRequestsReferencingThisProviderWill")}
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.providers.disable")}
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
        title={localizedMessage(isZh, "v2.providers.confirmRouteImport")}
        description={
          routeImportPreview
            ? localizedMessage(isZh, "providers.importDescription", { name: routeImportPreview.provider.name })
            : undefined
        }
        content={routeImportPreview ? <RouteImportSummary preview={routeImportPreview.preview} /> : undefined}
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.providers.import2")}
        confirmClassName="v2-confirm-primary"
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
        title={localizedMessage(isZh, "v2.providers.confirmProviderDeletion")}
        description={
          providerToDelete
            ? localizedMessage(isZh, "providers.deleteDescription", { name: providerToDelete.name })
            : undefined
        }
        cancelText={localizedMessage(isZh, "v2.providers.cancel")}
        confirmText={localizedMessage(isZh, "v2.providers.delete")}
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
        confirmText={localizedMessage(isZh, "v2.providers.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}
