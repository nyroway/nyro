import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { Fragment, useEffect, useMemo, useState, type ClipboardEvent, type KeyboardEvent } from "react";

import { SettingsFormSurface } from "@/features/settings/settings-form-surface";
import { reportSettingsError, reportSettingsSaved } from "@/features/settings/settings-feedback";
import { StateSettingsCard } from "@/features/settings/state-settings-card";
import { NyroHelpHint, NyroSearchSelect, NyroTextField } from "@/components/ui/nyro-fields";
import { Notice } from "@/components/v2/notice";
import { PageHeader } from "@/components/v2/page-header";
import { PageLayout } from "@/components/v2/page-layout";
import { settingsApi } from "@/lib/api/settings";
import { systemApi } from "@/lib/api/system";
import { useLocale } from "@/lib/i18n";
import { localizedMessage, type MessageKey } from "@/lib/messages";
import { normalizePublicGatewayURL } from "@/lib/public-gateway-url";
import {
  decodeRetryStatusCodes,
  encodeRetryStatusCodes,
  parseRetryStatusCodes,
  sameRetryStatusCodes,
} from "@/lib/retry-status-codes";
import { runtimeHTTPURL, runtimeRedisURL } from "@/lib/runtime-service-url";
import { SETTINGS_SECTIONS, type SettingsSectionID } from "@/lib/settings-sections";
import type { RuntimeService } from "@/lib/types";
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

const GO_DURATION_RE = /^(\d+(\.\d+)?(ns|µs|us|ms|s|m|h))+$/;

