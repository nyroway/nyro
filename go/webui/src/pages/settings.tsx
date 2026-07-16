import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState } from "react";
import type * as React from "react";
import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { normalizePublicGatewayURL } from "@/lib/public-gateway-url";
import { useLocale } from "@/lib/i18n";
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
  flushIntervalSettingKey,
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

// Admin-local flush cadence (per signal): how often the receiver persists that
// signal's buffered rows to parquet — the time trigger complementing the sink's
// size trigger. A sibling of the per-signal retention settings, same admin-local
// storage tier, applied at boot.
const OBS_FLUSH_DEFAULT = "5s";

const OBS_SIGNAL_LABEL: Record<Signal, { zh: string; en: string }> = {
  logs: { zh: "日志", en: "Logs" },
  metrics: { zh: "指标", en: "Metrics" },
  traces: { zh: "链路追踪", en: "Traces" },
};

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
        placeholder={isZh ? "输入状态码（400–599），按 Enter 添加" : "Enter a status code (400–599), then press Enter."}
        value={draft}
        onChange={(e) => onDraftChange(e.target.value)}
        onKeyDown={handleKeyDown}
        onPaste={handlePaste}
        className={error ? "border-red-400 focus-visible:ring-red-400" : undefined}
      />
      {error && (
        <p className="text-xs text-red-600">
          {isZh ? `“${error}” 不是有效的状态码，请输入 400–599 之间的整数` : `"${error}" is not a valid status code. Enter an integer between 400–599.`}
        </p>
      )}
    </div>
  );
}

function SettingsSection({
  eyebrow,
  title,
  description,
  appliesTo,
}: {
  eyebrow: string;
  title: string;
  description: string;
  appliesTo: string;
}) {
  return (
    <div className="flex flex-wrap items-end justify-between gap-3 border-b border-slate-200/80 pb-3">
      <div>
        <p className="text-[11px] font-semibold tracking-[0.18em] text-slate-500">{eyebrow}</p>
        <h2 className="mt-1 text-lg font-semibold text-slate-900">{title}</h2>
        <p className="mt-1 text-sm text-slate-500">{description}</p>
      </div>
      <span className="rounded-full border border-slate-200 bg-slate-50 px-2.5 py-1 text-xs font-medium text-slate-600">
        {appliesTo}
      </span>
    </div>
  );
}

