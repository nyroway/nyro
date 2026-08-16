import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import type * as React from "react";
import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { normalizePublicGatewayURL } from "@/lib/public-gateway-url";
import { useLocale } from "@/lib/i18n";
import { runtimeHTTPURL, runtimeRedisURL } from "@/lib/runtime-service-url";
import type { RuntimeService } from "@/lib/types";
import { SETTINGS_SECTIONS, type SettingsSectionID } from "@/lib/settings-sections";
import {
  decodeRetryStatusCodes,
  encodeRetryStatusCodes,
  parseRetryStatusCodes,
  sameRetryStatusCodes,
} from "@/lib/retry-status-codes";
import { HelpCircle, Loader2, Save, X } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import {
  exportersFor,
  exporterKindLabel,
  exporterSettingKey,
  retentionSettingKey,
  settingKey,
  SIGNALS,
  type ExporterDef,
  type FieldDef,
  type Signal,
} from "@/lib/observability-schema";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { SettingsFormSurface } from "@/features/settings/settings-form-surface";
import { StateSettingsCard } from "@/features/settings/state-settings-card";
import { localizedMessage, type MessageKey } from "@/lib/messages";

const PROXY_REQUEST_TIMEOUT_KEY = "proxy.request_timeout";
const PROXY_CONNECT_TIMEOUT_KEY = "proxy.connect_timeout";
const PROXY_MAX_RETRIES_KEY = "proxy.max_retries";
const PROXY_RETRY_ON_STATUS_KEY = "proxy.retry_on_status";
const PROXY_MAX_BODY_BYTES_KEY = "proxy.max_body_bytes";
const PUBLIC_GATEWAY_URL_KEY = "gateway.public_url";

const PROXY_REQUEST_TIMEOUT_DEFAULT = "120s";
const PROXY_CONNECT_TIMEOUT_DEFAULT = "30s";
const PROXY_MAX_RETRIES_DEFAULT = "2";
const PROXY_MAX_BODY_BYTES_DEFAULT = "33554432";

const OBS_RETENTION_DEFAULT: Record<Signal, string> = {
  logs: "7",
  metrics: "30",
  traces: "3",
};

const OBS_SIGNAL_LABEL: Record<Signal, MessageKey> = {
  logs: "settings.signal.logs",
  metrics: "settings.signal.metrics",
  traces: "settings.signal.traces",
};

type ShowSettingsError = (titleKey: MessageKey, error: unknown, params?: Record<string, string | number>) => void;

const EMPTY_SELECT_SENTINEL = "__empty__";
const GO_DURATION_RE = /^(\d+(\.\d+)?(ns|µs|us|ms|s|m|h))+$/;

function emptySelectValue(value: string): string {
  return value === "" ? EMPTY_SELECT_SENTINEL : value;
}

function emptySelectState(value: string): string {
  return value === EMPTY_SELECT_SENTINEL ? "" : value;
}

function isValidGoDuration(value: string): boolean {
  const trimmed = value.trim();
  return !trimmed || GO_DURATION_RE.test(trimmed);
}