function isValidGoDuration(value: string): boolean {
  const trimmed = value.trim();
  return !trimmed || GO_DURATION_RE.test(trimmed);
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
  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key === "Enter") {
      event.preventDefault();
      onAdd(draft);
    }
  }
  function handlePaste(event: ClipboardEvent<HTMLInputElement>) {
    event.preventDefault();
    onAdd(event.clipboardData.getData("text"));
  }
  return (
    <div className="field full">
      <span className="field-label">
        {localizedMessage(isZh, "v2.settings.retryStatusCodes")}
        <NyroHelpHint text={localizedMessage(isZh, "v2.settings.mapsToProxyRetryOnStatusEG")} />
      </span>
      <div className="field-control tags-control">
        {codes.map((code) => (
          <span className="tag" key={code}>
            {code}
            <button type="button" aria-label={`Remove ${code}`} onClick={() => onRemove(code)}>×</button>
          </span>
        ))}
        <input
          className="tag-input"
          inputMode="numeric"
          placeholder={localizedMessage(isZh, "v2.settings.enterAStatusCode400599ThenPress")}
          value={draft}
          onChange={(e) => onDraftChange(e.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
        />
      </div>
      {error && (
        <span className="field-hint error">
          {localizedMessage(isZh, "settings.invalidStatusCode", { value: error })}
        </span>
      )}
    </div>
  );
}

export default function SettingsPage() {
  const { locale, t } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
  const [activeSection, setActiveSection] = useState<SettingsSectionID>("forwarding");

  const { data: proxyRequestTimeoutSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_REQUEST_TIMEOUT_KEY],
    queryFn: () => settingsApi.get(PROXY_REQUEST_TIMEOUT_KEY),
  });
  const { data: proxyConnectTimeoutSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_CONNECT_TIMEOUT_KEY],
    queryFn: () => settingsApi.get(PROXY_CONNECT_TIMEOUT_KEY),
  });
  const { data: proxyMaxRetriesSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_MAX_RETRIES_KEY],
    queryFn: () => settingsApi.get(PROXY_MAX_RETRIES_KEY),
  });
  const { data: proxyRetryOnStatusSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_RETRY_ON_STATUS_KEY],
    queryFn: () => settingsApi.get(PROXY_RETRY_ON_STATUS_KEY),
  });
  const { data: proxyMaxBodyBytesSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_MAX_BODY_BYTES_KEY],
    queryFn: () => settingsApi.get(PROXY_MAX_BODY_BYTES_KEY),
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

  const saveProxyMut = useMutation({
    mutationFn: async () => {
      await Promise.all([
        settingsApi.set(PROXY_REQUEST_TIMEOUT_KEY, proxyRequestTimeout.trim() || PROXY_REQUEST_TIMEOUT_DEFAULT),
        settingsApi.set(PROXY_CONNECT_TIMEOUT_KEY, proxyConnectTimeout.trim() || PROXY_CONNECT_TIMEOUT_DEFAULT),
        settingsApi.set(PROXY_MAX_RETRIES_KEY, proxyMaxRetries.trim() || PROXY_MAX_RETRIES_DEFAULT),
        settingsApi.set(PROXY_RETRY_ON_STATUS_KEY, encodeRetryStatusCodes(proxyRetryStatusCodes)),
        settingsApi.set(PROXY_MAX_BODY_BYTES_KEY, proxyMaxBodyBytes.trim() || PROXY_MAX_BODY_BYTES_DEFAULT),
      ]);
    },
    onSuccess: () => {
      for (const key of [PROXY_REQUEST_TIMEOUT_KEY, PROXY_CONNECT_TIMEOUT_KEY, PROXY_MAX_RETRIES_KEY, PROXY_RETRY_ON_STATUS_KEY, PROXY_MAX_BODY_BYTES_KEY]) {
        qc.invalidateQueries({ queryKey: ["setting", key] });
      }
      reportSettingsSaved(isZh);
    },
    onError: (error: unknown) => reportSettingsError(isZh, "settings.error.forwarding", error),
  });

  const { data: runtimeServices = [] } = useQuery<RuntimeService[]>({
    queryKey: ["runtime-services"],
    queryFn: () => systemApi.runtimeServices(),
  });
  const builtInOtlpEndpoint = runtimeHTTPURL(
    runtimeServices.find((service) => service.id === "otlp-receiver" && service.status === "running")?.listen,
  );
  const builtInRedisURL = runtimeRedisURL(
    runtimeServices.find((service) => service.id === "redis-state" && service.status === "running")?.listen,
  );

  return (
    <PageLayout header={<PageHeader title={t("page.settings.title")} description={t("page.settings.subtitle")} />}>
      <div className="settings-layout">
        <nav className="settings-nav" aria-label={t("page.settings.title")}>
          {(["data-plane", "control-plane"] as const).map((group) => (
            <Fragment key={group}>
              <p className="nav-label">{t(group === "data-plane" ? "settings.dataPlane" : "settings.controlPlane")}</p>
              {SETTINGS_SECTIONS.filter((item) => item.group === group).map((item) => (
                <button
                  key={item.id}
                  type="button"
                  className={`nav-item${activeSection === item.id ? " active" : ""}`}
                  onClick={() => setActiveSection(item.id)}
                >
                  {t(item.label)}
                </button>
              ))}
            </Fragment>
          ))}
        </nav>

        <section>
          {activeSection === "forwarding" && (
            <SettingsFormSurface
              title={localizedMessage(isZh, "v2.settings.forwardingSettings")}
              description={localizedMessage(isZh, "v2.settings.requestTimeoutsConnectionLimitsAndRetryPolicy")}
            >
              <div className="form-grid">
                <NyroTextField
                  fullWidth={false}
                  label={localizedMessage(isZh, "v2.settings.requestTimeout")}
                  help={localizedMessage(isZh, "v2.settings.goDurationSyntaxEG120s2mMaps")}
                  placeholder={PROXY_REQUEST_TIMEOUT_DEFAULT}
                  value={proxyRequestTimeout}
                  onChange={(e) => setProxyRequestTimeout(e.target.value)}
                  error={requestTimeoutInvalid ? localizedMessage(isZh, "v2.settings.needsAUnitEG120s2m") : undefined}
                />
                <NyroTextField
                  fullWidth={false}
                  label={localizedMessage(isZh, "v2.settings.connectTimeout")}
                  help={localizedMessage(isZh, "v2.settings.goDurationSyntaxEG30sMapsTo")}
                  placeholder={PROXY_CONNECT_TIMEOUT_DEFAULT}
                  value={proxyConnectTimeout}
                  onChange={(e) => setProxyConnectTimeout(e.target.value)}
                  error={connectTimeoutInvalid ? localizedMessage(isZh, "v2.settings.needsAUnitEG30s1m") : undefined}
                />
                <NyroTextField
                  fullWidth={false}
                  type="number"
                  min={0}
                  label={localizedMessage(isZh, "v2.settings.maxRetries")}
                  placeholder={PROXY_MAX_RETRIES_DEFAULT}
                  value={proxyMaxRetries}
                  onChange={(e) => setProxyMaxRetries(e.target.value)}
                />
                <NyroTextField
                  fullWidth={false}
                  type="number"
                  min={1}
                  label={localizedMessage(isZh, "v2.settings.maxBodyBytes")}
                  placeholder={PROXY_MAX_BODY_BYTES_DEFAULT}
                  value={proxyMaxBodyBytes}
                  onChange={(e) => setProxyMaxBodyBytes(e.target.value)}
                />
                <RetryStatusCodeInput
                  isZh={isZh}
                  codes={proxyRetryStatusCodes}
                  draft={retryStatusDraft}
                  error={retryStatusError}
                  onDraftChange={setRetryStatusDraft}
                  onAdd={addRetryStatusCodes}
                  onRemove={removeRetryStatusCode}
                />
              </div>
              <div className="config-actions">
                {proxyDirty && (
                  <span className="config-note">
                    {localizedMessage(isZh, "v2.settings.saveToPublishToTheGatewayConfigurationStream")}
                  </span>
                )}
                <button
                  type="button"
                  className="button button-primary button-sm"
                  disabled={saveProxyMut.isPending || !proxyDirty || requestTimeoutInvalid || connectTimeoutInvalid || retryStatusDraft.trim() !== ""}
                  onClick={() => saveProxyMut.mutate()}
                >
                  {saveProxyMut.isPending && <span className="spinner" aria-hidden="true" />}
                  {localizedMessage(isZh, "v2.api-keys.save")}
                </button>
              </div>
            </SettingsFormSurface>
          )}
          {activeSection === "state" && <StateSettingsCard isZh={isZh} builtInRedisURL={builtInRedisURL} />}
          {SIGNALS.includes(activeSection as Signal) && <ObsSignalCard signal={activeSection as Signal} isZh={isZh} builtInOtlpEndpoint={builtInOtlpEndpoint} />}
          {activeSection === "public" && <PublicGatewayURLCard isZh={isZh} />}
          {activeSection === "retention" && <RetentionSettingsCard isZh={isZh} />}
        </section>
      </div>
    </PageLayout>
  );
}

