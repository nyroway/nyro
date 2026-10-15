import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, useRef } from "react";
import { backend } from "@/lib/backend";
import { localizeBackendErrorMessage } from "@/lib/backend-error";
import type { ExportData, GatewayStatus, ImportResult } from "@/lib/types";
import { useLocale } from "@/lib/i18n";
import { ConfirmDialog } from "@/components/ui/confirm-dialog";
import { showToast } from "@/lib/toast";
import { PageHeader, PageLayout } from "@/components/layout/page";
import { localizedMessage, type MessageKey } from "@/lib/messages";


function HelpHint({ text }: { text: string }) {
  return (
    <span className="help" aria-label={text}>
      ?
      <span className="help-tip" role="tooltip">{text}</span>
    </span>
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
    queryKey: ["setting", "enable_payload"],
    queryFn: () => backend("get_setting", { key: "enable_payload" }),
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

  function showErrorDialog(titleKey: MessageKey, error: unknown) {
    setErrorDialog({
      title: localizedMessage(isZh, titleKey),
      description: formatErrorMessage(error),
    });
  }

  const saveSetting = useMutation({
    mutationFn: (value: string) =>
      backend("set_setting", { key: "log_retention_days", value }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["setting", "log_retention_days"] });
      showToast(localizedMessage(isZh, "common.saved"));
    },
    onError: (error: unknown) => {
      showErrorDialog("settings.failedToSaveSettings", error);
    },
  });
  const saveLogRecordPayloads = useMutation({
    mutationFn: (value: boolean) =>
      backend("set_setting", { key: "enable_payload", value: value ? "true" : "false" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["setting", "enable_payload"] });
      showToast(localizedMessage(isZh, "common.saved"));
    },
    onError: (error: unknown) => {
      showErrorDialog("settings.failedToSaveSettings", error);
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
      showToast(localizedMessage(isZh, "common.saved"));
    },
    onError: (error: unknown) => {
      showErrorDialog("settings.failedToSaveProxySettings", error);
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
      showToast(localizedMessage(isZh, "settings.configExported"));
    },
    onError: (error: unknown) => {
      showErrorDialog("settings.failedToExportConfig", error);
    },
  });

  const importMut = useMutation({
    mutationFn: (data: ExportData) =>
      backend<ImportResult>("import_config", { data }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["providers"] });
      qc.invalidateQueries({ queryKey: ["routes"] });
      showToast(localizedMessage(isZh, "settings.configImported"));
    },
    onError: (error: unknown) => {
      showErrorDialog("settings.failedToImportConfig", error);
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
          title: localizedMessage(isZh, "settings.failedToImportConfig"),
          description: localizedMessage(isZh, "settings.invalidJSONFile"),
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
        title: localizedMessage(isZh, "settings.cannotSaveProxySettings"),
        description: localizedMessage(isZh, "settings.pleaseSetProxyURLBeforeSavingProxySettings"),
      });
      return;
    }
    saveProxyToggle.mutate(
      { enabled: proxyEnabled, url, bypass },
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
    <PageLayout
      header={(
        <PageHeader
          title={localizedMessage(isZh, "settings.settings")}
          description={localizedMessage(isZh, "settings.gatewayConfiguration")}
        />
      )}
    >
      <div className="card">
        <div className="card-header"><h2 className="card-title">{localizedMessage(isZh, "settings.gatewayStatus")}</h2></div>
        <div className="card-body">
          <div className="metric-strip">
            <div className="metric-strip-stats cols-4">
              <div className="metric-cell">
                <span className="metric-hint">{localizedMessage(isZh, "common.status")}</span>
                <strong>{status?.status ?? "–"}</strong>
              </div>
              <div className="metric-cell">
                <span className="metric-hint">{localizedMessage(isZh, "settings.proxyPort")}</span>
                <strong>{status?.proxy_port ?? "–"}</strong>
              </div>
              <div className="metric-cell">
                <span className="metric-hint">{localizedMessage(isZh, "settings.mode")}</span>
                <strong>Server</strong>
              </div>
              <div className="metric-cell">
                <span className="metric-hint">{localizedMessage(isZh, "settings.version")}</span>
                <strong>{appVersion}</strong>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div className="split-2">
        <div className="card">
          <div className="card-header"><h2 className="card-title">{localizedMessage(isZh, "settings.proxyConfiguration")}</h2></div>
          <div className="card-body form-stack">
            <div className="switch-inline">
              <span>{proxyEnabled ? (localizedMessage(isZh, "common.enabled2")) : (localizedMessage(isZh, "common.disabled"))}</span>
              <button
                type="button"
                className={proxyEnabled ? "switch-control on" : "switch-control"}
                aria-pressed={proxyEnabled}
                disabled={saveProxyToggle.isPending}
                onClick={() => setProxyEnabled(!proxyEnabled)}
              />
            </div>
            <label className="field">
              <span className="field-label">{localizedMessage(isZh, "settings.proxyURL")}</span>
              <input className="field-control" placeholder="http://127.0.0.1:7890" value={proxyUrl} onChange={(e) => setProxyUrl(e.target.value)} />
            </label>
            <label className="field">
              <span className="field-label">{localizedMessage(isZh, "settings.bypassHostsOptional")}</span>
              <input className="field-control" placeholder="localhost,127.0.0.1,.internal" value={proxyBypass} onChange={(e) => setProxyBypass(e.target.value)} />
            </label>
            <div className="module-actions">
              <button type="button" className="button button-primary button-sm" onClick={handleSaveProxy} disabled={saveProxyToggle.isPending || !proxyDirty}>
                {localizedMessage(isZh, "common.save")}
              </button>
              {proxyDirty ? <p className="module-note">{localizedMessage(isZh, "settings.unsavedChangesSaveToApply")}</p> : null}
            </div>
          </div>
        </div>

        <div className="card">
          <div className="card-header"><h2 className="card-title">{localizedMessage(isZh, "settings.logConfiguration")}</h2></div>
          <div className="card-body form-stack">
            <label className="field">
              <span className="field-label">
                {localizedMessage(isZh, "settings.retentionPeriodDays")}
                <HelpHint text={localizedMessage(isZh, "settings.autoDeleteLogsOlderThanTheConfiguredPeriod")} />
              </span>
              <input className="field-control" type="number" min={1} max={365} value={retentionInput} onChange={(e) => setRetentionInput(e.target.value)} />
            </label>
            <div className="field">
              <span className="field-label">
                {localizedMessage(isZh, "common.recordPayloads")}
                <HelpHint text={localizedMessage(isZh, "settings.masterSwitchWhenOffNoPayloadsAreRecordedWhenOnIndividual")} />
              </span>
              <div className="switch-inline">
                <span>{logRecordPayloadsEnabled ? (localizedMessage(isZh, "common.enabled2")) : (localizedMessage(isZh, "common.disabled"))}</span>
                <button
                  type="button"
                  className={logRecordPayloadsEnabled ? "switch-control on" : "switch-control"}
                  aria-pressed={logRecordPayloadsEnabled}
                  disabled={saveLogRecordPayloads.isPending}
                  onClick={() => saveLogRecordPayloads.mutate(!logRecordPayloadsEnabled)}
                />
              </div>
            </div>
            <div className="module-actions">
              <button type="button" className="button button-primary button-sm" onClick={() => saveSetting.mutate(retentionInput)} disabled={saveSetting.isPending || !retentionDirty}>
                {localizedMessage(isZh, "common.save")}
              </button>
              {retentionDirty ? (
                <p className="module-note">{localizedMessage(isZh, "settings.unsavedChangesSaveToApply")}</p>
              ) : saveSetting.isSuccess ? (
                <p className="module-note">{localizedMessage(isZh, "settings.savedSuccessfully")}</p>
              ) : null}
            </div>
          </div>
        </div>
      </div>

      <div className="card">
        <div className="card-header"><h2 className="card-title">{localizedMessage(isZh, "settings.configBackup")}</h2></div>
        <div className="card-body form-stack">
          <p className="module-note">{localizedMessage(isZh, "settings.exportOrImportProvidersModelsAndSettings")}</p>
          <div className="module-actions">
            <button type="button" className="button button-primary button-sm" onClick={() => exportMut.mutate()} disabled={exportMut.isPending}>
              {localizedMessage(isZh, "settings.export")}
            </button>
            <button type="button" className="button button-secondary button-sm" onClick={() => fileRef.current?.click()} disabled={importMut.isPending}>
              {localizedMessage(isZh, "settings.import")}
            </button>
            <input ref={fileRef} type="file" accept=".json" hidden onChange={handleImportFile} />
          </div>
          {importMut.isSuccess && importMut.data ? (
            <p className="module-note">
              {localizedMessage(isZh, "settings.importedProvidersModelsSettings", { a: (importMut.data as ImportResult).providers_imported, b: (importMut.data as ImportResult).models_imported, c: (importMut.data as ImportResult).settings_imported })}
            </p>
          ) : null}
        </div>
      </div>

      <ConfirmDialog
        open={Boolean(errorDialog)}
        onOpenChange={(open) => { if (!open) setErrorDialog(null); }}
        title={errorDialog?.title ?? ""}
        description={errorDialog?.description}
        hideCancel
        confirmText={localizedMessage(isZh, "common.ok")}
        confirmClassName="button-primary"
        onConfirm={() => setErrorDialog(null)}
      />
    </PageLayout>
  );
}