export default function SettingsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const qc = useQueryClient();
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

  function showErrorDialog(titleZh: string, titleEn: string, error: unknown) {
    setErrorDialog({
      title: isZh ? titleZh : titleEn,
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
    onError: (error: unknown) => showErrorDialog("保存转发参数失败", "Failed to save forwarding settings", error),
  });

  const builtInOtlpEndpoint = !IS_TAURI && typeof window !== "undefined" ? window.location.origin : null;

  return (
    <div className="space-y-8">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "设置" : "Settings"}</h1>
        <p className="mt-1 text-sm text-slate-500">{isZh ? "按生效对象管理网关与控制面配置" : "Manage configuration by the process it affects"}</p>
      </div>

      <section className="space-y-5">
        <SettingsSection
          eyebrow="DATA PLANE"
          title={isZh ? "数据面" : "Data Plane"}
          description={isZh ? "转发与遥测导出配置" : "Forwarding and telemetry export configuration"}
          appliesTo={isZh ? "作用于 Gateway" : "Applies to Gateway"}
        />

        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
          <div className="glass rounded-2xl p-6 space-y-5">
          <h3 className="text-lg font-semibold text-slate-900">{isZh ? "转发参数" : "Forwarding Settings"}</h3>
          <div className="rounded-xl bg-slate-50 p-4 space-y-3">
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "请求超时" : "Request Timeout"}
                  <HelpHint text={isZh ? "Go duration 格式，如 120s、2m。对应 proxy.request_timeout" : "Go duration syntax, e.g. 120s, 2m. Maps to proxy.request_timeout"} />
                </label>
                <Input placeholder={PROXY_REQUEST_TIMEOUT_DEFAULT} value={proxyRequestTimeout} onChange={(e) => setProxyRequestTimeout(e.target.value)} className={requestTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
                {requestTimeoutInvalid && <p className="text-xs text-red-600">{isZh ? "需要带单位，如 120s、2m" : "Needs a unit, e.g. 120s, 2m"}</p>}
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "连接超时" : "Connect Timeout"}
                  <HelpHint text={isZh ? "Go duration 格式，如 30s。对应 proxy.connect_timeout" : "Go duration syntax, e.g. 30s. Maps to proxy.connect_timeout"} />
                </label>
                <Input placeholder={PROXY_CONNECT_TIMEOUT_DEFAULT} value={proxyConnectTimeout} onChange={(e) => setProxyConnectTimeout(e.target.value)} className={connectTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
                {connectTimeoutInvalid && <p className="text-xs text-red-600">{isZh ? "需要带单位，如 30s、1m" : "Needs a unit, e.g. 30s, 1m"}</p>}
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "最大重试次数" : "Max Retries"}</label>
                <Input type="number" min={0} placeholder={PROXY_MAX_RETRIES_DEFAULT} value={proxyMaxRetries} onChange={(e) => setProxyMaxRetries(e.target.value)} />
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "最大请求体（字节）" : "Max Body Bytes"}</label>
                <Input type="number" min={1} placeholder={PROXY_MAX_BODY_BYTES_DEFAULT} value={proxyMaxBodyBytes} onChange={(e) => setProxyMaxBodyBytes(e.target.value)} />
              </div>
              <div className="space-y-1.5 sm:col-span-2">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "重试状态码" : "Retry Status Codes"}
                  <HelpHint text={isZh ? "对应 proxy.retry_on_status，例如 429,500,502,503,504" : "Maps to proxy.retry_on_status, e.g. 429,500,502,503,504"} />
                </label>
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
            </div>
            <div className="flex items-center gap-2">
              <Button onClick={() => saveProxyMut.mutate()} disabled={saveProxyMut.isPending || !proxyDirty || requestTimeoutInvalid || connectTimeoutInvalid || retryStatusDraft.trim() !== ""} size="sm" className="flex items-center gap-1.5">
                {saveProxyMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
                {isZh ? "保存" : "Save"}
              </Button>
              {proxyDirty && <p className="text-xs text-amber-600">{isZh ? "保存后立即推送到 Gateway 配置流" : "Save to publish to the Gateway configuration stream"}</p>}
            </div>
          </div>
          </div>
          {SIGNALS.map((signal) => (
            <ObsSignalCard key={signal} signal={signal} isZh={isZh} builtInOtlpEndpoint={builtInOtlpEndpoint} showErrorDialog={showErrorDialog} />
          ))}
        </div>
      </section>

      <section className="space-y-5">
        <SettingsSection
          eyebrow="CONTROL PLANE"
          title={isZh ? "控制面" : "Control Plane"}
          description={isZh ? "管理端入口与本地遥测存储" : "Admin entrypoint and local telemetry storage"}
          appliesTo={isZh ? "作用于 Admin" : "Applies to Admin"}
        />
        <div className="grid grid-cols-1 gap-5 lg:grid-cols-2">
          <PublicGatewayURLCard isZh={isZh} showErrorDialog={showErrorDialog} />
          <RetentionSettingsCard isZh={isZh} showErrorDialog={showErrorDialog} />
        </div>
      </section>

      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => { if (!open) setErrorDialog(null); }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={isZh ? "我知道了" : "OK"}
        onConfirm={() => setErrorDialog(null)}
      />
    </div>
  );
}

function PublicGatewayURLCard({ isZh, showErrorDialog }: { isZh: boolean; showErrorDialog: (titleZh: string, titleEn: string, error: unknown) => void }) {
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
  showErrorDialog: (titleZh: string, titleEn: string, error: unknown) => void;
}) {
  const qc = useQueryClient();
  const [value, setValue] = useState(baseline);

  const normalized = normalizePublicGatewayURL(value);
  const invalid = normalized === null;
  const dirty = normalized !== null && normalized !== baseline;
  const saveMut = useMutation({
    mutationFn: () => backend("set_setting", { key: PUBLIC_GATEWAY_URL_KEY, value: normalized ?? "" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["setting", PUBLIC_GATEWAY_URL_KEY] }),
    onError: (error: unknown) => showErrorDialog("保存公开网关地址失败", "Failed to save public gateway URL", error),
  });

  return (
    <div className="glass rounded-2xl p-6 space-y-4">
      <div>
        <h3 className="text-lg font-semibold text-slate-900">{isZh ? "公开网关地址" : "Public Gateway URL"}</h3>
        <p className="mt-1 text-sm text-slate-500">{isZh ? "客户端访问集群的 LB 或 Ingress 根地址；不参与节点路由。" : "The client-facing LB or Ingress root URL; it does not route individual nodes."}</p>
      </div>
      <div className="space-y-1.5">
        <label className="ml-1 text-xs text-slate-700">{isZh ? "根地址" : "Root URL"}</label>
        <Input placeholder="https://ai.example.com" value={value} onChange={(e) => setValue(e.target.value)} className={invalid ? "border-red-400 focus-visible:ring-red-400" : undefined} />
        {invalid && <p className="text-xs text-red-600">{isZh ? "请输入不含路径的 http(s) 根地址。" : "Enter an HTTP(S) root URL without a path."}</p>}
      </div>
      <div className="flex items-center gap-2">
        <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty || invalid} size="sm" className="flex items-center gap-1.5">
          {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {isZh ? "保存" : "Save"}
        </Button>
        {dirty && <p className="text-xs text-amber-600">{isZh ? "保存后供管理端接入信息使用" : "Used by control-plane connection guidance after saving"}</p>}
      </div>
    </div>
  );
}