function PublicGatewayURLCard({ isZh }: { isZh: boolean }) {
  const { data: setting } = useQuery<string | null>({
    queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY],
    queryFn: () => settingsApi.get(PUBLIC_GATEWAY_URL_KEY),
  });
  return <PublicGatewayURLForm key={setting ?? ""} baseline={setting ?? ""} isZh={isZh} />;
}

function PublicGatewayURLForm({ baseline, isZh }: { baseline: string; isZh: boolean }) {
  const qc = useQueryClient();
  const [value, setValue] = useState(baseline);

  const normalized = normalizePublicGatewayURL(value);
  const invalid = normalized === null;
  const dirty = normalized !== null && normalized !== baseline;
  const saveMut = useMutation({
    mutationFn: () => settingsApi.set(PUBLIC_GATEWAY_URL_KEY, normalized ?? ""),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY] });
      reportSettingsSaved(isZh);
    },
    onError: (error: unknown) => reportSettingsError(isZh, "settings.error.publicGateway", error),
  });

  return (
    <SettingsFormSurface
      title={localizedMessage(isZh, "v2.settings.publicGatewayUrl")}
      description={localizedMessage(isZh, "v2.settings.theClientFacingLbOrIngressRootUrl")}
    >
      <div className="form-grid">
        <NyroTextField
          label={localizedMessage(isZh, "v2.settings.rootUrl")}
          placeholder="https://ai.example.com"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          error={invalid ? localizedMessage(isZh, "v2.settings.enterAnHttpSRootUrlWithoutA") : undefined}
        />
      </div>
      <div className="config-actions">
        {dirty && (
          <span className="config-note">
            {localizedMessage(isZh, "v2.settings.usedByControlPlaneConnectionGuidanceAfterSaving")}
          </span>
        )}
        <button
          type="button"
          className="button button-primary button-sm"
          disabled={saveMut.isPending || !dirty || invalid}
          onClick={() => saveMut.mutate()}
        >
          {saveMut.isPending && <span className="spinner" aria-hidden="true" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </button>
      </div>
    </SettingsFormSurface>
  );
}

function RetentionSettingsCard({ isZh }: { isZh: boolean }) {
  const qc = useQueryClient();
  const retentionKeys = useMemo(() => SIGNALS.map(retentionSettingKey), []);
  const retentionQueries = useQueries({
    queries: retentionKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => settingsApi.get(key) })),
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
        settingsApi.set(retentionSettingKey(signal), retention[signal].trim() || OBS_RETENTION_DEFAULT[signal]),
      ));
    },
    onSuccess: () => {
      for (const key of retentionKeys) qc.invalidateQueries({ queryKey: ["setting", key] });
      reportSettingsSaved(isZh);
    },
    onError: (error: unknown) => reportSettingsError(isZh, "settings.error.retention", error),
  });

  return (
    <SettingsFormSurface
      title={localizedMessage(isZh, "v2.settings.localTelemetryRetention")}
      description={localizedMessage(isZh, "v2.settings.retentionDaysForEachTelemetrySignalInThe")}
    >
      <p className="form-section-title">{localizedMessage(isZh, "v2.settings.retentionDays")}</p>
      <div className="form-grid cols-3">
        {SIGNALS.map((signal) => (
          <NyroTextField
            key={signal}
            fullWidth={false}
            type="number"
            min={1}
            max={365}
            label={localizedMessage(isZh, OBS_SIGNAL_LABEL[signal])}
            placeholder={OBS_RETENTION_DEFAULT[signal]}
            value={retention[signal]}
            onChange={(e) => setRetention((prev) => ({ ...prev, [signal]: e.target.value }))}
          />
        ))}
      </div>
      <div className="config-actions">
        {dirty && (
          <span className="config-note">{localizedMessage(isZh, "v2.settings.restartAdminToApply")}</span>
        )}
        <button
          type="button"
          className="button button-primary button-sm"
          disabled={saveMut.isPending || !dirty}
          onClick={() => saveMut.mutate()}
        >
          {saveMut.isPending && <span className="spinner" aria-hidden="true" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </button>
      </div>
    </SettingsFormSurface>
  );
}

