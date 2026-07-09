import { useQueries, useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useState, useRef } from "react";
import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ExportData, GatewayStatus, ImportResult } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Download, HelpCircle, Upload, Save, Loader2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
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

// The real, backend-read settings keys this page exposes. This list is the
// source of truth cross-checked against the Go backend (not the plan doc's
// example YAML, not the retired Rust WebUI's field names):
//   - proxy.request_timeout/.connect_timeout/.max_retries/.retry_on_status/
//     .max_body_bytes                     -> go/internal/proxy/gateway.go (resolveProxySettings)
//   - obs_<signal>_exporter, obs_<signal>_<engine>_<field>,
//     obs_<signal>_retention_days        -> go/internal/observability/exporter.go
//                                            (registry) + config.go (LoadConfig)
// Legacy `log_retention_days` (never read; the real key is
// `obs_logs_retention_days`), `enable_payload` (a per-route column, not a
// global setting — see models.tsx), and `proxy_bypass` (no backend consumer
// anywhere) have been removed from this page. `proxy_enabled`/`proxy_url`
// were removed too: the proxy address is a connection destination, not a
// forwarding-behavior parameter, so it belongs per-upstream (Providers page)
// — see go/internal/proxy/gateway.go's httpClientFor(proxyURL string).
// The old shared `obs_<signal>_sink`/`obs_otlp_endpoint` keys are gone too:
// each signal now has its own independent exporter + fields (see
// src/lib/observability-schema.ts, a manual mirror of exporter.go's
// registry) — this was a breaking, pre-release key migration (see
// .superpowers/sdd/global-constraints.md), not a compat shim.
const PROXY_REQUEST_TIMEOUT_KEY = "proxy.request_timeout";
const PROXY_CONNECT_TIMEOUT_KEY = "proxy.connect_timeout";
const PROXY_MAX_RETRIES_KEY = "proxy.max_retries";
const PROXY_RETRY_ON_STATUS_KEY = "proxy.retry_on_status";
const PROXY_MAX_BODY_BYTES_KEY = "proxy.max_body_bytes";

// Defaults mirror the Go backend's own fallback defaults (see
// resolveProxySettings/LoadConfig) so the WebUI shows what's actually in
// effect when a key has never been written.
const PROXY_REQUEST_TIMEOUT_DEFAULT = "120s";
const PROXY_CONNECT_TIMEOUT_DEFAULT = "30s";
const PROXY_MAX_RETRIES_DEFAULT = "2";
const PROXY_RETRY_ON_STATUS_DEFAULT = [429, 500, 502, 503, 504];
const PROXY_MAX_BODY_BYTES_DEFAULT = "33554432"; // 32 MiB

const OBS_RETENTION_DEFAULT: Record<Signal, string> = {
  logs: "7",
  metrics: "30",
  traces: "3",
};

const OBS_SIGNAL_LABEL: Record<Signal, { zh: string; en: string }> = {
  logs: { zh: "日志", en: "Logs" },
  metrics: { zh: "指标", en: "Metrics" },
  traces: { zh: "链路追踪", en: "Traces" },
};

// Radix's <Select.Item> forbids value="" (it reserves the empty string to mean
// "clear selection / show placeholder"), but "" is this page's real
// disabled/empty state everywhere else (settings payload, baseline diffing).
// Only the rendered <SelectItem> uses this sentinel; translate at the Select
// boundary so "" keeps flowing through the rest of the page.
const EMPTY_SELECT_SENTINEL = "__empty__";

function emptySelectValue(value: string): string {
  return value === "" ? EMPTY_SELECT_SENTINEL : value;
}

function emptySelectState(value: string): string {
  return value === EMPTY_SELECT_SENTINEL ? "" : value;
}

function parseRetryOnStatus(raw: string | null | undefined): string {
  if (!raw || !raw.trim()) return PROXY_RETRY_ON_STATUS_DEFAULT.join(",");
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed)) return parsed.join(",");
  } catch {
    // fall through to raw value below
  }
  return raw;
}

