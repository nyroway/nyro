import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactNode, type RefObject, type SVGProps, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import clsx from "clsx";
import { upstreamsApi } from "@/lib/api/upstreams";
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
  Pencil,
  ChevronLeft,
  ChevronRight,
  Circle,
  Check,
  Eye,
  EyeOff,
  Filter,
  Loader2,
  Search,
  X,
} from "lucide-react";
import { useLocale } from "@/lib/i18n";
import { ProviderIcon } from "@/components/ui/provider-icon";
import { NyroHelpHint, NyroSearchSelect, NyroTextField, NyroTextareaField } from "@/components/ui/nyro-fields";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { resolveProtocol, PROTOCOL_TABLE, protocolDisplayName } from "@/lib/protocol";
import {
  isCustomProviderPreset,
  withCustomProviderPreset,
} from "@/lib/provider-presets";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { Inspector } from "@/components/v2/inspector";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { ResourceEditorDialog, ResourceEditorDrawer } from "@/components/v2/resource-editor-dialog";
import { RowActionMenu } from "@/components/v2/row-action-menu";
import { Status } from "@/components/v2/status";
import { ModelTagInput } from "@/features/providers/model-tag-input";
import { normalizeModelTags } from "@/features/providers/model-tags";
import { filterProviders, type ProviderFilters } from "@/features/providers/provider-view-model";
import { localizedMessage, type MessageKey } from "@/lib/messages";

/* 真源 #test「心跳」图标（providers.html symbol）：探测动作的 EKG 折线，
   替代此前的 lucide 闪电——path/描边(1.7)/圆角连接与基线逐项一致。 */
function HeartbeatIcon({ ...props }: SVGProps<SVGSVGElement>) {
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
  const models = normalizeModelTags(text.split("\n"));
  return models.length ? models : undefined;
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
// Sentinel empty arrays: destructuring `data` with `= []` mints a fresh array every
// render while the query is in flight, so any useMemo/useCallback/useEffect depending
// on it re-fires each render. The ?focus deep-link effect raced navigate() that way and
// looped into React #185 (max update depth) when the providers cache was pre-warmed by
// the global search but presets were still loading. Module-level constants keep the
// fallback identity stable.
const NO_UPSTREAMS: Upstream[] = [];
const NO_PRESET_DTOS: ProviderPresetDTO[] = [];
// Labels mirror go/internal/llm/protocol's Protocol.DisplayName(). TODO: fold into
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
};

/* 详情抽屉的主体（§9.2 ⑦，真源 #providerDrawer）：基线 descriptions 键值栅格；
   动作按钮一律放抽屉 footer（drawer-footer-start/end），不留在卡体里。 */
export function ProviderDetailContent({ provider, result }: ProviderDetailContentProps) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const healthLabel = !result
    ? localizedMessage(isZh, "v2.providers.notTested")
    : result.success
      ? localizedMessage(isZh, "v2.providers.healthy")
      : localizedMessage(isZh, "v2.providers.failed2");
  const healthClass = !result
    ? "health is-idle"
    : result.success
      ? "health"
      : "health danger";

  return (
    <dl className="descriptions">
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.health")}</dt>
        <dd><span className={healthClass}><span className="mini-dot" aria-hidden="true" />{healthLabel}</span></dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.latency")}</dt>
        <dd>{result ? `${result.latency_ms}ms` : "—"}</dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.models")}</dt>
        <dd>{provider.models?.length ?? 0}</dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.protocol")}</dt>
        <dd>{protocolDisplayName(provider.protocol ?? "") ?? provider.protocol ?? "—"}</dd>
      </div>
      <div>
        <dt>Base URL</dt>
        <dd><code className="code-pill">{provider.base_url || "—"}</code></dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.credentials")}</dt>
        <dd>{provider.credentials ? localizedMessage(isZh, "v2.providers.configured") : "—"}</dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.modelDiscoveryAddress")}</dt>
        <dd><code className="code-pill">{provider.models_url || "—"}</code></dd>
      </div>
      <div>
        <dt>{localizedMessage(isZh, "v2.providers.status")}</dt>
        <dd>{provider.enabled ? localizedMessage(isZh, "v2.providers.enabled") : localizedMessage(isZh, "v2.providers.disabled")}</dd>
      </div>
    </dl>
  );
}