function RetentionSettingsCard({ isZh, showErrorDialog }: { isZh: boolean; showErrorDialog: (titleZh: string, titleEn: string, error: unknown) => void }) {
  const qc = useQueryClient();
  const retentionKeys = useMemo(() => SIGNALS.map(retentionSettingKey), []);
  const flushKeys = useMemo(() => SIGNALS.map(flushIntervalSettingKey), []);
  const retentionQueries = useQueries({
    queries: retentionKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => backend<string | null>("get_setting", { key }) })),
  });
  const flushQueries = useQueries({
    queries: flushKeys.map((key) => ({ queryKey: ["setting", key], queryFn: () => backend<string | null>("get_setting", { key }) })),
  });
  const retentionSettings = retentionQueries.map((query) => query.data ?? null);
  const flushSettings = flushQueries.map((query) => query.data ?? null);

  const retentionBaseline = useMemo(() => {
    const values = {} as Record<Signal, string>;
    SIGNALS.forEach((signal, index) => { values[signal] = retentionSettings[index]?.trim() || OBS_RETENTION_DEFAULT[signal]; });
    return values;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(retentionSettings)]);
  const flushBaseline = useMemo(() => {
    const values = {} as Record<Signal, string>;
    SIGNALS.forEach((signal, index) => { values[signal] = flushSettings[index]?.trim() || OBS_FLUSH_DEFAULT; });
    return values;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [JSON.stringify(flushSettings)]);

  const [retention, setRetention] = useState<Record<Signal, string>>(retentionBaseline);
  const [flush, setFlush] = useState<Record<Signal, string>>(flushBaseline);
  useEffect(() => setRetention(retentionBaseline), [retentionBaseline]);
  useEffect(() => setFlush(flushBaseline), [flushBaseline]);

  const dirty = SIGNALS.some((signal) => retention[signal].trim() !== retentionBaseline[signal] || flush[signal].trim() !== flushBaseline[signal]);
  const flushInvalid = SIGNALS.some((signal) => !GO_DURATION_RE.test(flush[signal].trim() || OBS_FLUSH_DEFAULT));

  const saveMut = useMutation({
    mutationFn: async () => {
      await Promise.all(SIGNALS.flatMap((signal) => [
        backend("set_setting", { key: retentionSettingKey(signal), value: retention[signal].trim() || OBS_RETENTION_DEFAULT[signal] }),
        backend("set_setting", { key: flushIntervalSettingKey(signal), value: flush[signal].trim() || OBS_FLUSH_DEFAULT }),
      ]));
    },
    onSuccess: () => { for (const key of [...retentionKeys, ...flushKeys]) qc.invalidateQueries({ queryKey: ["setting", key] }); },
    onError: (error: unknown) => showErrorDialog("保存遥测存储策略失败", "Failed to save telemetry storage settings", error),
  });

  return (
    <div className="glass rounded-2xl p-6 space-y-4">
      <div>
        <h3 className="text-lg font-semibold text-slate-900">{isZh ? "本地遥测保留" : "Local Telemetry Retention"}</h3>
        <p className="mt-1 text-sm text-slate-500">{isZh ? "Admin 本地 parquet 存储的保留天数与落盘间隔，不配置外部导出器的生命周期。" : "Retention days and flush interval for Admin-local parquet storage, not an external exporter's lifecycle."}</p>
      </div>
      <div className="space-y-1.5">
        <p className="ml-1 text-xs font-medium text-slate-600">{isZh ? "保留天数" : "Retention (days)"}</p>
        <div className="grid grid-cols-3 gap-3">
          {SIGNALS.map((signal) => (
            <div key={signal} className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{isZh ? OBS_SIGNAL_LABEL[signal].zh : OBS_SIGNAL_LABEL[signal].en}</label>
              <Input type="number" min={1} max={365} placeholder={OBS_RETENTION_DEFAULT[signal]} value={retention[signal]} onChange={(e) => setRetention((prev) => ({ ...prev, [signal]: e.target.value }))} />
            </div>
          ))}
        </div>
      </div>
      <div className="space-y-1.5">
        <p className="ml-1 text-xs font-medium text-slate-600">{isZh ? "落盘间隔（如 5s）" : "Flush interval (e.g. 5s)"}</p>
        <div className="grid grid-cols-3 gap-3">
          {SIGNALS.map((signal) => (
            <div key={signal} className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{isZh ? OBS_SIGNAL_LABEL[signal].zh : OBS_SIGNAL_LABEL[signal].en}</label>
              <Input placeholder={OBS_FLUSH_DEFAULT} value={flush[signal]} onChange={(e) => setFlush((prev) => ({ ...prev, [signal]: e.target.value }))} />
            </div>
          ))}
        </div>
      </div>
      <div className="flex items-center gap-2">
        <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty || flushInvalid} size="sm" className="flex items-center gap-1.5">
          {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
          {isZh ? "保存" : "Save"}
        </Button>
        {flushInvalid && <p className="text-xs text-rose-600">{isZh ? "落盘间隔格式无效（示例：5s、30s、1m）" : "Invalid flush interval (e.g. 5s, 30s, 1m)"}</p>}
        {dirty && !flushInvalid && <p className="text-xs text-amber-600">{isZh ? "重启 Admin 后生效" : "Restart Admin to apply"}</p>}
      </div>
    </div>
  );
}