// GO_DURATION_RE approximates Go's time.ParseDuration syntax closely enough
// to catch the common mistake of typing a bare number (e.g. "120") with no
// unit suffix, which parses fine client-side but is rejected server-side
// (go/internal/proxy/gateway.go), silently leaving the old value in effect.
// This is intentionally not a full Go duration parser — it accepts one or
// more concatenated (number, unit) pairs such as "120s", "2m", or "1h30m".
const GO_DURATION_RE = /^(\d+(\.\d+)?(ns|µs|us|ms|s|m|h))+$/;

function isValidGoDuration(value: string): boolean {
  const trimmed = value.trim();
  if (!trimmed) return true; // empty falls back to the default on save
  return GO_DURATION_RE.test(trimmed);
}

function encodeRetryOnStatus(text: string): string | null {
  const codes = text
    .split(",")
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => Number.parseInt(part, 10));
  if (codes.some((code) => Number.isNaN(code))) return null;
  return JSON.stringify(codes);
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
        <TooltipContent side="top" className="max-w-xs">
          {text}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}

export default function SettingsPage() {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const appVersion = import.meta.env.VITE_APP_VERSION;

  const qc = useQueryClient();
  const fileRef = useRef<HTMLInputElement>(null);
  const [errorDialog, setErrorDialog] = useState<{ title: string; description?: string } | null>(null);

  const { data: status } = useQuery<GatewayStatus>({
    queryKey: ["gateway-status"],
    queryFn: () => backend("get_gateway_status"),
  });

  // --- Proxy settings ---
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
  const [proxyRetryOnStatus, setProxyRetryOnStatus] = useState("");
  const [proxyMaxBodyBytes, setProxyMaxBodyBytes] = useState("");

  const proxyBaseline = {
    requestTimeout: (proxyRequestTimeoutSetting ?? PROXY_REQUEST_TIMEOUT_DEFAULT).trim(),
    connectTimeout: (proxyConnectTimeoutSetting ?? PROXY_CONNECT_TIMEOUT_DEFAULT).trim(),
    maxRetries: (proxyMaxRetriesSetting ?? PROXY_MAX_RETRIES_DEFAULT).trim(),
    retryOnStatus: parseRetryOnStatus(proxyRetryOnStatusSetting),
    maxBodyBytes: (proxyMaxBodyBytesSetting ?? PROXY_MAX_BODY_BYTES_DEFAULT).trim(),
  };
  const requestTimeoutInvalid = !isValidGoDuration(proxyRequestTimeout);
  const connectTimeoutInvalid = !isValidGoDuration(proxyConnectTimeout);

  const proxyDirty =
    proxyRequestTimeout.trim() !== proxyBaseline.requestTimeout
    || proxyConnectTimeout.trim() !== proxyBaseline.connectTimeout
    || proxyMaxRetries.trim() !== proxyBaseline.maxRetries
    || proxyRetryOnStatus.trim() !== proxyBaseline.retryOnStatus
    || proxyMaxBodyBytes.trim() !== proxyBaseline.maxBodyBytes;

  useEffect(() => {
    setProxyRequestTimeout(proxyBaseline.requestTimeout);
    setProxyConnectTimeout(proxyBaseline.connectTimeout);
    setProxyMaxRetries(proxyBaseline.maxRetries);
    setProxyRetryOnStatus(proxyBaseline.retryOnStatus);
    setProxyMaxBodyBytes(proxyBaseline.maxBodyBytes);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    proxyRequestTimeoutSetting,
    proxyConnectTimeoutSetting,
    proxyMaxRetriesSetting,
    proxyRetryOnStatusSetting,
    proxyMaxBodyBytesSetting,
  ]);

  function formatErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleZh: string, titleEn: string, error: unknown) {
    setErrorDialog({
      title: isZh ? titleZh : titleEn,
      description: formatErrorMessage(error),
    });
  }

  const saveProxyMut = useMutation({
    mutationFn: async (input: {
      requestTimeout: string;
      connectTimeout: string;
      maxRetries: string;
      retryOnStatus: string;
      maxBodyBytes: string;
    }) => {
      const encodedRetryOnStatus = encodeRetryOnStatus(input.retryOnStatus);
      if (encodedRetryOnStatus == null) {
        throw new Error(
          isZh
            ? "重试状态码必须是以逗号分隔的数字列表，例如 429,500,502,503,504"
            : "Retry status codes must be a comma-separated list of numbers, e.g. 429,500,502,503,504",
        );
      }
      await Promise.all([
        backend("set_setting", { key: PROXY_REQUEST_TIMEOUT_KEY, value: input.requestTimeout }),
        backend("set_setting", { key: PROXY_CONNECT_TIMEOUT_KEY, value: input.connectTimeout }),
        backend("set_setting", { key: PROXY_MAX_RETRIES_KEY, value: input.maxRetries }),
        backend("set_setting", { key: PROXY_RETRY_ON_STATUS_KEY, value: encodedRetryOnStatus }),
        backend("set_setting", { key: PROXY_MAX_BODY_BYTES_KEY, value: input.maxBodyBytes }),
      ]);
    },
    onSuccess: () => {
      for (const key of [
        PROXY_REQUEST_TIMEOUT_KEY,
        PROXY_CONNECT_TIMEOUT_KEY,
        PROXY_MAX_RETRIES_KEY,
        PROXY_RETRY_ON_STATUS_KEY,
        PROXY_MAX_BODY_BYTES_KEY,
      ]) {
        qc.invalidateQueries({ queryKey: ["setting", key] });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("保存转发参数失败", "Failed to save forwarding settings", error);
    },
  });

  const exportMut = useMutation({
    mutationFn: () => backend<ExportData>("export_config"),
    onSuccess: (data) => {
      const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `nyro-config-${new Date().toISOString().slice(0, 10)}.json`;
      a.click();
      URL.revokeObjectURL(url);
    },
    onError: (error: unknown) => {
      showErrorDialog("导出配置失败", "Failed to export config", error);
    },
  });

  const importMut = useMutation({
    mutationFn: (data: ExportData) =>
      backend<ImportResult>("import_config", { data }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      qc.invalidateQueries({ queryKey: ["routes"] });
    },
    onError: (error: unknown) => {
      showErrorDialog("导入配置失败", "Failed to import config", error);
    },
  });

  function handleImportFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      try {
        const data = JSON.parse(reader.result as string) as ExportData;
        importMut.mutate(data);
      } catch {
        setErrorDialog({
          title: isZh ? "导入配置失败" : "Failed to import config",
          description: isZh ? "无效的 JSON 文件" : "Invalid JSON file",
        });
      }
    };
    reader.readAsText(file);
    e.target.value = "";
  }

  function handleSaveProxy() {
    if (requestTimeoutInvalid || connectTimeoutInvalid) {
      setErrorDialog({
        title: isZh ? "无法保存转发参数" : "Cannot Save Forwarding Settings",
        description: isZh
          ? "请求超时/连接超时必须是 Go duration 格式，如 120s、2m、1h30m。"
          : "Request Timeout / Connect Timeout must be a Go duration, e.g. 120s, 2m, 1h30m.",
      });
      return;
    }
    saveProxyMut.mutate({
      requestTimeout: proxyRequestTimeout.trim() || PROXY_REQUEST_TIMEOUT_DEFAULT,
      connectTimeout: proxyConnectTimeout.trim() || PROXY_CONNECT_TIMEOUT_DEFAULT,
      maxRetries: proxyMaxRetries.trim() || PROXY_MAX_RETRIES_DEFAULT,
      retryOnStatus: proxyRetryOnStatus.trim() || PROXY_RETRY_ON_STATUS_DEFAULT.join(","),
      maxBodyBytes: proxyMaxBodyBytes.trim() || PROXY_MAX_BODY_BYTES_DEFAULT,
    });
  }

  // Server mode: WebUI is served by the same admin process it talks to, so
  // the built-in receiver address is just this origin (admin's OTLP receiver
  // shares the REST API's router/addr — see admin.go). Tauri desktop mode has
  // no such same-origin relationship and no existing "admin address" constant
  // to reuse (the Tauri shell here belongs to a different, untouched Rust
  // webui project — see go/webui/src/lib/backend.ts's IS_TAURI check), so we
  // cannot reliably determine the built-in address there; the "use built-in
  // address" button is disabled in that case (see ObsSignalCard below).
  const builtInOtlpEndpoint =
    !IS_TAURI && typeof window !== "undefined" ? window.location.origin : null;

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold text-slate-900">{isZh ? "设置" : "Settings"}</h1>
        <p className="mt-1 text-sm text-slate-500">
          {isZh ? "网关配置" : "Gateway configuration"}
        </p>
      </div>

      {/* Gateway Status */}
      <div className="glass rounded-2xl p-6 space-y-4">
        <h2 className="text-lg font-semibold text-slate-900">{isZh ? "网关状态" : "Gateway Status"}</h2>
        <div className="grid grid-cols-2 gap-4 lg:grid-cols-4">
          <div className="rounded-xl bg-slate-50 p-4">
            <p className="text-xs text-slate-500">{isZh ? "状态" : "Status"}</p>
            <p className="mt-1 font-semibold text-green-600">{status?.status ?? "–"}</p>
          </div>
          <div className="rounded-xl bg-slate-50 p-4">
            <p className="text-xs text-slate-500">{isZh ? "存储后端" : "Storage Backend"}</p>
            <p className="mt-1 font-semibold text-slate-900">{status?.backend ?? "–"}</p>
          </div>
          <div className="rounded-xl bg-slate-50 p-4">
            <p className="text-xs text-slate-500">{isZh ? "模式" : "Mode"}</p>
            <p className="mt-1 font-semibold text-slate-900">{IS_TAURI ? (isZh ? "桌面版" : "Desktop") : "Server"}</p>
          </div>
          <div className="rounded-xl bg-slate-50 p-4">
            <p className="text-xs text-slate-500">{isZh ? "版本" : "Version"}</p>
            <p className="mt-1 font-semibold text-slate-900">{appVersion}</p>
          </div>
        </div>
      </div>

      <div className="grid grid-cols-1 gap-5">
        {/* Forwarding Settings */}
        <div className="glass rounded-2xl p-6 space-y-5">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "转发参数" : "Forwarding Settings"}</h2>
          <div className="rounded-xl bg-slate-50 p-4 space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "请求超时" : "Request Timeout"}
                  <HelpHint
                    text={
                      isZh
                        ? "Go duration 格式，如 120s、2m。对应 proxy.request_timeout"
                        : "Go duration syntax, e.g. 120s, 2m. Maps to proxy.request_timeout"
                    }
                  />
                </label>
                <Input
                  placeholder={PROXY_REQUEST_TIMEOUT_DEFAULT}
                  value={proxyRequestTimeout}
                  onChange={(e) => setProxyRequestTimeout(e.target.value)}
                  className={requestTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined}
                />
                {requestTimeoutInvalid && (
                  <p className="text-xs text-red-600">
                    {isZh ? "需要带单位，如 120s、2m" : "Needs a unit, e.g. 120s, 2m"}
                  </p>
                )}
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "连接超时" : "Connect Timeout"}
                  <HelpHint
                    text={
                      isZh
                        ? "Go duration 格式，如 30s。对应 proxy.connect_timeout"
                        : "Go duration syntax, e.g. 30s. Maps to proxy.connect_timeout"
                    }
                  />
                </label>
                <Input
                  placeholder={PROXY_CONNECT_TIMEOUT_DEFAULT}
                  value={proxyConnectTimeout}
                  onChange={(e) => setProxyConnectTimeout(e.target.value)}
                  className={connectTimeoutInvalid ? "border-red-400 focus-visible:ring-red-400" : undefined}
                />
                {connectTimeoutInvalid && (
                  <p className="text-xs text-red-600">
                    {isZh ? "需要带单位，如 30s、1m" : "Needs a unit, e.g. 30s, 1m"}
                  </p>
                )}
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "最大重试次数" : "Max Retries"}</label>
                <Input
                  type="number"
                  min={0}
                  placeholder={PROXY_MAX_RETRIES_DEFAULT}
                  value={proxyMaxRetries}
                  onChange={(e) => setProxyMaxRetries(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "最大请求体（字节）" : "Max Body Bytes"}</label>
                <Input
                  type="number"
                  min={1}
                  placeholder={PROXY_MAX_BODY_BYTES_DEFAULT}
                  value={proxyMaxBodyBytes}
                  onChange={(e) => setProxyMaxBodyBytes(e.target.value)}
                />
              </div>
              <div className="col-span-2 space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "重试状态码（逗号分隔）" : "Retry Status Codes (comma-separated)"}
                  <HelpHint
                    text={
                      isZh
                        ? "对应 proxy.retry_on_status，例如 429,500,502,503,504"
                        : "Maps to proxy.retry_on_status, e.g. 429,500,502,503,504"
                    }
                  />
                </label>
                <Input
                  placeholder={PROXY_RETRY_ON_STATUS_DEFAULT.join(",")}
                  value={proxyRetryOnStatus}
                  onChange={(e) => setProxyRetryOnStatus(e.target.value)}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                onClick={handleSaveProxy}
                disabled={saveProxyMut.isPending || !proxyDirty || requestTimeoutInvalid || connectTimeoutInvalid}
                size="sm"
                className="flex items-center gap-1.5"
              >
                {saveProxyMut.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save className="h-3.5 w-3.5" />
                )}
                {isZh ? "保存" : "Save"}
              </Button>
              {proxyDirty && (
                <p className="text-xs text-amber-600">
                  {isZh ? "配置已修改，保存后生效" : "Unsaved changes, save to apply"}
                </p>
              )}
            </div>
          </div>
        </div>

      </div>

      {/* Observability Configuration: three fully independent per-signal cards */}
      <div className="grid grid-cols-1 gap-5 lg:grid-cols-3">
        {SIGNALS.map((signal) => (
          <ObsSignalCard
            key={signal}
            signal={signal}
            isZh={isZh}
            builtInOtlpEndpoint={builtInOtlpEndpoint}
            showErrorDialog={showErrorDialog}
          />
        ))}
      </div>

      {/* Config Backup */}
      <div className="glass rounded-2xl p-6 space-y-5">
        <h2 className="text-lg font-semibold text-slate-900">{isZh ? "配置备份" : "Config Backup"}</h2>
        <div className="flex flex-wrap items-center gap-3 rounded-xl bg-slate-50 px-4 py-3">
          <p className="text-xs text-slate-500">
            {isZh ? "导出或导入提供商、模型和设置" : "Export or import providers, models & settings"}
          </p>
          <div className="ml-auto flex items-center gap-2">
            <Button
              onClick={() => exportMut.mutate()}
              disabled
              size="sm"
              className="flex items-center gap-1.5"
            >
              {exportMut.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Download className="h-3.5 w-3.5" />
              )}
              {isZh ? "暂不可用" : "Unavailable"}
            </Button>
            <Button
              onClick={() => fileRef.current?.click()}
              disabled
              variant="secondary"
              size="sm"
              className="flex items-center gap-1.5"
            >
              {importMut.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Upload className="h-3.5 w-3.5" />
              )}
              {isZh ? "导入" : "Import"}
            </Button>
            <input
              ref={fileRef}
              type="file"
              accept=".json"
              className="hidden"
              onChange={handleImportFile}
            />
          </div>
          {importMut.isSuccess && importMut.data && (
            <p className="w-full text-xs text-green-600">
              {isZh
                ? `已导入：${(importMut.data as ImportResult).providers_imported} 个提供商，${(importMut.data as ImportResult).models_imported} 个模型，${(importMut.data as ImportResult).settings_imported} 项设置`
                : `Imported: ${(importMut.data as ImportResult).providers_imported} providers, ${(importMut.data as ImportResult).models_imported} models, ${(importMut.data as ImportResult).settings_imported} settings`}
            </p>
          )}
        </div>
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