function HelpHint({ text }: { text: string }) {
  return (
    <TooltipProvider delayDuration={120}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            className="inline-flex h-4 w-4 items-center justify-center text-slate-400 hover:text-slate-600"
            aria-label="help"
          >
            <HelpCircle className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent side="top" className="max-w-xs">{text}</TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

function RetryStatusCodeInput({
  isZh,
  codes,
  draft,
  error,
  onDraftChange,
  onAdd,
  onRemove,
}: {
  isZh: boolean;
  codes: number[];
  draft: string;
  error: string | null;
  onDraftChange: (value: string) => void;
  onAdd: (input: string) => void;
  onRemove: (code: number) => void;
}) {
  function handleKeyDown(event: React.KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Enter") {
      event.preventDefault();
      onAdd(draft);
    }
  }
  function handlePaste(event: React.ClipboardEvent<HTMLInputElement>) {
    event.preventDefault();
    onAdd(event.clipboardData.getData("text"));
  }
  return (
    <div className="space-y-1.5">
      <div className="flex flex-wrap gap-1.5">
        {codes.map((code) => (
          <Badge key={code} variant="outline" className="gap-1 pr-1">
            {code}
            <button
              type="button"
              aria-label={`Remove ${code}`}
              className="inline-flex h-3.5 w-3.5 items-center justify-center rounded-full text-slate-400 hover:text-slate-700"
              onClick={() => onRemove(code)}
            >
              <X className="h-3 w-3" />
            </button>
          </Badge>
        ))}
      </div>
      <Input
        inputMode="numeric"
        placeholder={localizedMessage(isZh, "v2.settings.enterAStatusCode400599ThenPress")}
        value={draft}
        onChange={(e) => onDraftChange(e.target.value)}
        onKeyDown={handleKeyDown}
        onPaste={handlePaste}
        className={error ? "border-red-400 focus-visible:ring-red-400" : undefined}
      />
      {error && (
        <p className="text-xs text-red-600">
          {localizedMessage(isZh, "settings.invalidStatusCode", { value: error })}
        </p>
      )}
    </div>
  );
}

export default function SettingsPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
  const [activeSection, setActiveSection] = useState<SettingsSectionID>("forwarding");
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);

  const { data: proxyRequestTimeoutSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_REQUEST_TIMEOUT_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_REQUEST_TIMEOUT_KEY }),
  });
  const { data: proxyConnectTimeoutSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_CONNECT_TIMEOUT_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_CONNECT_TIMEOUT_KEY }),
  });
  const { data: proxyMaxRetriesSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_MAX_RETRIES_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_MAX_RETRIES_KEY }),
  });
  const { data: proxyRetryOnStatusSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_RETRY_ON_STATUS_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_RETRY_ON_STATUS_KEY }),
  });
  const { data: proxyMaxBodyBytesSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_MAX_BODY_BYTES_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_MAX_BODY_BYTES_KEY }),
  });

  const [proxyRequestTimeout, setProxyRequestTimeout] = useState("");
  const [proxyConnectTimeout, setProxyConnectTimeout] = useState("");
  const [proxyMaxRetries, setProxyMaxRetries] = useState("");
  const [proxyRetryStatusCodes, setProxyRetryStatusCodes] = useState<number[]>([]);
  const [retryStatusDraft, setRetryStatusDraft] = useState("");
  const [retryStatusError, setRetryStatusError] = useState<string | null>(null);
  const [proxyMaxBodyBytes, setProxyMaxBodyBytes] = useState("");

  const proxyBaseline = {
    requestTimeout: (proxyRequestTimeoutSetting ?? PROXY_REQUEST_TIMEOUT_DEFAULT).trim(),
    connectTimeout: (proxyConnectTimeoutSetting ?? PROXY_CONNECT_TIMEOUT_DEFAULT).trim(),
    maxRetries: (proxyMaxRetriesSetting ?? PROXY_MAX_RETRIES_DEFAULT).trim(),
    retryStatusCodes: decodeRetryStatusCodes(proxyRetryOnStatusSetting),
    maxBodyBytes: (proxyMaxBodyBytesSetting ?? PROXY_MAX_BODY_BYTES_DEFAULT).trim(),
  };
  const requestTimeoutInvalid = !isValidGoDuration(proxyRequestTimeout);
  const connectTimeoutInvalid = !isValidGoDuration(proxyConnectTimeout);
  const proxyDirty =
    proxyRequestTimeout.trim() !== proxyBaseline.requestTimeout
    || proxyConnectTimeout.trim() !== proxyBaseline.connectTimeout
    || proxyMaxRetries.trim() !== proxyBaseline.maxRetries
    || !sameRetryStatusCodes(proxyRetryStatusCodes, proxyBaseline.retryStatusCodes)
    || proxyMaxBodyBytes.trim() !== proxyBaseline.maxBodyBytes;

  function addRetryStatusCodes(input: string) {
    const result = parseRetryStatusCodes(input);
    if (result.invalid) {
      setRetryStatusError(result.invalid);
      return;
    }
    setProxyRetryStatusCodes((current) => [
      ...current,
      ...result.codes.filter((code) => !current.includes(code)),
    ]);
    setRetryStatusDraft("");
    setRetryStatusError(null);
  }

  function removeRetryStatusCode(code: number) {
    setProxyRetryStatusCodes((current) => current.filter((existing) => existing !== code));
  }

  useEffect(() => {
    setProxyRequestTimeout(proxyBaseline.requestTimeout);
    setProxyConnectTimeout(proxyBaseline.connectTimeout);
    setProxyMaxRetries(proxyBaseline.maxRetries);
    setProxyRetryStatusCodes(proxyBaseline.retryStatusCodes);
    setRetryStatusDraft("");
    setRetryStatusError(null);
    setProxyMaxBodyBytes(proxyBaseline.maxBodyBytes);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    proxyRequestTimeoutSetting,
    proxyConnectTimeoutSetting,
    proxyMaxRetriesSetting,
    proxyRetryOnStatusSetting,
    proxyMaxBodyBytesSetting,
  ]);

  function showErrorDialog(titleKey: MessageKey, error: unknown, params?: Record<string, string | number>) {
    setErrorDialog({
      title: localizedMessage(isZh, titleKey, params),
      description: localizeBackendErrorMessage(error, isZh),
    });
  }

  const saveProxyMut = useMutation({
    mutationFn: async () => {
      await Promise.all([
        backend("set_setting", { key: PROXY_REQUEST_TIMEOUT_KEY, value: proxyRequestTimeout.trim() || PROXY_REQUEST_TIMEOUT_DEFAULT }),
        backend("set_setting", { key: PROXY_CONNECT_TIMEOUT_KEY, value: proxyConnectTimeout.trim() || PROXY_CONNECT_TIMEOUT_DEFAULT }),
        backend("set_setting", { key: PROXY_MAX_RETRIES_KEY, value: proxyMaxRetries.trim() || PROXY_MAX_RETRIES_DEFAULT }),
        backend("set_setting", { key: PROXY_RETRY_ON_STATUS_KEY, value: encodeRetryStatusCodes(proxyRetryStatusCodes) }),
        backend("set_setting", { key: PROXY_MAX_BODY_BYTES_KEY, value: proxyMaxBodyBytes.trim() || PROXY_MAX_BODY_BYTES_DEFAULT }),
      ]);
    },
    onSuccess: () => {
      for (const key of [PROXY_REQUEST_TIMEOUT_KEY, PROXY_CONNECT_TIMEOUT_KEY, PROXY_MAX_RETRIES_KEY, PROXY_RETRY_ON_STATUS_KEY, PROXY_MAX_BODY_BYTES_KEY]) {
        qc.invalidateQueries({ queryKey: ["setting", key] });
      }
    },
    onError: (error: unknown) => showErrorDialog("settings.error.forwarding", error),
  });

  const { data: runtimeServices = [] } = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => backend("list_runtime_services"),
  });
  const builtInOtlpEndpoint = runtimeHTTPURL(
    runtimeServices.find((service) => service.id === "otlp-receiver" && service.status === "running")?.listen,
  );
  const builtInRedisURL = runtimeRedisURL(
    runtimeServices.find((service) => service.id === "redis-state" && service.status === "running")?.listen,
  );

  return (
    <PageLayout header={<PageHeader title={t("page.settings.title")} description={t("page.settings.subtitle")} />}>
      <div className="v2-settings-layout">
        <nav className="v2-settings-nav" aria-label={t("page.settings.title")}>
          <p>{t("settings.dataPlane")}</p>
          {SETTINGS_SECTIONS.filter((item) => item.group === "data-plane").map((item) => <SettingsNavButton key={item.id} active={activeSection === item.id} onClick={() => setActiveSection(item.id)}>{t(item.label)}</SettingsNavButton>)}
          <p>{t("settings.controlPlane")}</p>
          {SETTINGS_SECTIONS.filter((item) => item.group === "control-plane").map((item) => <SettingsNavButton key={item.id} active={activeSection === item.id} onClick={() => setActiveSection(item.id)}>{t(item.label)}</SettingsNavButton>)}
        </nav>

        <section className="v2-settings-content">
          {activeSection === "forwarding" && (
            <SettingsFormSurface title={localizedMessage(isZh, "v2.settings.forwardingSettings")} description={localizedMessage(isZh, "v2.settings.requestTimeoutsConnectionLimitsAndRetryPolicy")}>
              <div className="v2-setting-stack">
                <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
                  <div className="space-y-1.5">
                    <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.requestTimeout")}<HelpHint text={localizedMessage(isZh, "v2.settings.goDurationSyntaxEG120s2mMaps")} /></label>
                    <Input placeholder={PROXY_REQUEST_TIMEOUT_DEFAULT} value={proxyRequestTimeout} onChange={(e) => setProxyRequestTimeout(e.target.value)} className={requestTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
                    {requestTimeoutInvalid && <p className="text-xs text-red-600">{localizedMessage(isZh, "v2.settings.needsAUnitEG120s2m")}</p>}
                  </div>
                  <div className="space-y-1.5">
                    <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.connectTimeout")}<HelpHint text={localizedMessage(isZh, "v2.settings.goDurationSyntaxEG30sMapsTo")} /></label>
                    <Input placeholder={PROXY_CONNECT_TIMEOUT_DEFAULT} value={proxyConnectTimeout} onChange={(e) => setProxyConnectTimeout(e.target.value)} className={connectTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
                    {connectTimeoutInvalid && <p className="text-xs text-red-600">{localizedMessage(isZh, "v2.settings.needsAUnitEG30s1m")}</p>}
                  </div>
                  <div className="space-y-1.5"><label className="ml-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.maxRetries")}</label><Input type="number" min={0} placeholder={PROXY_MAX_RETRIES_DEFAULT} value={proxyMaxRetries} onChange={(e) => setProxyMaxRetries(e.target.value)} /></div>
                  <div className="space-y-1.5"><label className="ml-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.maxBodyBytes")}</label><Input type="number" min={1} placeholder={PROXY_MAX_BODY_BYTES_DEFAULT} value={proxyMaxBodyBytes} onChange={(e) => setProxyMaxBodyBytes(e.target.value)} /></div>
                  <div className="space-y-1.5 sm:col-span-2">
                    <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.retryStatusCodes")}<HelpHint text={localizedMessage(isZh, "v2.settings.mapsToProxyRetryOnStatusEG")} /></label>
                    <RetryStatusCodeInput isZh={isZh} codes={proxyRetryStatusCodes} draft={retryStatusDraft} error={retryStatusError} onDraftChange={setRetryStatusDraft} onAdd={addRetryStatusCodes} onRemove={removeRetryStatusCode} />
                  </div>
                </div>
                <div className="flex items-center gap-2">
                  <Button onClick={() => saveProxyMut.mutate()} disabled={saveProxyMut.isPending || !proxyDirty || requestTimeoutInvalid || connectTimeoutInvalid || retryStatusDraft.trim() !== ""} size="sm" className="flex items-center gap-1.5">{saveProxyMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}{localizedMessage(isZh, "v2.api-keys.save")}</Button>
                  {proxyDirty && <p className="text-xs text-amber-600">{localizedMessage(isZh, "v2.settings.saveToPublishToTheGatewayConfigurationStream")}</p>}
                </div>
              </div>
            </SettingsFormSurface>
          )}
          {activeSection === "state" && <StateSettingsCard isZh={isZh} onError={showErrorDialog} builtInRedisURL={builtInRedisURL} />}
          {SIGNALS.includes(activeSection as Signal) && <ObsSignalCard signal={activeSection as Signal} isZh={isZh} builtInOtlpEndpoint={builtInOtlpEndpoint} showErrorDialog={showErrorDialog} />}
          {activeSection === "public" && <PublicGatewayURLCard isZh={isZh} showErrorDialog={showErrorDialog} />}
          {activeSection === "retention" && <RetentionSettingsCard isZh={isZh} showErrorDialog={showErrorDialog} />}
        </section>
      </div>

      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => { if (!open) setErrorDialog(null); }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={localizedMessage(isZh, "v2.providers.ok")}
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}