/* 创建/编辑表单的三段骨架（§9.2 ②③④）：基线 form-section/form-grid。
   add-provider-form 外壳让基线的紧凑 tags-control 等表单内规则生效。 */
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
    <div className="add-provider-form">
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
    <section className="form-section" data-provider-form-section={name}>
      <h3 className="form-section-title">{title}</h3>
      <p className="form-section-desc">{description}</p>
      <div className="form-grid">{children}</div>
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
// keyed by the Go backend's field `type` ("string" | "secret" | "enum") —
// §9.2 ③：基线 form-grid + secret-control/secret-toggle 词汇。Secret fields
// whose name looks like a JSON blob (e.g. gcp-vertex's
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
      <NyroSearchSelect<string>
        label={label}
        required={field.required}
        options={field.values}
        value={value || field.default || field.values[0]}
        onChange={(next) => onChange(next ?? "")}
        getOptionLabel={(option) => option}
        searchable={false}
        fullWidth={false}
      />
    );
  }

  if (isJsonBlob) {
    return (
      <NyroTextareaField
        label={label}
        required={field.required}
        placeholder={localizedMessage(isZh, "v2.providers.pasteJsonContent")}
        value={value}
        rows={8}
        className="input-mono"
        autoCapitalize="none"
        autoCorrect="off"
        spellCheck={false}
        onChange={(event) => onChange(event.target.value)}
      />
    );
  }

  if (isSecret) {
    return (
      <div className="field full">
        <label className="field-label">
          {label}
          {field.required ? <span className="required" aria-hidden="true">*</span> : null}
        </label>
        <span className="field-control secret-control">
          <input
            type={reveal ? "text" : "password"}
            placeholder={credentialPlaceholder}
            value={value}
            autoComplete="off"
            aria-label={label}
            onChange={(event) => onChange(event.target.value)}
          />
          <button
            type="button"
            className="secret-toggle"
            onClick={() => setReveal((prev) => !prev)}
            aria-label={reveal ? (localizedMessage(isZh, "v2.providers.hide")) : (localizedMessage(isZh, "v2.providers.show"))}
          >
            {reveal ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
          </button>
        </span>
      </div>
    );
  }

  return (
    <NyroTextField
      label={label}
      required={field.required}
      placeholder={credentialPlaceholder}
      value={value}
      fullWidth={false}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

type TestLogLevel = "info" | "success" | "error";

type TestLogEntry = {
  timestamp: string;
  level: TestLogLevel;
  message: string;
};

/* SSE 流式帧的步进视图（§9.2 ⑤）：waf-gateway 的 probe-steps 模式。
   每步 idle（空心圆）/running（旋转）/passed（对勾）/failed（叉），
   meta 显示等待/时延/数量/错误。 */
export type ProbeStepStatus = "idle" | "running" | "passed" | "failed";

export type ProbeStepView = {
  key: string;
  label: string;
  status: ProbeStepStatus;
  meta?: string;
};

export function ProbeSteps({ steps, waitingLabel }: { steps: ProbeStepView[]; waitingLabel: string }) {
  return (
    <div className="probe-steps">
      {steps.map((step) => (
        <div
          key={step.key}
          className={clsx(
            "probe-step",
            step.status === "passed" && "done",
            step.status === "failed" && "failed",
          )}
        >
          <span className="probe-step-icon" aria-hidden="true">
            {step.status === "running" ? <Loader2 className="probe-spin" />
              : step.status === "passed" ? <Check />
                : step.status === "failed" ? <X />
                  : <Circle />}
          </span>
          <span className="probe-step-label">{step.label}</span>
          <small className="probe-step-meta">{step.meta ?? waitingLabel}</small>
        </div>
      ))}
    </div>
  );
}

/* 测试/导入全量日志（§9.2 ⑤）：基线 code-output 卡 + 每行级别着色。 */
export function ProviderTestLog({
  logs,
  emptyLabel,
  containerRef,
  statusLabel,
}: {
  logs: TestLogEntry[];
  emptyLabel: string;
  containerRef?: RefObject<HTMLDivElement | null>;
  statusLabel: string;
}) {
  return (
    <div className="code-output">
      <div className="code-output-bar">
        <span className="code-output-lang">LOG</span>
        <span className="code-output-meta">{statusLabel}</span>
      </div>
      <div ref={containerRef} className="code-output-body provider-test-log">
        {logs.length === 0
          ? <p className="provider-test-empty">{emptyLabel}</p>
          : logs.map((log, index) => (
            <p key={`${log.timestamp}-${index}`} data-log-level={log.level}>
              <time>{log.timestamp}</time><span>{log.message}</span>
            </p>
          ))}
      </div>
    </div>
  );
}

/* 路由导入预览（§9.2 ⑥）：汇总指标 + 逐模型动作表（创建/跳过）。 */
export function RouteImportSummary({ preview }: { preview: RouteImportPreview }) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";

  return (
    <div className="provider-import-summary">
      <div className="provider-import-metrics">
        <div><span>{localizedMessage(isZh, "v2.providers.discovered")}</span><strong>{preview.discovered}</strong></div>
        <div><span>{localizedMessage(isZh, "v2.providers.create")}</span><strong>{preview.create.length}</strong></div>
        <div><span>{localizedMessage(isZh, "v2.providers.skip")}</span><strong>{preview.skip.length}</strong></div>
      </div>
      <div className="modal-list-items">
        <table className="table provider-import-table">
          <thead>
            <tr>
              <th>{localizedMessage(isZh, "v2.providers.model")}</th>
              <th>{localizedMessage(isZh, "v2.providers.action")}</th>
            </tr>
          </thead>
          <tbody>
            {preview.create.map((model) => (
              <tr key={`create-${model}`}>
                <td><code className="code-pill">{model}</code></td>
                <td><span className="tag tag-success">{localizedMessage(isZh, "v2.providers.create")}</span></td>
              </tr>
            ))}
            {preview.skip.map((model) => (
              <tr key={`skip-${model}`}>
                <td><code className="code-pill">{model}</code></td>
                <td><span className="tag">{localizedMessage(isZh, "v2.providers.skip")}</span></td>
              </tr>
            ))}
          </tbody>
        </table>
        {preview.create.length === 0 && (
          <p className="field-hint">{localizedMessage(isZh, "v2.providers.noRoutesNeedToBeCreated")}</p>
        )}
      </div>
      {preview.skip.length > 0 && (
        <p className="field-hint">{localizedMessage(isZh, "providers.importSkipped", { count: preview.skip.length })}</p>
      )}
    </div>
  );
}

// SSE 帧驱动的步进状态：健康检查 4 步 / 路由导入 2 阶段，同一份状态表。
type ProbeStepState = { status: ProbeStepStatus; meta?: string };

const HEALTH_CHECK_KEYS: Exclude<ProviderHealthEvent["check"], undefined>[] = ["config", "credentials", "models", "model_request"];
const ROUTE_STAGE_KEYS: Exclude<RouteImportEvent["stage"], undefined>[] = ["models", "creating"];

// 路由导入的逐模型结果行（§9.2 ⑥ 的导入结果表）。
type RouteResultRow = {
  model: string;
  status: "created" | "skipped" | "failed";
  note?: string;
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
  const [probeSteps, setProbeSteps] = useState<Record<string, ProbeStepState>>({});
  const [routeResults, setRouteResults] = useState<RouteResultRow[]>([]);
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

  const { data: providers = NO_UPSTREAMS, isLoading } = useQuery<Upstream[]>({
    queryKey: ["providers"],
    queryFn: () => upstreamsApi.list(),
  });
  const { data: providerPresetsRaw = NO_PRESET_DTOS } = useQuery<ProviderPresetDTO[]>({
    queryKey: ["provider-presets"],
    queryFn: () => upstreamsApi.presets(),
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
    mutationFn: (input: CreateUpstream) => upstreamsApi.create(input),
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
      upstreamsApi.update(id, input),
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
    mutationFn: (id: string) => upstreamsApi.remove(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("providers.error.delete", error);
    },
  });

  const [providerToDisable, setProviderToDisable] = useState<Upstream | null>(null);

  const toggleEnabledMut = useMutation({
    mutationFn: ({ id, is_enabled }: { id: string; is_enabled: boolean }) =>
      upstreamsApi.update(id, { enabled: is_enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["providers"] }),
    onError: (error: unknown) => {
      showErrorDialog("providers.error.operation", error);
    },
  });

  function appendTestLog(level: TestLogLevel, message: string) {
    setTestLogs((prev) => [...prev, { timestamp: nowTimestamp(), level, message }]);
  }

  function updateProbeStep(key: string, state: ProbeStepState) {
    setProbeSteps((prev) => ({ ...prev, [key]: state }));
  }

  // complete(success) 意味着整条流水线通过：未收到独立事件的步骤一并置为
  // passed（failed 的保持失败，如实反映）。
  function promoteProbeSteps(keys: string[]) {
    setProbeSteps((prev) => {
      const next = { ...prev };
      for (const key of keys) {
        if (next[key]?.status === "failed") continue;
        next[key] = { status: "passed", meta: next[key]?.meta };
      }
      return next;
    });
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
    setProbeSteps({});
    setRouteResults([]);
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

      await upstreamsApi.testHealth(provider.id, (event) => {
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
      if (event.success) promoteProbeSteps(HEALTH_CHECK_KEYS);
      return;
    }

    if (!event.check) return;
    const name = healthCheckName(event.check);
    if (event.status === "running") {
      updateProbeStep(event.check, { status: "running" });
      appendTestLog("info", `▶ ${name}${event.model ? ` (${event.model})` : ""}`);
      return;
    }
    if (event.status === "passed") {
      updateProbeStep(event.check, {
        status: "passed",
        meta: event.check === "models" && event.models?.length
          ? localizedMessage(isZh, "providers.modelsFound", { count: event.models.length })
          : event.latency_ms != null ? `${event.latency_ms}ms` : undefined,
      });
      if (event.check === "models" && event.models && event.models.length > 0) {
        appendTestLog("success", `✓ ${name} (${localizedMessage(isZh, "providers.modelsFound", { count: event.models.length })})`);
        for (const model of event.models) {
          appendTestLog("info", `    - ${model}`);
        }
        return;
      }
      appendTestLog(
        "success",
        `✓ ${name}${event.model ? ` (${event.model})` : ""}${event.latency_ms != null ? ` ${event.latency_ms}ms` : ""}`,
      );
      return;
    }
    if (event.status === "failed") {
      updateProbeStep(event.check, { status: "failed", meta: event.error ?? event.message });
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
      if (event.success) promoteProbeSteps(ROUTE_STAGE_KEYS);
      return;
    }
    if (event.type === "stage") {
      if (!event.stage) return;
      const name = routeImportStageName(event.stage);
      if (event.status === "running") {
        updateProbeStep(event.stage, { status: "running" });
        appendTestLog("info", `▶ ${name}`);
      } else if (event.status === "passed") {
        updateProbeStep(event.stage, {
          status: "passed",
          meta: event.count != null ? `${event.count}` : undefined,
        });
        appendTestLog("success", `✓ ${name}${event.count != null ? ` (${event.count})` : ""}`);
      } else if (event.status === "failed") {
        updateProbeStep(event.stage, { status: "failed", meta: event.error ?? event.message });
        appendTestLog("error", `✗ ${name}: ${event.error ?? event.message ?? (localizedMessage(isZh, "v2.providers.failed"))}`);
      }
      return;
    }
    if (event.type === "route") {
      if (event.status === "created") {
        setRouteResults((prev) => [...prev, { model: event.model ?? "", status: "created" }]);
        appendTestLog("success", `✓ ${event.model ?? ""}`);
      } else if (event.status === "skipped") {
        setRouteResults((prev) => [...prev, { model: event.model ?? "", status: "skipped" }]);
        appendTestLog("info", `- ${event.model ?? ""} ${localizedMessage(isZh, "v2.providers.alreadyExistsSkipped")}`);
      } else if (event.status === "failed") {
        const note = event.error ?? event.reason ?? (localizedMessage(isZh, "v2.providers.failed"));
        setRouteResults((prev) => [...prev, { model: event.model ?? "", status: "failed", note }]);
        appendTestLog("error", `✗ ${event.model ?? ""}: ${note}`);
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
    setProbeSteps({});
    setRouteResults([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", localizedMessage(isZh, "providers.startImport", { name: provider.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.existingRoutesWithTheSameNameAreSkipped"));

    try {
      await upstreamsApi.importRoutes(provider.id, (event) => {
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
      const preview = await upstreamsApi.importPreview(provider.id);
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
    setProbeSteps({});
    setRouteResults([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", localizedMessage(isZh, "providers.startPreCreate", { name: input.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.aMinimalUpstreamModelRequestWillBeSent"));

    try {
      await upstreamsApi.testDraft(input, (event) => {
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
    setProbeSteps({});
    setRouteResults([]);
    setTestDialogOpen(true);
    setIsTestRunning(true);

    appendTestLog("info", localizedMessage(isZh, "providers.startPreSave", { name: draft.name }));
    appendTestLog("info", localizedMessage(isZh, "v2.providers.aMinimalUpstreamModelRequestWillBeSent"));

    try {
      await upstreamsApi.testEditDraft(update.id, draft, (event) => {
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

  // Deep links (?focus= / ?action=create) must fire exactly once per link even while
  // deps are still settling: re-running startEdit+navigate before the replace commits
  // loops into React #185. The ref remembers the handled link until the param is gone.
  const deepLinkHandledRef = useRef<string | null>(null);
  useEffect(() => {
    const params = new URLSearchParams(location.search);
    const create = params.get("action") === "create";
    const focus = params.get("focus");
    if (!create && !focus) {
      deepLinkHandledRef.current = null;
      return;
    }
    const linkKey = create ? "create" : `focus:${focus}`;
    if (deepLinkHandledRef.current === linkKey) return;
    if (create) {
      deepLinkHandledRef.current = linkKey;
      setEditingId(null);
      setShowForm(true);
      navigate(location.pathname, { replace: true });
      return;
    }
    if (providerPresets.length === 0) return;
    const provider = providers.find((item) => item.id === focus);
    if (!provider) return;
    deepLinkHandledRef.current = linkKey;
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

  // 步进视图：健康模式 4 检查项 / 导入模式 2 阶段。
  function probeStepView(key: string, label: string): ProbeStepView {
    const state = probeSteps[key];
    return { key, label, status: state?.status ?? "idle", meta: state?.meta };
  }

  const probeStepViews: ProbeStepView[] = testDialogMode === "route_import"
    ? ROUTE_STAGE_KEYS.map((stage) => probeStepView(stage, routeImportStageName(stage)))
    : HEALTH_CHECK_KEYS.map((check) => probeStepView(check, healthCheckName(check)));

  const providerColumns: DataTableColumn<Upstream>[] = [
    {
      key: "provider",
      header: localizedMessage(isZh, "v2.providers.provider"),
      render: (provider) => {
        const preset = providerPresets.find((item) => item.id === (provider.provider || ""));
        return (
          <div className="provider-name">
            <ProviderIcon
              iconKey={preset?.icon}
              name={provider.name}
              protocol={provider.protocol}
              baseUrl={provider.base_url}
              size={28}
            />
            <div><strong>{provider.name}</strong><small>{provider.provider || "custom"}</small></div>
          </div>
        );
      },
    },
    {
      key: "protocol",
      header: localizedMessage(isZh, "v2.providers.protocol"),
      render: (provider) => protocolDisplayName(provider.protocol ?? "") ?? provider.protocol ?? "—",
    },
    {
      key: "connection",
      header: localizedMessage(isZh, "v2.providers.endpoint"),
      render: (provider) => (
        <div className="cell-stack">
          <span className="provider-link" title={provider.base_url}>{provider.base_url || "—"}</span>
          {provider.proxy_url ? <small className="cell-note">{localizedMessage(isZh, "v2.providers.viaProxy")}</small> : null}
        </div>
      ),
    },
    {
      key: "models",
      header: localizedMessage(isZh, "v2.providers.modelCount"),
      render: (provider) => provider.models?.length ?? 0,
    },
    {
      key: "health",
      header: localizedMessage(isZh, "v2.providers.health"),
      render: (provider) => {
        const result = testResult[provider.id];
        if (!result) {
          return <span className="health is-idle"><span className="mini-dot" aria-hidden="true" />{localizedMessage(isZh, "v2.providers.notTested")}</span>;
        }
        return result.success
          ? <span className="health"><span className="mini-dot" aria-hidden="true" />{localizedMessage(isZh, "v2.providers.healthy")}</span>
          : <span className="health danger"><span className="mini-dot" aria-hidden="true" />{localizedMessage(isZh, "v2.providers.failed2")}</span>;
      },
    },
    {
      key: "enabled",
      header: localizedMessage(isZh, "v2.providers.enable"),
      className: "cell-switch",
      render: (provider) => (
        <button
          type="button"
          role="switch"
          aria-checked={provider.enabled}
          className={clsx("switch-control sm", provider.enabled && "on")}
          onClick={(event) => {
            event.stopPropagation();
            if (provider.enabled) setProviderToDisable(provider);
            else toggleEnabledMut.mutate({ id: provider.id, is_enabled: true });
          }}
          title={provider.enabled ? localizedMessage(isZh, "v2.providers.disable") : localizedMessage(isZh, "v2.providers.enable")}
        />
      ),
    },
    {
      key: "actions",
      header: localizedMessage(isZh, "v2.providers.actions"),
      className: "cell-actions",
      render: (provider) => (
        <div className="row-actions">
          <button
            type="button"
            className="icon-action"
            data-tip={localizedMessage(isZh, "v2.providers.edit")}
            aria-label={localizedMessage(isZh, "v2.providers.edit")}
            onClick={(event) => { event.stopPropagation(); startEdit(provider); }}
          >
            <Pencil aria-hidden="true" />
          </button>
          <button
            type="button"
            className="icon-action"
            data-tip={localizedMessage(isZh, "v2.providers.probe")}
            aria-label={localizedMessage(isZh, "v2.providers.probe")}
            onClick={(event) => { event.stopPropagation(); void handleTest(provider); }}
          >
            <HeartbeatIcon aria-hidden="true" />
          </button>
          <RowActionMenu label={localizedMessage(isZh, "v2.providers.more")}>
            <button
              type="button"
              onClick={() => void handlePreviewRouteImport(provider)}
            >
              {localizedMessage(isZh, "v2.providers.importModels")}
            </button>
            <button
              type="button"
              className="danger"
              onClick={() => setProviderToDelete(provider)}
            >
              {localizedMessage(isZh, "v2.providers.delete")}
            </button>
          </RowActionMenu>
        </div>
      ),
    },
  ];

  return (
    <PageLayout
      header={(
        <PageHeader
          title={t("page.providers.title")}
          description={t("page.providers.subtitle")}
        />
      )}
    >
      {/* 统计条（真源 .resource-summary）：卡外内联数字条，新增按钮移入工具条。 */}
      <div className="resource-summary">
        <span><strong>{providers.length}</strong><span>{localizedMessage(isZh, "v2.providers.summaryProviders")}</span></span>
        <span><strong>{providers.filter((provider) => provider.enabled).length}</strong><span>{localizedMessage(isZh, "v2.providers.summaryEnabled")}</span></span>
        <span><strong>{providers.reduce((count, provider) => count + (provider.models?.length ?? 0), 0)}</strong><span>{localizedMessage(isZh, "v2.providers.summaryModels")}</span></span>
      </div>

      {/* Create Form */}
      <ResourceEditorDrawer
        open={showForm}
        title={localizedMessage(isZh, "v2.providers.newProvider")}
        description={localizedMessage(isZh, "v2.providers.configureConnectionCredentialsAndModelDiscovery")}
        onClose={closeCreateForm}
        footer={(
          <>
            <button type="button" className="button button-secondary" onClick={closeCreateForm}>
              {localizedMessage(isZh, "v2.providers.cancel")}
            </button>
            <button
              type="button"
              className="button button-primary"
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
            </button>
          </>
        )}
      >
        <ProviderFormSections
          connection={(
            <>
              <div className="field full">
                <span className="field-label">
                  <span className="required" aria-hidden="true">*</span>
                  {localizedMessage(isZh, "v2.providers.provider")}
                </span>
                <div className="provider-pick">
                  {providerPresets.map((preset) => (
                    <button
                      type="button"
                      key={preset.id}
                      className={clsx("provider-pick-item", selectedPresetId === preset.id && "selected")}
                      aria-pressed={selectedPresetId === preset.id}
                      aria-label={presetLabel(preset)}
                      onClick={() => {
                        if (selectedPresetId !== preset.id) handleTemplateChange(preset.id);
                      }}
                    >
                      <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={20} />
                      <span>{presetLabel(preset)}</span>
                    </button>
                  ))}
                </div>
              </div>
              <NyroTextField
                label={localizedMessage(isZh, "v2.providers.name")}
                required
                fullWidth={false}
                placeholder={localizedMessage(isZh, "v2.providers.eGOpenaiProduction")}
                value={form.name}
                onChange={(event) => setForm({ ...form, name: event.target.value })}
              />
              <NyroSearchSelect<ProviderProtocol>
                label={localizedMessage(isZh, "v2.providers.protocol")}
                required
                fullWidth={false}
                options={createProtocolOptions}
                value={(form.protocol as ProviderProtocol) ?? null}
                onChange={(next) => { if (next) handleProtocolChange(next); }}
                getOptionLabel={(protocol) => protocolDisplayName(protocol) ?? protocol}
                searchable={false}
              />
              <NyroTextField
                label="Base URL"
                required
                fullWidth={false}
                placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1")}
                value={form.base_url}
                onChange={(event) => setForm({ ...form, base_url: event.target.value })}
              />
              <NyroTextField
                label={localizedMessage(isZh, "v2.providers.proxyUrl")}
                fullWidth={false}
                placeholder={localizedMessage(isZh, "v2.providers.eGHttp1270017890")}
                value={form.proxy_url ?? ""}
                onChange={(event) => setForm({ ...form, proxy_url: event.target.value })}
              />
            </>
          )}
          credentials={(
            <>
              {createCredentialLayout.apiKeyField && <CredentialFieldInput field={createCredentialLayout.apiKeyField} value={form.credentials?.[createCredentialLayout.apiKeyField.name] ?? ""} onChange={(value) => setForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [createCredentialLayout.apiKeyField!.name]: value } }))} isZh={isZh} />}
              {createCredentialLayout.otherFields.map((field) => <CredentialFieldInput key={field.name} field={field} value={form.credentials?.[field.name] ?? ""} onChange={(value) => setForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [field.name]: value } }))} isZh={isZh} />)}
            </>
          )}
          discovery={(
            <div className="field full">
              <div className="field-label discover-label">
                {/* "?" 帮助钮必须是 field-label 的直接子元素（flex + gap 居中对齐，
                    同基线 waf-gateway 的 label 结构）；包进文本 span 会掉进行内
                    基线对齐导致 icon 错位。 */}
                <span>
                  <span className="required" aria-hidden="true">*</span>
                  {localizedMessage(isZh, "v2.providers.modelDiscovery2")}
                </span>
                <NyroHelpHint text={localizedMessage(isZh, "v2.providers.usedToAutoFetchAvailableModelListWhen")} />
                <div className="discover-switch" role="radiogroup" aria-label={localizedMessage(isZh, "v2.providers.modelDiscovery2")}>
                  <button type="button" role="radio" aria-checked={modelsMode === "url"} className={modelsMode === "url" ? "selected" : ""} onClick={() => setModelsMode("url")}>
                    {localizedMessage(isZh, "v2.providers.autoDiscovery")}
                  </button>
                  <button type="button" role="radio" aria-checked={modelsMode === "static"} className={modelsMode === "static" ? "selected" : ""} onClick={() => setModelsMode("static")}>
                    {localizedMessage(isZh, "v2.providers.manualEntry")}
                  </button>
                </div>
              </div>
              {modelsMode === "url" ? (
                <input
                  className="field-control"
                  placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1Models")}
                  aria-label={localizedMessage(isZh, "v2.providers.modelDiscovery2")}
                  value={form.models_url ?? ""}
                  onChange={(event) => setForm({ ...form, models_url: event.target.value })}
                />
              ) : (
                <ModelTagInput
                  value={modelsArrayFromText(form.models) ?? []}
                  onChange={(models) => setForm((current) => ({ ...current, models: models.join("\n") }))}
                  inputLabel={localizedMessage(isZh, "v2.providers.manualModelInput")}
                  listLabel={localizedMessage(isZh, "v2.providers.manualModelList")}
                  placeholder={localizedMessage(isZh, "v2.providers.enterModelId")}
                  helpText={localizedMessage(isZh, "v2.providers.manualModelsHelp")}
                  removeLabel={(model) => localizedMessage(isZh, "v2.providers.removeModel", { model })}
                />
              )}
            </div>
          )}
        />
      </ResourceEditorDrawer>

      <DataTable
        carded
        columns={providerColumns}
        rows={pagedProviders}
        rowKey={(provider) => provider.id}
        onRowClick={(provider) => setSelectedProviderId(provider.id)}
        loading={isLoading}
        toolbar={(
          <>
            <label className="toolbar-search">
              <Search aria-hidden="true" />
              <input
                aria-label={localizedMessage(isZh, "v2.providers.searchProviders")}
                placeholder={localizedMessage(isZh, "v2.providers.searchNameProtocolOrEndpoint")}
                value={filters.query}
                onChange={(event) => setFilters((current) => ({ ...current, query: event.target.value }))}
              />
            </label>
            <NyroSearchSelect<string>
              options={["all", ...protocolOptions.map((option) => option.value)]}
              value={filters.protocol}
              onChange={(next) => setFilters((current) => ({ ...current, protocol: next ?? "all" }))}
              getOptionLabel={(value) => value === "all"
                ? localizedMessage(isZh, "v2.providers.allProtocols")
                : protocolDisplayName(value) ?? value}
              searchable={false}
              fullWidth={false}
              controlClassName="toolbar-filter"
              leadingIcon={<Filter size={14} aria-hidden="true" />}
              ariaLabel={localizedMessage(isZh, "v2.providers.filterByProtocol")}
            />
            <NyroSearchSelect<string>
              options={["all", "enabled", "disabled"]}
              value={filters.enabled}
              onChange={(next) => setFilters((current) => ({ ...current, enabled: (next ?? "all") as ProviderFilters["enabled"] }))}
              getOptionLabel={(value) => value === "all"
                ? localizedMessage(isZh, "v2.providers.allStatuses")
                : value === "enabled"
                  ? localizedMessage(isZh, "v2.providers.enabled")
                  : localizedMessage(isZh, "v2.providers.disabled")}
              searchable={false}
              fullWidth={false}
              controlClassName="toolbar-filter"
              ariaLabel={localizedMessage(isZh, "v2.providers.filterByStatus")}
            />
            <button
              type="button"
              className="button button-primary button-sm toolbar-add"
              onClick={() => {
                setEditingId(null);
                setShowForm(true);
                setSelectedPresetId("");
                setModelsMode("url");
                setForm(emptyCreate);
              }}
            >
              {localizedMessage(isZh, "v2.providers.addProvider")}
            </button>
          </>
        )}
        empty={(
          <EmptyState
            title={providers.length === 0
              ? (localizedMessage(isZh, "v2.providers.noProvidersConfigured"))
              : (localizedMessage(isZh, "v2.providers.noProvidersMatchTheseFilters"))}
            description={providers.length === 0
              ? (localizedMessage(isZh, "v2.providers.addYourFirstModelServiceConnectionToStart"))
              : (localizedMessage(isZh, "v2.providers.tryAdjustingTheSearchOrFilters"))}
            action={providers.length === 0 ? (
              <button type="button" className="button button-primary" onClick={() => setShowForm(true)}>
                {localizedMessage(isZh, "v2.providers.addProvider")}
              </button>
            ) : undefined}
          />
        )}
        footer={(
          <>
            <div className="provider-table-meta">
              <Status tone="success">
                {localizedMessage(isZh, "v2.providers.availableCount", {
                  count: filteredProviders.filter((provider) => provider.enabled && testResult[provider.id]?.success).length,
                })}
              </Status>
              <div className="table-footer-notes">
                <span>{localizedMessage(isZh, "v2.providers.healthStatusNotice")}</span>
                <span>{localizedMessage(isZh, "v2.providers.credentialsPlaintextNotice")}</span>
              </div>
            </div>
            {totalPages > 1 && (
              <div className="pagination">
                <span className="table-summary">{localizedMessage(isZh, "common.pagination", { page: page + 1, total: totalPages })}</span>
                <button
                  type="button"
                  className="page-button"
                  disabled={page === 0}
                  aria-label={localizedMessage(isZh, "common.prevPage")}
                  onClick={() => setPage(Math.max(0, page - 1))}
                >
                  <ChevronLeft aria-hidden="true" />
                </button>
                <button
                  type="button"
                  className="page-button"
                  disabled={page >= totalPages - 1}
                  aria-label={localizedMessage(isZh, "common.nextPage")}
                  onClick={() => setPage(Math.min(totalPages - 1, page + 1))}
                >
                  <ChevronRight aria-hidden="true" />
                </button>
              </div>
            )}
          </>
        )}
      />

      <Inspector
        open={Boolean(selectedProvider)}
        className="drawer-wide"
        title={selectedProvider?.name ?? localizedMessage(isZh, "v2.providers.providerDetails")}
        description={selectedProvider?.id}
        onClose={() => setSelectedProviderId(null)}
        footer={selectedProvider ? (
          <>
            {/* 基线 #providerDrawer footer：左侧删除/导入（text 系），右侧关闭/测试/编辑配置。 */}
            <div className="drawer-footer-start">
              <button
                type="button"
                className="button button-text button-danger-text"
                onClick={() => { setSelectedProviderId(null); setProviderToDelete(selectedProvider); }}
              >
                {localizedMessage(isZh, "v2.providers.delete")}
              </button>
              <button
                type="button"
                className="button button-text"
                onClick={() => { setSelectedProviderId(null); void handlePreviewRouteImport(selectedProvider); }}
              >
                {localizedMessage(isZh, "v2.providers.importModels")}
              </button>
            </div>
            <div className="drawer-footer-end">
              <button type="button" className="button button-secondary" onClick={() => setSelectedProviderId(null)}>
                {localizedMessage(isZh, "v2.providers.close")}
              </button>
              <button
                type="button"
                className="button button-secondary"
                onClick={() => { setSelectedProviderId(null); void handleTest(selectedProvider); }}
              >
                {localizedMessage(isZh, "v2.providers.test")}
              </button>
              <button
                type="button"
                className="button button-primary"
                onClick={() => { setSelectedProviderId(null); startEdit(selectedProvider); }}
              >
                {localizedMessage(isZh, "v2.providers.editConfiguration")}
              </button>
            </div>
          </>
        ) : undefined}
      >
        {selectedProvider && (
          <ProviderDetailContent
            provider={selectedProvider}
            result={testResult[selectedProvider.id]}
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
          <ResourceEditorDrawer
            key={provider.id}
            open
            title={localizedMessage(isZh, "v2.providers.editProvider")}
            description={provider.name}
            onClose={() => { setEditingId(null); setEditError(null); }}
            footer={(
              <>
                <button type="button" className="button button-secondary" onClick={() => { setEditingId(null); setEditError(null); }}>
                  {localizedMessage(isZh, "v2.providers.cancel")}
                </button>
                <button
                  type="button"
                  className="button button-primary"
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
                </button>
              </>
            )}
          >
            <ProviderFormSections
              connection={(
                <>
                  <div className="field full">
                    <span className="field-label">
                      {localizedMessage(isZh, "v2.providers.provider")}
                      <NyroHelpHint text={localizedMessage(isZh, "v2.providers.theProviderPresetCanTBeChangedAfter")} />
                    </span>
                    <div className="provider-pick">
                      {editLockedPresets.map((preset) => (
                        <button
                          type="button"
                          key={preset.id}
                          className={clsx("provider-pick-item", editingPresetId === preset.id && "selected")}
                          disabled
                          aria-pressed={editingPresetId === preset.id}
                          aria-label={presetLabel(preset)}
                        >
                          <ProviderIcon iconKey={preset.icon} name={preset.icon ?? preset.name} size={20} />
                          <span>{presetLabel(preset)}</span>
                        </button>
                      ))}
                    </div>
                  </div>
                  <NyroTextField
                    label={localizedMessage(isZh, "v2.providers.name")}
                    required
                    fullWidth={false}
                    placeholder={localizedMessage(isZh, "v2.providers.eGOpenaiProduction")}
                    value={editForm.name ?? ""}
                    onChange={(event) => setEditForm({ ...editForm, name: event.target.value })}
                  />
                  <NyroSearchSelect<ProviderProtocol>
                    label={localizedMessage(isZh, "v2.providers.protocol")}
                    required
                    fullWidth={false}
                    options={editProtocolOptions}
                    value={(editForm.protocol as ProviderProtocol) ?? null}
                    onChange={(next) => { if (next) handleEditProtocolChange(next); }}
                    getOptionLabel={(protocol) => protocolDisplayName(protocol) ?? protocol}
                    searchable={false}
                  />
                  <NyroTextField
                    label="Base URL"
                    required
                    fullWidth={false}
                    placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1")}
                    value={editForm.base_url ?? ""}
                    onChange={(event) => setEditForm({ ...editForm, base_url: event.target.value })}
                  />
                  <NyroTextField
                    label={localizedMessage(isZh, "v2.providers.proxyUrl")}
                    fullWidth={false}
                    placeholder={localizedMessage(isZh, "v2.providers.eGHttp1270017890")}
                    value={editForm.proxy_url ?? ""}
                    onChange={(event) => setEditForm({ ...editForm, proxy_url: event.target.value })}
                  />
                </>
              )}
              credentials={(
                <>
                  {editCredentialLayout.apiKeyField && <CredentialFieldInput field={editCredentialLayout.apiKeyField} value={editForm.credentials?.[editCredentialLayout.apiKeyField.name] ?? ""} onChange={(value) => setEditForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [editCredentialLayout.apiKeyField!.name]: value } }))} isZh={isZh} />}
                  {editCredentialLayout.otherFields.map((field) => <CredentialFieldInput key={field.name} field={field} value={editForm.credentials?.[field.name] ?? ""} onChange={(value) => setEditForm((current) => ({ ...current, credentials: { ...(current.credentials ?? {}), [field.name]: value } }))} isZh={isZh} />)}
                </>
              )}
              discovery={(
                <div className="field full">
                  <div className="field-label discover-label">
                    {/* 同上：帮助钮作为 field-label 直接子元素参与 flex 对齐。 */}
                    <span>
                      <span className="required" aria-hidden="true">*</span>
                      {localizedMessage(isZh, "v2.providers.modelDiscovery2")}
                    </span>
                    <NyroHelpHint text={localizedMessage(isZh, "v2.providers.usedToAutoFetchAvailableModelListWhen")} />
                    <div className="discover-switch" role="radiogroup" aria-label={localizedMessage(isZh, "v2.providers.modelDiscovery2")}>
                      <button type="button" role="radio" aria-checked={editModelsMode === "url"} className={editModelsMode === "url" ? "selected" : ""} onClick={() => setEditModelsMode("url")}>
                        {localizedMessage(isZh, "v2.providers.autoDiscovery")}
                      </button>
                      <button type="button" role="radio" aria-checked={editModelsMode === "static"} className={editModelsMode === "static" ? "selected" : ""} onClick={() => setEditModelsMode("static")}>
                        {localizedMessage(isZh, "v2.providers.manualEntry")}
                      </button>
                    </div>
                  </div>
                  {editModelsMode === "url" ? (
                    <input
                      className="field-control"
                      placeholder={localizedMessage(isZh, "v2.providers.eGHttpsApiOpenaiComV1Models")}
                      aria-label={localizedMessage(isZh, "v2.providers.modelDiscovery2")}
                      value={editForm.models_url ?? ""}
                      onChange={(event) => setEditForm({ ...editForm, models_url: event.target.value })}
                    />
                  ) : (
                    <ModelTagInput
                      value={modelsArrayFromText(editForm.models) ?? []}
                      onChange={(models) => setEditForm((current) => ({ ...current, models: models.join("\n") }))}
                      inputLabel={localizedMessage(isZh, "v2.providers.manualModelInput")}
                      listLabel={localizedMessage(isZh, "v2.providers.manualModelList")}
                      placeholder={localizedMessage(isZh, "v2.providers.enterModelId")}
                      helpText={localizedMessage(isZh, "v2.providers.manualModelsHelp")}
                      removeLabel={(model) => localizedMessage(isZh, "v2.providers.removeModel", { model })}
                    />
                  )}
                </div>
              )}
            />
            {editError && <p className="provider-form-error">{editError}</p>}
          </ResourceEditorDrawer>
        );
      })}

      {/* SSE 测试/导入进度（§9.2 ⑤⑥）：步进面板 + 导入结果表 + 全量日志。 */}
      <ResourceEditorDialog
        open={testDialogOpen}
        title={
          testDialogMode === "create"
            ? localizedMessage(isZh, "providers.testDialog.create", { name: pendingCreateInput?.name ?? "" })
            : testDialogMode === "edit"
              ? localizedMessage(isZh, "providers.testDialog.edit", { name: editForm.name ?? "" })
              : testDialogMode === "route_import"
                ? localizedMessage(isZh, "providers.testDialog.import", { name: testTarget?.name ?? "" })
                : localizedMessage(isZh, "providers.testDialog.test", { name: testTarget?.name ?? "" })
        }
        description={
          testDialogMode === "create"
            ? (localizedMessage(isZh, "v2.providers.realTimePreCreateValidationPipeline"))
            : testDialogMode === "edit"
              ? (localizedMessage(isZh, "v2.providers.realTimePreSaveValidationPipeline"))
              : testDialogMode === "route_import"
                ? (localizedMessage(isZh, "v2.providers.realTimeProgressForRouteImport"))
                : (localizedMessage(isZh, "v2.providers.realTimeLogsForProviderTesting"))
        }
        onClose={closeTestDialog}
        footer={
          testDialogMode === "create" && createHealthPassed && pendingCreateInput ? (
            <button
              type="button"
              className="button button-primary"
              onClick={() => createMut.mutate(pendingCreateInput)}
              disabled={createMut.isPending}
            >
              {createMut.isPending
                ? (localizedMessage(isZh, "v2.providers.creating"))
                : (localizedMessage(isZh, "v2.providers.createProvider"))}
            </button>
          ) : testDialogMode === "edit" && editHealthPassed && pendingUpdateInput ? (
            <button
              type="button"
              className="button button-primary"
              onClick={() => updateMut.mutate(pendingUpdateInput)}
              disabled={updateMut.isPending}
            >
              {updateMut.isPending
                ? (localizedMessage(isZh, "v2.providers.saving"))
                : (localizedMessage(isZh, "v2.providers.saveProvider"))}
            </button>
          ) : (
            <button type="button" className="button button-secondary" onClick={closeTestDialog}>
              {isTestRunning
                ? (localizedMessage(isZh, "v2.providers.cancel"))
                : (localizedMessage(isZh, "v2.providers.close"))}
            </button>
          )
        }
      >
        <div className="probe-panel">
          <ProbeSteps steps={probeStepViews} waitingLabel={localizedMessage(isZh, "v2.providers.probeWaiting")} />
        </div>
        {testDialogMode === "route_import" && routeResults.length > 0 && (
          <div className="probe-panel">
            <table className="table provider-route-table">
              <thead>
                <tr>
                  <th>{localizedMessage(isZh, "v2.providers.model")}</th>
                  <th>{localizedMessage(isZh, "v2.providers.result")}</th>
                </tr>
              </thead>
              <tbody>
                {routeResults.map((row, index) => (
                  <tr key={`${row.model}-${index}`}>
                    <td><code className="code-pill">{row.model}</code></td>
                    <td>
                      {row.status === "created"
                        ? <span className="tag tag-success">{localizedMessage(isZh, "v2.providers.created")}</span>
                        : row.status === "skipped"
                          ? <span className="tag">{localizedMessage(isZh, "v2.providers.skipped")}</span>
                          : <span className="tag tag-danger">{localizedMessage(isZh, "v2.providers.failed2")}</span>}
                      {row.note ? <small className="cell-note">{row.note}</small> : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        <div className="probe-panel">
          <ProviderTestLog
            logs={testLogs}
            emptyLabel={localizedMessage(isZh, "v2.providers.waitingForTestToStart")}
            containerRef={logsContainerRef}
            statusLabel={isTestRunning
              ? localizedMessage(isZh, "v2.providers.inProgress")
              : localizedMessage(isZh, "v2.providers.finished")}
          />
        </div>
      </ResourceEditorDialog>

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
        confirmClassName="button-primary"
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
