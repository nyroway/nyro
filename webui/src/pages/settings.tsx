import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, useRef } from "react";
import { backend, IS_TAURI } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type {
  ExportData,
  GatewayStatus,
  ImportResult,
} from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import {
  Download,
  HelpCircle,
  Upload,
  Save,
  Loader2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Switch } from "@/components/ui/switch";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";

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

  const { data: retentionDays } = useQuery<string | null>({
    queryKey: ["setting", "log_retention_days"],
    queryFn: () => backend("get_setting", { key: "log_retention_days" }),
  });
  const { data: logRecordPayloadsSetting } = useQuery<string | null>({
    queryKey: ["setting", "log_record_payloads"],
    queryFn: () => backend("get_setting", { key: "log_record_payloads" }),
  });
  const { data: proxyEnabledSetting } = useQuery<string | null>({
    queryKey: ["setting", "proxy_enabled"],
    queryFn: () => backend("get_setting", { key: "proxy_enabled" }),
  });
  const { data: proxyUrlSetting } = useQuery<string | null>({
    queryKey: ["setting", "proxy_url"],
    queryFn: () => backend("get_setting", { key: "proxy_url" }),
  });
  const { data: proxyBypassSetting } = useQuery<string | null>({
    queryKey: ["setting", "proxy_bypass"],
    queryFn: () => backend("get_setting", { key: "proxy_bypass" }),
  });

  const [retentionInput, setRetentionInput] = useState<string>("");
  const retentionBaseline = (retentionDays ?? "7").trim();
  const retentionCurrent = retentionInput.trim();
  const retentionDirty = retentionCurrent !== retentionBaseline;
  const logRecordPayloadsEnabled = !["false", "0", "off", "no"].includes(
    (logRecordPayloadsSetting ?? "true").trim().toLowerCase(),
  );
  const [proxyEnabled, setProxyEnabled] = useState(false);
  const [proxyUrl, setProxyUrl] = useState("");
  const [proxyBypass, setProxyBypass] = useState("");
  const normalizedProxyEnabledSetting = ["1", "true", "yes", "on"].includes(
    (proxyEnabledSetting ?? "").trim().toLowerCase(),
  );
  const proxyDirty =
    proxyEnabled !== normalizedProxyEnabledSetting
    || proxyUrl.trim() !== (proxyUrlSetting ?? "").trim()
    || proxyBypass.trim() !== (proxyBypassSetting ?? "").trim();

  useEffect(() => {
    setProxyEnabled(normalizedProxyEnabledSetting);
    setProxyUrl(proxyUrlSetting ?? "");
    setProxyBypass(proxyBypassSetting ?? "");
  }, [normalizedProxyEnabledSetting, proxyUrlSetting, proxyBypassSetting]);

  useEffect(() => {
    setRetentionInput(retentionDays ?? "7");
  }, [retentionDays]);

  function formatErrorMessage(error: unknown) {
    return localizeBackendErrorMessage(error, isZh);
  }

  function showErrorDialog(titleZh: string, titleEn: string, error: unknown) {
    setErrorDialog({
      title: isZh ? titleZh : titleEn,
      description: formatErrorMessage(error),
    });
  }

  const saveSetting = useMutation({
    mutationFn: (value: string) =>
      backend("set_setting", { key: "log_retention_days", value }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["setting", "log_retention_days"] }),
    onError: (error: unknown) => {
      showErrorDialog("保存设置失败", "Failed to save settings", error);
    },
  });
  const saveLogRecordPayloads = useMutation({
    mutationFn: (value: boolean) =>
      backend("set_setting", { key: "log_record_payloads", value: value ? "true" : "false" }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["setting", "log_record_payloads"] }),
    onError: (error: unknown) => {
      showErrorDialog("保存设置失败", "Failed to save settings", error);
    },
  });
  const saveProxyToggle = useMutation({
    mutationFn: async (input: { enabled: boolean; url: string; bypass: string }) => {
      await Promise.all([
        backend("set_setting", { key: "proxy_enabled", value: input.enabled ? "true" : "false" }),
        backend("set_setting", { key: "proxy_url", value: input.url }),
        backend("set_setting", { key: "proxy_bypass", value: input.bypass }),
      ]);
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["setting", "proxy_enabled"] });
      qc.invalidateQueries({ queryKey: ["setting", "proxy_url"] });
      qc.invalidateQueries({ queryKey: ["setting", "proxy_bypass"] });
    },
    onError: (error: unknown) => {
      showErrorDialog("保存代理设置失败", "Failed to save proxy settings", error);
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
    const bypass = proxyBypass.trim();
    if (proxyEnabled && !url) {
      setErrorDialog({
        title: isZh ? "无法保存代理配置" : "Cannot Save Proxy Settings",
        description: isZh ? "请先填写代理 URL，再保存代理配置。" : "Please set proxy URL before saving proxy settings.",
      });
      return;
    }
    saveProxyToggle.mutate(
      {
        enabled: proxyEnabled,
        url,
        bypass,
      },
      {
        onSuccess: () => {
          setProxyUrl(url);
          setProxyBypass(bypass);
          qc.setQueryData(["setting", "proxy_enabled"], proxyEnabled ? "true" : "false");
          qc.setQueryData(["setting", "proxy_url"], url);
          qc.setQueryData(["setting", "proxy_bypass"], bypass);
        },
      },
    );
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
            <p className="text-xs text-slate-500">{isZh ? "代理端口" : "Proxy Port"}</p>
            <p className="mt-1 font-semibold text-slate-900">{status?.proxy_port ?? "–"}</p>
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
                  disabled={saveProxyToggle.isPending}
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
            <div className="space-y-1.5">
              <label className="ml-1 text-xs text-slate-700">{isZh ? "绕过地址（可选）" : "Bypass hosts (optional)"}</label>
              <Input
                placeholder={isZh ? "localhost,127.0.0.1,.internal" : "localhost,127.0.0.1,.internal"}
                value={proxyBypass}
                onChange={(e) => setProxyBypass(e.target.value)}
              />
            </div>
            <div className="flex items-center gap-2">
              <Button
                onClick={handleSaveProxy}
                disabled={saveProxyToggle.isPending || !proxyDirty}
                size="sm"
                className="flex items-center gap-1.5"
              >
                {saveProxyToggle.isPending ? (
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

        {/* Log Configuration */}
        <div className="glass rounded-2xl p-6 space-y-5">
          <h2 className="text-lg font-semibold text-slate-900">{isZh ? "日志配置" : "Log Configuration"}</h2>
          <div className="rounded-xl bg-slate-50 p-4 space-y-3">
            <div className="space-y-1.5">
              <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                {isZh ? "保留时长（天）" : "Retention Period (days)"}
                <HelpHint
                  text={
                    isZh
                      ? "自动删除超过设置时长的日志"
                      : "Auto-delete logs older than the configured period"
                  }
                />
              </label>
              <Input
                type="number"
                min={1}
                max={365}
                value={retentionInput}
                onChange={(e) => setRetentionInput(e.target.value)}
              />
            </div>
            <div className="space-y-1.5">
              <label className="ml-1 flex items-center gap-1 text-xs text-slate-700">
                {isZh ? "记录 Payloads" : "Record Payloads"}
                <HelpHint
                  text={
                    isZh
                      ? "关闭后不再记录请求头、请求体、响应头和响应体"
                      : "When off, request/response headers and bodies are no longer recorded"
                  }
                />
              </label>
              <div className="flex items-center justify-between rounded-lg border border-slate-200 bg-white px-3 py-2.5">
                <div className="flex items-center gap-2">
                  <ToggleStatusLabel enabled={logRecordPayloadsEnabled} isZh={isZh} />
                </div>
                <Switch
                  checked={logRecordPayloadsEnabled}
                  disabled={saveLogRecordPayloads.isPending}
                  onCheckedChange={(checked) => saveLogRecordPayloads.mutate(checked)}
                />
              </div>
            </div>
            <div className="flex items-center gap-2">
              <Button
                onClick={() => saveSetting.mutate(retentionInput)}
                disabled={saveSetting.isPending || !retentionDirty}
                size="sm"
                className="flex items-center gap-1.5"
              >
                {saveSetting.isPending ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <Save className="h-3.5 w-3.5" />
                )}
                {isZh ? "保存" : "Save"}
              </Button>
              {retentionDirty ? (
                <p className="text-xs text-amber-600">
                  {isZh ? "配置已修改，保存后生效" : "Unsaved changes, save to apply"}
                </p>
              ) : saveSetting.isSuccess ? (
                <p className="text-xs text-green-600">{isZh ? "保存成功" : "Saved successfully"}</p>
              ) : null}
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
              disabled={exportMut.isPending}
              size="sm"
              className="flex items-center gap-1.5"
            >
              {exportMut.isPending ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin" />
              ) : (
                <Download className="h-3.5 w-3.5" />
              )}
              {isZh ? "导出" : "Export"}
            </Button>
            <Button
              onClick={() => fileRef.current?.click()}
              disabled={importMut.isPending}
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