function SettingsNavButton({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return <button type="button" className={active ? "active" : undefined} onClick={onClick}>{children}</button>;
}

function PublicGatewayURLCard({ isZh, showErrorDialog }: { isZh: boolean; showErrorDialog: ShowSettingsError }) {
  const { data: setting } = useQuery<string | null>({
    queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY],
    queryFn: () => backend("get_setting", { key: PUBLIC_GATEWAY_URL_KEY }),
  });
  return <PublicGatewayURLForm key={setting ?? ""} baseline={setting ?? ""} isZh={isZh} showErrorDialog={showErrorDialog} />;
}

function PublicGatewayURLForm({
  baseline,
  isZh,
  showErrorDialog,
}: {
  baseline: string;
  isZh: boolean;
  showErrorDialog: ShowSettingsError;
}) {
  const qc = useQueryClient();
  const [value, setValue] = useState(baseline);

  const normalized = normalizePublicGatewayURL(value);
  const invalid = normalized === null;
  const dirty = normalized !== null && normalized !== baseline;
  const saveMut = useMutation({
    mutationFn: () => backend("set_setting", { key: PUBLIC_GATEWAY_URL_KEY, value: normalized ?? "" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY] }),
    onError: (error: unknown) => showErrorDialog("settings.error.publicGateway", error),
  });

  return (
    <SettingsFormSurface title={localizedMessage(isZh, "v2.settings.publicGatewayUrl")} description={localizedMessage(isZh, "v2.settings.theClientFacingLbOrIngressRootUrl")}>
      <div className="space-y-1.5">
        <label className="ml-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.rootUrl")}</label>
        <Input placeholder="https://ai.example.com" value={value} onChange={(e) => setValue(e.target.value)} className={invalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
        {invalid && <p className="text-xs text-red-600">{localizedMessage(isZh, "v2.settings.enterAnHttpSRootUrlWithoutA")}</p>}
      </div>
      <div className="flex items-center gap-2">
        <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty || invalid} size="sm" className="flex items-center gap-1.5">
          {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </Button>
        {dirty && <p className="text-xs text-amber-600">{localizedMessage(isZh, "v2.settings.usedByControlPlaneConnectionGuidanceAfterSaving")}</p>}
      </div>
    </SettingsFormSurface>
  );
}