function ObsSignalCard({
  signal,
  isZh,
  builtInOtlpEndpoint,
}: {
  signal: Signal;
  isZh: boolean;
  builtInOtlpEndpoint: string | null;
}) {
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
    queries: allKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => settingsApi.get(key) })),
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
      await Promise.all(Object.entries(payload).map(([key, value]) => settingsApi.set(key, value)));
      return payload;
    },
    onSuccess: (payload) => { for (const key of Object.keys(payload)) qc.invalidateQueries({ queryKey: ["setting", key] }); reportSettingsSaved(isZh); },
    onError: (error: unknown) => {
      reportSettingsError(isZh, "settings.error.signalExport", error, { signal: localizedMessage(isZh, OBS_SIGNAL_LABEL[signal]) });
    },
  });
  const title = localizedMessage(isZh, OBS_SIGNAL_LABEL[signal]);

  return (
    <SettingsFormSurface
      title={title}
      description={localizedMessage(isZh, "v2.settings.exportedByGateway")}
      badge={<span className="tag">Gateway</span>}
    >
      <div className="form-grid">
        <NyroSearchSelect<ExporterDef | null>
          fullWidth={false}
          label={localizedMessage(isZh, "v2.settings.exporter")}
          options={[null, ...defs]}
          value={activeDef}
          onChange={(option) => setExporter(option?.kind ?? "")}
          getOptionLabel={(option) => option === null
            ? localizedMessage(isZh, "v2.settings.disabled")
            : exporterKindLabel(option.kind)}
          searchable={false}
        />
        {activeFields.map((field) => {
          const value = fieldValues[field.name] ?? "";
          const invalid = Boolean(field.required) && !value.trim();
          if (field.type === "select") {
            return (
              <NyroSearchSelect<string>
                key={field.name}
                fullWidth={false}
                label={field.label}
                required={field.required}
                options={field.options ?? []}
                value={value || field.default || field.options?.[0] || ""}
                onChange={(next) => setFieldValues((prev) => ({ ...prev, [field.name]: next ?? "" }))}
                getOptionLabel={(option) => option}
                searchable={false}
              />
            );
          }
          const showBuiltIn = activeDef?.kind === "otlp" && field.name === "endpoint";
          return (
            <div key={field.name} className="field">
              <label className="field-label">
                {field.label}
                {field.required && <span className="required" aria-hidden="true">*</span>}
              </label>
              <div className={`field-control${invalid ? " is-error" : ""}`}>
                <input
                  placeholder={field.default || undefined}
                  value={value}
                  onChange={(e) => setFieldValues((prev) => ({ ...prev, [field.name]: e.target.value }))}
                />
                {showBuiltIn && (
                  <button
                    type="button"
                    className="button button-text button-sm"
                    disabled={!builtInOtlpEndpoint}
                    onClick={() => setFieldValues((prev) => ({ ...prev, endpoint: builtInOtlpEndpoint ?? "" }))}
                  >
                    {localizedMessage(isZh, "v2.settings.useBuiltIn")}
                  </button>
                )}
              </div>
              {invalid && (
                <span className="field-hint error">{localizedMessage(isZh, "v2.settings.thisFieldIsRequired")}</span>
              )}
            </div>
          );
        })}
      </div>
      {!builtInOtlpEndpoint && activeDef?.kind === "otlp" && (
        <Notice tone="info">{localizedMessage(isZh, "v2.settings.theBuiltInAddressCanTBeAuto")}</Notice>
      )}
      {notBuiltIn && (
        <Notice tone="warning">{localizedMessage(isZh, "v2.settings.thisSignalIsnTWritingToBuiltIn")}</Notice>
      )}
      <div className="config-actions">
        {dirty && (
          <span className="config-note">{localizedMessage(isZh, "v2.settings.restartGatewayToApply")}</span>
        )}
        <button
          type="button"
          className="button button-primary button-sm"
          disabled={saveMut.isPending || !dirty || missingRequired}
          onClick={() => saveMut.mutate()}
        >
          {saveMut.isPending && <span className="spinner" aria-hidden="true" />}
          {localizedMessage(isZh, "v2.api-keys.save")}
        </button>
      </div>
    </SettingsFormSurface>
  );
}
