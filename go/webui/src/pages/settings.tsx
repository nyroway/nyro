import { useQueries, useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, useRef } from "react";
import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ExportData, GatewayStatus, ImportResult } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { Download, HelpCircle, Upload, Save, Loader2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
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

// The real, backend-read settings keys this page exposes. This list is the
// source of truth cross-checked against the Go backend (not the plan doc's
// example YAML, not the retired Rust WebUI's field names):
//   - proxy_enabled / proxy_url            -> go/internal/proxy/gateway.go
//   - proxy.request_timeout/.connect_timeout/.max_retries/.retry_on_status/
//     .max_body_bytes                     -> go/internal/proxy/gateway.go (resolveProxySettings)
//   - obs_logs_sink/obs_metrics_sink/obs_traces_sink/obs_otlp_endpoint/
//     obs_logs_retention_days/obs_metrics_retention_days/obs_traces_retention_days
//                                          -> go/internal/observability/config.go (LoadConfig)
// Legacy `log_retention_days` (never read; the real key is
// `obs_logs_retention_days`), `enable_payload` (a per-route column, not a
// global setting — see models.tsx), and `proxy_bypass` (no backend consumer
// anywhere) have been removed from this page.
const PROXY_ENABLED_KEY = "proxy_enabled";
const PROXY_URL_KEY = "proxy_url";
const PROXY_REQUEST_TIMEOUT_KEY = "proxy.request_timeout";
const PROXY_CONNECT_TIMEOUT_KEY = "proxy.connect_timeout";
const PROXY_MAX_RETRIES_KEY = "proxy.max_retries";
const PROXY_RETRY_ON_STATUS_KEY = "proxy.retry_on_status";
const PROXY_MAX_BODY_BYTES_KEY = "proxy.max_body_bytes";

const OBS_LOGS_SINK_KEY = "obs_logs_sink";
const OBS_METRICS_SINK_KEY = "obs_metrics_sink";
const OBS_TRACES_SINK_KEY = "obs_traces_sink";
const OBS_OTLP_ENDPOINT_KEY = "obs_otlp_endpoint";
const OBS_LOGS_RETENTION_KEY = "obs_logs_retention_days";
const OBS_METRICS_RETENTION_KEY = "obs_metrics_retention_days";
const OBS_TRACES_RETENTION_KEY = "obs_traces_retention_days";

// Defaults mirror the Go backend's own fallback defaults (see
// resolveProxySettings/LoadConfig) so the WebUI shows what's actually in
// effect when a key has never been written.
const PROXY_REQUEST_TIMEOUT_DEFAULT = "120s";
const PROXY_CONNECT_TIMEOUT_DEFAULT = "30s";
const PROXY_MAX_RETRIES_DEFAULT = "2";
const PROXY_RETRY_ON_STATUS_DEFAULT = [429, 500, 502, 503, 504];
const PROXY_MAX_BODY_BYTES_DEFAULT = "33554432"; // 32 MiB
const OBS_LOGS_RETENTION_DEFAULT = "7";
const OBS_METRICS_RETENTION_DEFAULT = "30";
const OBS_TRACES_RETENTION_DEFAULT = "3";

const SINK_OPTIONS = ["", "none", "stdout", "otlp"] as const;

function sinkOptionLabel(value: string, isZh: boolean) {
  if (value === "") return isZh ? "（默认继承）" : "(inherit default)";
  if (value === "none") return isZh ? "关闭" : "None";
  if (value === "stdout") return "stdout";
  return "OTLP";
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

function ToggleStatusLabel({ enabled, isZh }: { enabled: boolean; isZh: boolean }) {
  return (
    <Badge variant={enabled ? "success" : "secondary"} className="connect-label-badge">
      {enabled ? (isZh ? "已启用" : "Enabled") : (isZh ? "未启用" : "Disabled")}
    </Badge>
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
  const { data: proxyEnabledSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_ENABLED_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_ENABLED_KEY }),
  });
  const { data: proxyUrlSetting } = useQuery<string | null>({
    queryKey: ["setting", PROXY_URL_KEY],
    queryFn: () => backend("get_setting", { key: PROXY_URL_KEY }),
  });
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

  // --- Observability settings ---
  const obsQueries = useQueries({
    queries: [
      OBS_LOGS_SINK_KEY,
      OBS_METRICS_SINK_KEY,
      OBS_TRACES_SINK_KEY,
      OBS_OTLP_ENDPOINT_KEY,
      OBS_LOGS_RETENTION_KEY,
      OBS_METRICS_RETENTION_KEY,
      OBS_TRACES_RETENTION_KEY,
    ].map((key) => ({
      queryKey: ["setting", key],
      queryFn: () => backend<string | null>("get_setting", { key }),
    })),
  });
  const [
    obsLogsSinkSetting,
    obsMetricsSinkSetting,
    obsTracesSinkSetting,
    obsOtlpEndpointSetting,
    obsLogsRetentionSetting,
    obsMetricsRetentionSetting,
    obsTracesRetentionSetting,
  ] = obsQueries.map((q) => q.data ?? null);

  const normalizedProxyEnabledSetting = ["1", "true", "yes", "on"].includes(
    (proxyEnabledSetting ?? "").trim().toLowerCase(),
  );
  const [proxyEnabled, setProxyEnabled] = useState(false);
  const [proxyUrl, setProxyUrl] = useState("");
  const [proxyRequestTimeout, setProxyRequestTimeout] = useState("");
  const [proxyConnectTimeout, setProxyConnectTimeout] = useState("");
  const [proxyMaxRetries, setProxyMaxRetries] = useState("");
  const [proxyRetryOnStatus, setProxyRetryOnStatus] = useState("");
  const [proxyMaxBodyBytes, setProxyMaxBodyBytes] = useState("");

  const [obsLogsSink, setObsLogsSink] = useState("");
  const [obsMetricsSink, setObsMetricsSink] = useState("");
  const [obsTracesSink, setObsTracesSink] = useState("");
  const [obsOtlpEndpoint, setObsOtlpEndpoint] = useState("");
  const [obsLogsRetention, setObsLogsRetention] = useState("");
  const [obsMetricsRetention, setObsMetricsRetention] = useState("");
  const [obsTracesRetention, setObsTracesRetention] = useState("");

  const proxyBaseline = {
    enabled: normalizedProxyEnabledSetting,
    url: (proxyUrlSetting ?? "").trim(),
    requestTimeout: (proxyRequestTimeoutSetting ?? PROXY_REQUEST_TIMEOUT_DEFAULT).trim(),
    connectTimeout: (proxyConnectTimeoutSetting ?? PROXY_CONNECT_TIMEOUT_DEFAULT).trim(),
    maxRetries: (proxyMaxRetriesSetting ?? PROXY_MAX_RETRIES_DEFAULT).trim(),
    retryOnStatus: parseRetryOnStatus(proxyRetryOnStatusSetting),
    maxBodyBytes: (proxyMaxBodyBytesSetting ?? PROXY_MAX_BODY_BYTES_DEFAULT).trim(),
  };
  const requestTimeoutInvalid = !isValidGoDuration(proxyRequestTimeout);
  const connectTimeoutInvalid = !isValidGoDuration(proxyConnectTimeout);

  const proxyDirty =
    proxyEnabled !== proxyBaseline.enabled
    || proxyUrl.trim() !== proxyBaseline.url
    || proxyRequestTimeout.trim() !== proxyBaseline.requestTimeout
    || proxyConnectTimeout.trim() !== proxyBaseline.connectTimeout
    || proxyMaxRetries.trim() !== proxyBaseline.maxRetries
    || proxyRetryOnStatus.trim() !== proxyBaseline.retryOnStatus
    || proxyMaxBodyBytes.trim() !== proxyBaseline.maxBodyBytes;

  const obsBaseline = {
    logsSink: obsLogsSinkSetting ?? "",
    metricsSink: obsMetricsSinkSetting ?? "",
    tracesSink: obsTracesSinkSetting ?? "",
    otlpEndpoint: obsOtlpEndpointSetting ?? "",
    logsRetention: (obsLogsRetentionSetting ?? OBS_LOGS_RETENTION_DEFAULT).trim(),
    metricsRetention: (obsMetricsRetentionSetting ?? OBS_METRICS_RETENTION_DEFAULT).trim(),
    tracesRetention: (obsTracesRetentionSetting ?? OBS_TRACES_RETENTION_DEFAULT).trim(),
  };
  const obsDirty =
    obsLogsSink !== obsBaseline.logsSink
    || obsMetricsSink !== obsBaseline.metricsSink
    || obsTracesSink !== obsBaseline.tracesSink
    || obsOtlpEndpoint.trim() !== obsBaseline.otlpEndpoint
    || obsLogsRetention.trim() !== obsBaseline.logsRetention
    || obsMetricsRetention.trim() !== obsBaseline.metricsRetention
    || obsTracesRetention.trim() !== obsBaseline.tracesRetention;

  useEffect(() => {
    setProxyEnabled(proxyBaseline.enabled);
    setProxyUrl(proxyBaseline.url);
    setProxyRequestTimeout(proxyBaseline.requestTimeout);
    setProxyConnectTimeout(proxyBaseline.connectTimeout);
    setProxyMaxRetries(proxyBaseline.maxRetries);
    setProxyRetryOnStatus(proxyBaseline.retryOnStatus);
    setProxyMaxBodyBytes(proxyBaseline.maxBodyBytes);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    normalizedProxyEnabledSetting,
    proxyUrlSetting,
    proxyRequestTimeoutSetting,
    proxyConnectTimeoutSetting,
    proxyMaxRetriesSetting,
    proxyRetryOnStatusSetting,
    proxyMaxBodyBytesSetting,
  ]);

  useEffect(() => {
    setObsLogsSink(obsBaseline.logsSink);
    setObsMetricsSink(obsBaseline.metricsSink);
    setObsTracesSink(obsBaseline.tracesSink);
    setObsOtlpEndpoint(obsBaseline.otlpEndpoint);
    setObsLogsRetention(obsBaseline.logsRetention);
    setObsMetricsRetention(obsBaseline.metricsRetention);
    setObsTracesRetention(obsBaseline.tracesRetention);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    obsLogsSinkSetting,
    obsMetricsSinkSetting,
    obsTracesSinkSetting,
    obsOtlpEndpointSetting,
    obsLogsRetentionSetting,
    obsMetricsRetentionSetting,
    obsTracesRetentionSetting,
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
      enabled: boolean;
      url: string;
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
        backend("set_setting", { key: PROXY_ENABLED_KEY, value: input.enabled ? "true" : "false" }),
        backend("set_setting", { key: PROXY_URL_KEY, value: input.url }),
        backend("set_setting", { key: PROXY_REQUEST_TIMEOUT_KEY, value: input.requestTimeout }),
        backend("set_setting", { key: PROXY_CONNECT_TIMEOUT_KEY, value: input.connectTimeout }),
        backend("set_setting", { key: PROXY_MAX_RETRIES_KEY, value: input.maxRetries }),
        backend("set_setting", { key: PROXY_RETRY_ON_STATUS_KEY, value: encodedRetryOnStatus }),
        backend("set_setting", { key: PROXY_MAX_BODY_BYTES_KEY, value: input.maxBodyBytes }),
      ]);
    },
    onSuccess: () => {
      for (const key of [
        PROXY_ENABLED_KEY,
        PROXY_URL_KEY,
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
      showErrorDialog("保存代理设置失败", "Failed to save proxy settings", error);
    },
  });

  const saveObsMut = useMutation({
    mutationFn: async (input: {
      logsSink: string;
      metricsSink: string;
      tracesSink: string;
      otlpEndpoint: string;
      logsRetention: string;
      metricsRetention: string;
      tracesRetention: string;
    }) => {
      await Promise.all([
        backend("set_setting", { key: OBS_LOGS_SINK_KEY, value: input.logsSink }),
        backend("set_setting", { key: OBS_METRICS_SINK_KEY, value: input.metricsSink }),
        backend("set_setting", { key: OBS_TRACES_SINK_KEY, value: input.tracesSink }),
        backend("set_setting", { key: OBS_OTLP_ENDPOINT_KEY, value: input.otlpEndpoint }),
        backend("set_setting", { key: OBS_LOGS_RETENTION_KEY, value: input.logsRetention }),
        backend("set_setting", { key: OBS_METRICS_RETENTION_KEY, value: input.metricsRetention }),
        backend("set_setting", { key: OBS_TRACES_RETENTION_KEY, value: input.tracesRetention }),
      ]);
    },
    onSuccess: () => {
      for (const key of [
        OBS_LOGS_SINK_KEY,
        OBS_METRICS_SINK_KEY,
        OBS_TRACES_SINK_KEY,
        OBS_OTLP_ENDPOINT_KEY,
        OBS_LOGS_RETENTION_KEY,
        OBS_METRICS_RETENTION_KEY,
        OBS_TRACES_RETENTION_KEY,
      ]) {
        qc.invalidateQueries({ queryKey: ["setting", key] });
      }
    },
    onError: (error: unknown) => {
      showErrorDialog("保存可观测性设置失败", "Failed to save observability settings", error);
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
    const url = proxyUrl.trim();
    if (proxyEnabled && !url) {
      setErrorDialog({
        title: isZh ? "无法保存代理配置" : "Cannot Save Proxy Settings",
        description: isZh ? "请先填写代理 URL，再保存代理配置。" : "Please set proxy URL before saving proxy settings.",
      });
      return;
    }
    if (requestTimeoutInvalid || connectTimeoutInvalid) {
      setErrorDialog({
        title: isZh ? "无法保存代理配置" : "Cannot Save Proxy Settings",
        description: isZh
          ? "请求超时/连接超时必须是 Go duration 格式，如 120s、2m、1h30m。"
          : "Request Timeout / Connect Timeout must be a Go duration, e.g. 120s, 2m, 1h30m.",
      });
      return;
    }
    saveProxyMut.mutate({
      enabled: proxyEnabled,
      url,
      requestTimeout: proxyRequestTimeout.trim() || PROXY_REQUEST_TIMEOUT_DEFAULT,
      connectTimeout: proxyConnectTimeout.trim() || PROXY_CONNECT_TIMEOUT_DEFAULT,
      maxRetries: proxyMaxRetries.trim() || PROXY_MAX_RETRIES_DEFAULT,
      retryOnStatus: proxyRetryOnStatus.trim() || PROXY_RETRY_ON_STATUS_DEFAULT.join(","),
      maxBodyBytes: proxyMaxBodyBytes.trim() || PROXY_MAX_BODY_BYTES_DEFAULT,
    });
  }

  function handleSaveObs() {
    saveObsMut.mutate({
      logsSink: obsLogsSink,
      metricsSink: obsMetricsSink,
      tracesSink: obsTracesSink,
      otlpEndpoint: obsOtlpEndpoint.trim(),
      logsRetention: obsLogsRetention.trim() || OBS_LOGS_RETENTION_DEFAULT,
      metricsRetention: obsMetricsRetention.trim() || OBS_METRICS_RETENTION_DEFAULT,
      tracesRetention: obsTracesRetention.trim() || OBS_TRACES_RETENTION_DEFAULT,
    });
  }

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

      <div className="grid grid-cols-1 gap-5 md:grid-cols-2">
        {/* Proxy Configuration */}
        <div className="glass rounded-2xl p-6 space-y-5">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "代理配置" : "Proxy Configuration"}</h2>
          <div className="rounded-xl bg-slate-50 p-4 space-y-3">
            <div className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{isZh ? "代理" : "Proxy"}</label>
              <div className="flex items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
                <div className="flex items-center gap-2">
                  <ToggleStatusLabel enabled={proxyEnabled} isZh={isZh} />
                </div>
                <Switch
                  checked={proxyEnabled}
                  disabled={saveProxyMut.isPending}
                  onCheckedChange={setProxyEnabled}
                />
              </div>
            </div>
            <div className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{isZh ? "代理 URL" : "Proxy URL"}</label>
              <Input
                placeholder="http://127.0.0.1:7890"
                value={proxyUrl}
                onChange={(e) => setProxyUrl(e.target.value)}
              />
            </div>
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

        {/* Observability Configuration */}
        <div className="glass rounded-2xl p-6 space-y-5">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "可观测性配置" : "Observability Configuration"}</h2>
          <div className="rounded-xl bg-slate-50 p-4 space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "日志导出" : "Logs Sink"}</label>
                <Select value={obsLogsSink} onValueChange={setObsLogsSink}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SINK_OPTIONS.map((option) => (
                      <SelectItem key={option || "inherit"} value={option}>
                        {sinkOptionLabel(option, isZh)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "指标导出" : "Metrics Sink"}</label>
                <Select value={obsMetricsSink} onValueChange={setObsMetricsSink}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SINK_OPTIONS.map((option) => (
                      <SelectItem key={option || "inherit"} value={option}>
                        {sinkOptionLabel(option, isZh)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "链路追踪导出" : "Traces Sink"}</label>
                <Select value={obsTracesSink} onValueChange={setObsTracesSink}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {SINK_OPTIONS.map((option) => (
                      <SelectItem key={option || "inherit"} value={option}>
                        {sinkOptionLabel(option, isZh)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  OTLP Endpoint
                  <HelpHint
                    text={
                      isZh
                        ? "例如 http://127.0.0.1:19531，对应 obs_otlp_endpoint"
                        : "e.g. http://127.0.0.1:19531. Maps to obs_otlp_endpoint"
                    }
                  />
                </label>
                <Input
                  placeholder="http://127.0.0.1:19531"
                  value={obsOtlpEndpoint}
                  onChange={(e) => setObsOtlpEndpoint(e.target.value)}
                />
              </div>
            </div>
            <div className="grid grid-cols-3 gap-3">
              <div className="space-y-1.5">
                <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                  {isZh ? "日志保留（天）" : "Logs Retention (days)"}
                  <HelpHint
                    text={isZh ? "自动删除超过设置时长的日志" : "Auto-delete logs older than the configured period"}
                  />
                </label>
                <Input
                  type="number"
                  min={1}
                  max={365}
                  placeholder={OBS_LOGS_RETENTION_DEFAULT}
                  value={obsLogsRetention}
                  onChange={(e) => setObsLogsRetention(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "指标保留（天）" : "Metrics Retention (days)"}</label>
                <Input
                  type="number"
                  min={1}
                  max={365}
                  placeholder={OBS_METRICS_RETENTION_DEFAULT}
                  value={obsMetricsRetention}
                  onChange={(e) => setObsMetricsRetention(e.target.value)}
                />
              </div>
              <div className="space-y-1.5">
                <label className="ml-1 text-xs text-slate-700">{isZh ? "链路保留（天）" : "Traces Retention (days)"}</label>
                <Input
                  type="number"
                  min={1}
                  max={365}
                  placeholder={OBS_TRACES_RETENTION_DEFAULT}
                  value={obsTracesRetention}
                  onChange={(e) => setObsTracesRetention(e.target.value)}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                onClick={handleSaveObs}
                disabled={saveObsMut.isPending || !obsDirty}
                size="sm"
                className="flex items-center gap-1.5"
              >
                {saveObsMut.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save className="h-3.5 w-3.5" />
                )}
                {isZh ? "保存" : "Save"}
              </Button>
              {obsDirty && (
                <p className="text-xs text-amber-600">
                  {isZh ? "配置已修改，保存后生效" : "Unsaved changes, save to apply"}
                </p>
              )}
            </div>
          </div>
        </div>
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