function RetentionSettingsCard({ isZh, showErrorDialog }: { isZh: boolean; showErrorDialog: ShowSettingsError }) {
  const qc = useQueryClient();
  const retentionKeys = useMemo(() => SIGNALS.map(retentionSettingKey), []);
  const retentionQueries = useQueries({
    queries: retentionKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => backend<string | null>("get_setting", { key }) })),
  });
  const retentionSettings = retentionQueries.map((query) => query.data ?? null);

  const retentionBaseline = useMemo(() => {
    const values = {} as Record<Signal, string>;
    SIGNALS.forEach((signal, index) => { values[signal] = retentionSettings[index]?.trim() || OBS_RETENTION_DEFAULT[signal]; });
    return values;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(retentionSettings)]);
  const [retention, setRetention] = useState<Record<Signal, string>>(retentionBaseline);
  useEffect(() => setRetention(retentionBaseline), [retentionBaseline]);

  const dirty = SIGNALS.some((signal) => retention[signal].trim() !== retentionBaseline[signal]);

  const saveMut = useMutation({
    mutationFn: async () => {
      await Promise.all(SIGNALS.map((signal) =>
        backend("set_setting", { key: retentionSettingKey(signal), value: retention[signal].trim() || OBS_RETENTION_DEFAULT[signal] }),
      ));
    },
    onSuccess: () => { for (const key of retentionKeys) qc.invalidateQueries({ queryKey: ["setting", key] }); },
    onError: (error: unknown) => showErrorDialog("settings.error.retention", error),
  });

  return (
    <SettingsFormSurface title={localizedMessage(isZh, "v2.settings.localTelemetryRetention")} description={localizedMessage(isZh, "v2.settings.retentionDaysForEachTelemetrySignalInThe")}>
      <div className="space-y-1.5">
        <p className="ml-1 text-xs font-medium text-slate-600">{localizedMessage(isZh, "v2.settings.retentionDays")}</p>
        <div className="grid grid-cols-3 gap-3">
          {SIGNALS.map((signal) => (
            <div key={signal} className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{localizedMessage(isZh, OBS_SIGNAL_LABEL[signal])}</label>
              <Input type="number" min={1} max={365} placeholder={OBS_RETENTION_DEFAULT[signal]} value={retention[signal]} onChange={(e) => setRetention((prev) => ({ ...prev, [signal]: e.target.value }))} />
            </div>
          ))}
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty} size="sm" className="flex items-center gap-1.5">
          {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </Button>
        {dirty && <p className="text-xs text-amber-600">{localizedMessage(isZh, "v2.settings.restartAdminToApply")}</p>}
      </div>
    </SettingsFormSurface>
  );
}

