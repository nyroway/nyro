import { Copy, RefreshCw } from "lucide-react";

import type { MessageKey } from "@/lib/i18n";
import type { RuntimeService, RuntimeServiceID } from "@/lib/types";

type Translate = (key: MessageKey, params?: Record<string, string | number>) => string;

interface ServiceMeta {
  mark: string;
  name: MessageKey;
  technical: MessageKey;
  role: MessageKey;
  flags: string;
}

const SERVICE_META: Record<RuntimeServiceID, ServiceMeta> = {
  "control-plane": {
    mark: "CP",
    name: "services.control.name",
    technical: "services.control.technical",
    role: "services.control.role",
    flags: "--listen",
  },
  "embedded-proxy": {
    mark: "DP",
    name: "services.proxy.name",
    technical: "services.proxy.technical",
    role: "services.proxy.role",
    flags: "--proxy-listen / --disable-proxy",
  },
  "redis-state": {
    mark: "ST",
    name: "services.redis.name",
    technical: "services.redis.technical",
    role: "services.redis.role",
    flags: "--redis-listen / --disable-redis",
  },
  "otlp-receiver": {
    mark: "OT",
    name: "services.otlp.name",
    technical: "services.otlp.technical",
    role: "services.otlp.role",
    flags: "--otlp-listen / --disable-otlp",
  },
};

interface RuntimeServicesViewProps {
  services: RuntimeService[];
  isLoading: boolean;
  isError: boolean;
  isFetching: boolean;
  t: Translate;
  onRefresh: () => void;
  onShowNodes: () => void;
}

export function RuntimeServicesView({
  services,
  isLoading,
  isError,
  isFetching,
  t,
  onRefresh,
  onShowNodes,
}: RuntimeServicesViewProps) {
  const runningCount = services.filter((service) => service.status === "running").length;
  const disabledCount = services.filter((service) => service.status === "disabled").length;
  const pending = isLoading && services.length === 0;
  const summaryTitle = isError
    ? t("services.statusUnavailable")
    : pending
      ? t("common.loading")
      : t("services.instanceHealthy");
  const summaryDetail = isError ? t("services.none") : t("services.instanceHealthyDetail");

  return (
    <>
      <section
        className={`v2-status-ribbon v2-services-ribbon${isError || pending ? " state-unknown" : ""}`}
        aria-live="polite"
      >
        <div className="v2-status-ribbon-main">
          <h2>{summaryTitle}</h2>
          <p>{summaryDetail}</p>
        </div>
        <ServiceStat label={t("services.runningCount")} value={pending ? "—" : String(runningCount)} />
        <ServiceStat label={t("services.disabledCount")} value={pending ? "—" : String(disabledCount)} />
        <ServiceStat
          label={t("services.checkedAt")}
          value={isFetching ? t("common.loading") : isError ? "—" : t("services.justNow")}
        />
      </section>

      <section className="v2-surface v2-service-table" aria-busy={isFetching}>
        <div className="v2-surface-head">
          <div>
            <h2>{t("services.tableTitle")}</h2>
            <p>{t("services.tableDetail")}</p>
          </div>
          <button
            type="button"
            className="v2-button v2-button-icon"
            disabled={isFetching}
            onClick={onRefresh}
          >
            <RefreshCw aria-hidden="true" />
            {t("common.refresh")}
          </button>
        </div>
        <div className="v2-table-wrap">
          <table className="v2-data-table">
            <thead>
              <tr>
                <th>{t("services.service")}</th>
                <th>{t("services.status")}</th>
                <th>{t("services.address")}</th>
                <th>{t("services.localData")}</th>
                <th>{t("services.flag")}</th>
              </tr>
            </thead>
            <tbody>
              {services.map((service) => {
                const meta = SERVICE_META[service.id];
                return (
                  <tr key={service.id}>
                    <td>
                      <div className="v2-service-name">
                        <span className="v2-service-mark" aria-hidden="true">{meta.mark}</span>
                        <span>
                          <strong>{t(meta.name)}</strong>
                          <small>{t(meta.technical)} · {t(meta.role)}</small>
                        </span>
                      </div>
                    </td>
                    <td>
                      <span className={`v2-status status-${service.status}`}>
                        <i aria-hidden="true" />
                        {service.status === "running" ? t("common.running") : t("common.disabled")}
                      </span>
                    </td>
                    <td>
                      <span className="v2-service-address">
                        <code>{service.listen || "—"}</code>
                        <CopyValue value={service.listen} label={t("common.copy")} />
                      </span>
                    </td>
                    <td>
                      <span className="v2-service-data">
                        <code>{service.data_path || "—"}</code>
                        {service.storage_backend && <small>{service.storage_backend}</small>}
                      </span>
                    </td>
                    <td><code className="v2-service-flag">{meta.flags}</code></td>
                  </tr>
                );
              })}
              {!isLoading && (isError || services.length === 0) && (
                <tr><td colSpan={5}><div className="v2-inline-empty">{t("services.none")}</div></td></tr>
              )}
              {pending && (
                <tr><td colSpan={5}><div className="v2-inline-empty">{t("common.loading")}</div></td></tr>
              )}
            </tbody>
          </table>
        </div>
        <div className="v2-surface-foot">
          <span>{t("services.scope")}</span>
          <button type="button" onClick={onShowNodes}>{t("services.viewNodes")} →</button>
        </div>
      </section>

      <section className="v2-service-note">
        <span aria-hidden="true">i</span>
        <div>
          <strong>{t("services.noteTitle")}</strong>
          <p>{t("services.noteDetail")}</p>
        </div>
        <code>nyro serve --help</code>
      </section>
    </>
  );
}

function ServiceStat({ label, value }: { label: string; value: string }) {
  return <div className="v2-status-ribbon-stat"><span>{label}</span><strong>{value}</strong></div>;
}

function CopyValue({ value, label }: { value?: string; label: string }) {
  if (!value) return null;
  return (
    <button
      type="button"
      className="v2-copy-button"
      title={label}
      onClick={() => void navigator.clipboard.writeText(value)}
    >
      <Copy aria-hidden="true" />
    </button>
  );
}