// ObsSignalCard renders one fully independent Logs/Metrics/Traces
// observability card: its own exporter dropdown, its own per-engine field
// inputs (driven by the static exporter-schema mirror), its own retention
// input, and its own save button/mutation. Saving one card never touches the
// other two signals' settings keys.
interface ObsSignalCardProps {
  signal: Signal;
  isZh: boolean;
  // Server mode: window.location.origin (WebUI and admin are same-origin).
  // Tauri mode: null — there is no known built-in admin address to offer
  // (see the comment above builtInOtlpEndpoint in SettingsPage), so the
  // "use built-in address" button is disabled below.
  builtInOtlpEndpoint: string | null;
  showErrorDialog: (titleZh: string, titleEn: string, error: unknown) => void;
}

function ObsSignalCard({ signal, isZh, builtInOtlpEndpoint, showErrorDialog }: ObsSignalCardProps) {
  const qc = useQueryClient();
  const defs = useMemo(() => exportersFor(signal), [signal]);
  const expKey = exporterSettingKey(signal);
  const retKey = retentionSettingKey(signal);
  const retentionDefault = OBS_RETENTION_DEFAULT[signal];

  // Every (engine, field) storage key for this signal, flattened. Field names
  // never collide across engines within a signal (endpoint/protocol/interval
  // are otlp-only, listen/path are prometheus-only), so form state below can
  // key values by field name alone.
  const fieldSlots = useMemo(() => {
    const slots: { kind: ExporterDef["kind"]; field: FieldDef; storageKey: string }[] = [];
    for (const def of defs) {
      for (const field of def.fields) {
        slots.push({ kind: def.kind, field, storageKey: settingKey(signal, def.kind, field.name) });
      }
    }
    return slots;
  }, [defs, signal]);

  const allKeys = useMemo(
    () => [expKey, retKey, ...fieldSlots.map((s) => s.storageKey)],
    [expKey, retKey, fieldSlots],
  );

  const queries = useQueries({
    queries: allKeys.map((key) => ({
      queryKey: ["setting", key],
      queryFn: () => backend<string | null>("get_setting", { key }),
    })),
  });

  const exporterSetting = queries[0]?.data ?? null;
  const retentionSetting = queries[1]?.data ?? null;
  const fieldSettings = fieldSlots.map((_, i) => queries[2 + i]?.data ?? null);

  const baselineExporter = exporterSetting ?? "";
  const baselineRetention = (retentionSetting ?? retentionDefault).trim();
  const baselineFields = useMemo(() => {
    const obj: Record<string, string> = {};
    fieldSlots.forEach((slot, i) => {
      obj[slot.field.name] = fieldSettings[i] ?? "";
    });
    return obj;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fieldSlots, JSON.stringify(fieldSettings)]);

  const [exporter, setExporter] = useState("");
  const [retention, setRetention] = useState("");
  const [fieldValues, setFieldValues] = useState<Record<string, string>>({});

  useEffect(() => {
    setExporter(baselineExporter);
    setRetention(baselineRetention);
    setFieldValues(baselineFields);
  }, [baselineExporter, baselineRetention, baselineFields]);

  const activeDef = defs.find((d) => d.kind === exporter) ?? null;
  const activeFields = activeDef?.fields ?? [];

  const missingRequired = activeFields.some((f) => f.required && !(fieldValues[f.name] ?? "").trim());

  const dirty =
    exporter !== baselineExporter
    || retention.trim() !== baselineRetention
    || activeFields.some((f) => (fieldValues[f.name] ?? "").trim() !== (baselineFields[f.name] ?? "").trim());

  const currentEndpoint = (fieldValues["endpoint"] ?? "").trim();
  const notBuiltIn =
    exporter !== "otlp" || currentEndpoint !== (builtInOtlpEndpoint ?? "").trim() || !builtInOtlpEndpoint;

  const saveMut = useMutation({
    mutationFn: async () => {
      const payload: Record<string, string> = {
        [expKey]: exporter,
        [retKey]: retention.trim() || retentionDefault,
      };
      for (const f of activeFields) {
        payload[settingKey(signal, exporter as ExporterDef["kind"], f.name)] = (fieldValues[f.name] ?? "").trim();
      }
      await Promise.all(
        Object.entries(payload).map(([key, value]) => backend("set_setting", { key, value })),
      );
      return payload;
    },
    onSuccess: (payload) => {
      for (const key of Object.keys(payload)) {
        qc.invalidateQueries({ queryKey: ["setting", key] });
      }
    },
    onError: (error: unknown) => {
      const title = OBS_SIGNAL_LABEL[signal];
      showErrorDialog(
        `保存${title.zh}导出设置失败`,
        `Failed to save ${title.en} export settings`,
        error,
      );
    },
  });

  const title = OBS_SIGNAL_LABEL[signal];

  return (
    <div className="glass rounded-2xl p-6 space-y-4">
      <h2 className="text-lg font-semibold text-slate-900">{isZh ? title.zh : title.en}</h2>
      <div className="rounded-xl bg-slate-50 p-4 space-y-3">
        <div className="space-y-1.5">
          <label className="ml-1 text-xs text-slate-700">{isZh ? "导出引擎" : "Exporter"}</label>
          <Select value={emptySelectValue(exporter)} onValueChange={(v) => setExporter(emptySelectState(v))}>
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={EMPTY_SELECT_SENTINEL}>{isZh ? "关闭" : "Disabled"}</SelectItem>
              {defs.map((def) => (
                <SelectItem key={def.kind} value={def.kind}>
                  {exporterKindLabel(def.kind)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        {activeFields.length > 0 && (
          <div className="space-y-3">
            {activeFields.map((f) => {
              const value = fieldValues[f.name] ?? "";
              const invalid = Boolean(f.required) && !value.trim();
              return (
                <div key={f.name} className="space-y-1.5">
                  <label className="ml-1 text-xs text-slate-700">
                    {f.label}
                    {f.required ? " *" : ""}
                  </label>
                  {f.type === "select" ? (
                    <Select
                      value={value || f.default || f.options?.[0] || ""}
                      onValueChange={(v) => setFieldValues((prev) => ({ ...prev, [f.name]: v }))}
                    >
                      <SelectTrigger>
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {(f.options ?? []).map((opt) => (
                          <SelectItem key={opt} value={opt}>
                            {opt}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  ) : (
                    <div className="flex items-center gap-2">
                      <Input
                        placeholder={f.default || undefined}
                        value={value}
                        onChange={(e) => setFieldValues((prev) => ({ ...prev, [f.name]: e.target.value }))}
                        className={invalid ? "border-red-400 focus-visible:ring-red-400" : undefined}
                      />
                      {activeDef?.kind === "otlp" && f.name === "endpoint" && (
                        <Button
                          type="button"
                          variant="secondary"
                          size="sm"
                          disabled={!builtInOtlpEndpoint}
                          onClick={() =>
                            setFieldValues((prev) => ({ ...prev, endpoint: builtInOtlpEndpoint ?? "" }))
                          }
                          className="whitespace-nowrap"
                        >
                          {isZh ? "填入内置地址" : "Use built-in"}
                        </Button>
                      )}
                    </div>
                  )}
                  {invalid && (
                    <p className="text-xs text-red-600">
                      {isZh ? "必填字段不能为空" : "This field is required"}
                    </p>
                  )}
                </div>
              );
            })}
          </div>
        )}

        {!builtInOtlpEndpoint && activeDef?.kind === "otlp" && (
          <p className="text-xs text-slate-500">
            {isZh
              ? "桌面模式下暂无法自动识别内置地址，请手动填写。"
              : "The built-in address can't be auto-detected in desktop mode; enter it manually."}
          </p>
        )}

        {notBuiltIn && (
          <p className="text-xs text-amber-600">
            {isZh
              ? "该信号不写入内置存储，Stats/Logs 面板无数据，请到外部引擎自带 UI 查看。"
              : "This signal isn't writing to built-in storage — the Stats/Logs panel will show no data; check the external engine's own UI instead."}
          </p>
        )}

        <div className="space-y-1.5">
          <label className="ml-1 text-xs text-slate-700">{isZh ? "保留天数" : "Retention (days)"}</label>
          <Input
            type="number"
            min={1}
            max={365}
            placeholder={retentionDefault}
            value={retention}
            onChange={(e) => setRetention(e.target.value)}
          />
        </div>

        <div className="flex items-center gap-2">
          <Button
            onClick={() => saveMut.mutate()}
            disabled={saveMut.isPending || !dirty || missingRequired}
            size="sm"
            className="flex items-center gap-1.5"
          >
            {saveMut.isPending ? (
              <Loader2 className="h-3.5 w-3.5 animate-spin" />
            ) : (
              <Save className="h-3.5 w-3.5" />
            )}
            {isZh ? "保存" : "Save"}
          </Button>
          {dirty && (
            <p className="text-xs text-amber-600">
              {isZh ? "配置已修改，重启网关后生效" : "Unsaved changes — restart the gateway to apply"}
            </p>
          )}
        </div>
      </div>
    </div>
  );
}