interface ObsSignalCardProps {
  signal: Signal;
  isZh: boolean;
  builtInOtlpEndpoint: string | null;
  showErrorDialog: ShowSettingsError;
}

function ObsSignalCard({ signal, isZh, builtInOtlpEndpoint, showErrorDialog }: ObsSignalCardProps) {
  const qc = useQueryClient();
  const defs = useMemo(() => exportersFor(signal), [signal]);
  const expKey = exporterSettingKey(signal);
  const fieldSlots = useMemo(() => {
    const slots: { kind: ExporterDef["kind"]; field: FieldDef; storageKey: string }[] = [];
    for (const def of defs) for (const field of def.fields) slots.push({ kind: def.kind, field, storageKey: settingKey(signal, def.kind, field.name) });
    return slots;
  }, [defs, signal]);
  const allKeys = useMemo(() => [expKey, ...fieldSlots.map((slot) => slot.storageKey)], [expKey, fieldSlots]);
  const queries = useQueries({
    queries: allKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => backend<string | null>("get_setting", { key }) })),
  });
  const exporterSetting = queries[0]?.data ?? null;
  const fieldSettings = fieldSlots.map((_, index) => queries[1 + index]?.data ?? null);
  const baselineExporter = exporterSetting ?? "";
  const baselineFields = useMemo(() => {
    const values: Record<string, string> = {};
    fieldSlots.forEach((slot, index) => { values[slot.field.name] = fieldSettings[index] ?? ""; });
    return values;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fieldSlots, JSON.stringify(fieldSettings)]);
  const [exporter, setExporter] = useState("");
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({});
  useEffect(() => {
    setExporter(baselineExporter);
    setFieldValues(baselineFields);
  }, [baselineExporter, baselineFields]);

  const activeDef = defs.find((def) => def.kind === exporter) ?? null;
  const activeFields = activeDef?.fields ?? [];
  const missingRequired = activeFields.some((field) => field.required && !(fieldValues[field.name] ?? "").trim());
  const dirty = exporter !== baselineExporter || activeFields.some((field) => (fieldValues[field.name] ?? "").trim() !== (baselineFields[field.name] ?? "").trim());
  const currentEndpoint = (fieldValues.endpoint ?? "").trim();
  const notBuiltIn = exporter !== "otlp" || currentEndpoint !== (builtInOtlpEndpoint ?? "").trim() || !builtInOtlpEndpoint;
  const saveMut = useMutation({
    mutationFn: async () => {
      const payload: Record<string, string> = { [expKey]: exporter };
      for (const field of activeFields) payload[settingKey(signal, exporter as ExporterDef["kind"], field.name)] = (fieldValues[field.name] ?? "").trim();
      await Promise.all(Object.entries(payload).map(([key, value]) => backend("set_setting", { key, value })));
      return payload;
    },
    onSuccess: (payload) => { for (const key of Object.keys(payload)) qc.invalidateQueries({ queryKey: ["setting", key] }); },
    onError: (error: unknown) => {
      showErrorDialog("settings.error.signalExport", error, { signal: localizedMessage(isZh, OBS_SIGNAL_LABEL[signal]) });
    },
  });
  const title = localizedMessage(isZh, OBS_SIGNAL_LABEL[signal]);

  return (
    <SettingsFormSurface title={title} description={localizedMessage(isZh, "v2.settings.exportedByGateway")} badge={<span className="v2-setting-badge">Gateway</span>}>
      <div className="v2-setting-stack">
        <div className="space-y-1.5">
          <label className="ml-1 text-xs text-slate-700">{localizedMessage(isZh, "v2.settings.exporter")}</label>
          <Select value={emptySelectValue(exporter)} onValueChange={(value) => setExporter(emptySelectState(value))}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={EMPTY_SELECT_SENTINEL}>{localizedMessage(isZh, "v2.settings.disabled")}</SelectItem>
              {defs.map((def) => <SelectItem key={def.kind} value={def.kind}>{exporterKindLabel(def.kind)}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        {activeFields.map((field) => {
          const value = fieldValues[field.name] ?? "";
          const invalid = Boolean(field.required) && !value.trim();
          return (
            <div key={field.name} className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{field.label}{field.required ? " *" : ""}</label>
              {field.type === "select" ? (
                <Select value={value || field.default || field.options?.[0] || ""} onValueChange={(next) => setFieldValues((prev) => ({ ...prev, [field.name]: next }))}>
                  <SelectTrigger><SelectValue /></SelectTrigger>
                  <SelectContent>{(field.options ?? []).map((option) => <SelectItem key={option} value={option}>{option}</SelectItem>)}</SelectContent>
                </Select>
              ) : (
                <div className="flex items-center gap-2">
                  <Input placeholder={field.default || undefined} value={value} onChange={(e) => setFieldValues((prev) => ({ ...prev, [field.name]: e.target.value }))} className={invalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
                  {activeDef?.kind === "otlp" && field.name === "endpoint" && (
                    <Button type="button" variant="secondary" size="sm" disabled={!builtInOtlpEndpoint} onClick={() => setFieldValues((prev) => ({ ...prev, endpoint: builtInOtlpEndpoint ?? "" }))} className="whitespace-nowrap">
                      {localizedMessage(isZh, "v2.settings.useBuiltIn")}
                    </Button>
                  )}
                </div>
              )}
              {invalid && <p className="text-xs text-red-600">{localizedMessage(isZh, "v2.settings.thisFieldIsRequired")}</p>}
            </div>
          );
        })}
        {!builtInOtlpEndpoint && activeDef?.kind === "otlp" && <p className="text-xs text-slate-500">{localizedMessage(isZh, "v2.settings.theBuiltInAddressCanTBeAuto")}</p>}
        {notBuiltIn && <p className="text-xs text-amber-600">{localizedMessage(isZh, "v2.settings.thisSignalIsnTWritingToBuiltIn")}</p>}
        <div className="flex items-center gap-2">
          <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty || missingRequired} size="sm" className="flex items-center gap-1.5">
            {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
            {localizedMessage(isZh, "v2.api-keys.save")}
          </Button>
          {dirty && <p className="text-xs text-amber-600">{localizedMessage(isZh, "v2.settings.restartGatewayToApply")}</p>}
        </div>
      </div>
    </SettingsFormSurface>
  );
}