interface ObsSignalCardProps {
  signal: Signal;
  isZh: boolean;
  builtInOtlpEndpoint: string | null;
  showErrorDialog: (titleZh: string, titleEn: string, error: unknown) => void;
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
      const title = OBS_SIGNAL_LABEL[signal];
      showErrorDialog(`保存${title.zh}导出设置失败`, `Failed to save ${title.en} export settings`, error);
    },
  });
  const title = OBS_SIGNAL_LABEL[signal];

  return (
    <div className="glass rounded-2xl p-6 space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h3 className="text-lg font-semibold text-slate-900">{isZh ? title.zh : title.en}</h3>
          <p className="mt-1 text-sm text-slate-500">{isZh ? "由 Gateway 导出" : "Exported by Gateway"}</p>
        </div>
        <span className="rounded-full bg-slate-100 px-2 py-1 text-[11px] font-medium text-slate-600">Gateway</span>
      </div>
      <div className="rounded-xl bg-slate-50 p-4 space-y-3">
        <div className="space-y-1.5">
          <label className="ml-1 text-xs text-slate-700">{isZh ? "导出引擎" : "Exporter"}</label>
          <Select value={emptySelectValue(exporter)} onValueChange={(value) => setExporter(emptySelectState(value))}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value={EMPTY_SELECT_SENTINEL}>{isZh ? "关闭" : "Disabled"}</SelectItem>
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
                      {isZh ? "填入内置地址" : "Use built-in"}
                    </Button>
                  )}
                </div>
              )}
              {invalid && <p className="text-xs text-red-600">{isZh ? "必填字段不能为空" : "This field is required"}</p>}
            </div>
          );
        })}
        {!builtInOtlpEndpoint && activeDef?.kind === "otlp" && <p className="text-xs text-slate-500">{isZh ? "桌面模式下暂无法自动识别内置地址，请手动填写。" : "The built-in address can't be auto-detected in desktop mode; enter it manually."}</p>}
        {notBuiltIn && <p className="text-xs text-amber-600">{isZh ? "该信号不写入内置存储，Stats/Logs 面板无数据，请到外部引擎自带 UI 查看。" : "This signal isn't writing to built-in storage — the Stats/Logs panel will show no data; check the external engine's own UI instead."}</p>}
        <div className="flex items-center gap-2">
          <Button onClick={() => saveMut.mutate()} disabled={saveMut.isPending || !dirty || missingRequired} size="sm" className="flex items-center gap-1.5">
            {saveMut.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Save className="h-3.5 w-3.5" />}
            {isZh ? "保存" : "Save"}
          </Button>
          {dirty && <p className="text-xs text-amber-600">{isZh ? "重启 Gateway 后生效" : "Restart Gateway to apply"}</p>}
        </div>
      </div>
    </div>
  );
}
