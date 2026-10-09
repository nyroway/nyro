import { Check, Copy, RefreshCw } from "lucide-react";
import { useState } from "react";

import type { MessageKey } from "@/lib/i18n";
import type { RuntimeService, RuntimeServiceID } from "@/lib/types";
import { DataTable, type DataTableColumn } from "@/components/v2/data-table";
import { EmptyState } from "@/components/v2/empty-state";
import { MetricLedger } from "@/components/v2/metric-ledger";
import { Notice } from "@/components/v2/notice";
import { Status } from "@/components/v2/status";

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

  const columns: DataTableColumn<RuntimeService>[] = [
    {
      key: "service",
      header: t("services.service"),
      render: (service) => {
        const meta = SERVICE_META[service.id];
        return (
          <div className="provider-name">
            <span className="provider-logo" aria-hidden="true">{meta.mark}</span>
            <div>
              <strong>{t(meta.name)}</strong>
              <small>{t(meta.technical)} · {t(meta.role)}</small>
            </div>
          </div>
        );
      },
    },
    {
      key: "status",
      header: t("services.status"),
      render: (service) => (
        <Status tone={service.status === "running" ? "success" : "neutral"}>
          {service.status === "running" ? t("common.running") : t("common.disabled")}
        </Status>
      ),
    },
    {
      key: "address",
      header: t("services.address"),
      render: (service) => service.listen ? (
        <span className="cell-copy">
          <code>{service.listen}</code>
          <CopyValue value={service.listen} label={t("common.copy")} />
        </span>
      ) : "—",
    },
    {
      key: "data",
      header: t("services.localData"),
      render: (service) => service.data_path ? (
        <div className="cell-stack">
          <code>{service.data_path}</code>
          {service.storage_backend && <small>{service.storage_backend}</small>}
        </div>
      ) : "—",
    },
    {
      key: "flag",
      header: t("services.flag"),
      render: (service) => <code>{SERVICE_META[service.id].flags}</code>,
    },
  ];

  return (
    <>
      <MetricLedger
        items={[
          { key: "running", label: t("services.runningCount"), value: pending ? "—" : String(runningCount), tone: "success" },
          { key: "disabled", label: t("services.disabledCount"), value: pending ? "—" : String(disabledCount) },
          { key: "checked", label: t("services.checkedAt"), value: isFetching ? t("common.loading") : isError ? "—" : t("services.justNow") },
        ]}
      />

      <DataTable
        carded
        columns={columns}
        rows={services}
        rowKey={(service) => service.id}
        loading={isLoading}
        empty={<EmptyState title={t("services.none")} />}
        toolbar={
          <>
            <Status tone={isError ? "danger" : pending ? "neutral" : "success"}>
              {isError ? t("services.statusUnavailable") : pending ? t("common.loading") : t("services.instanceHealthy")}
            </Status>
            <span className="table-summary">
              {isError ? t("services.none") : t("services.instanceHealthyDetail")}
            </span>
            <button
              type="button"
              className="button button-sm toolbar-end"
              disabled={isFetching}
              onClick={onRefresh}
            >
              <RefreshCw aria-hidden="true" />
              {t("common.refresh")}
            </button>
          </>
        }
        footer={
          <>
            <span className="table-summary">{t("services.scope")}</span>
            <button type="button" className="button button-text button-sm" onClick={onShowNodes}>
              {t("services.viewNodes")} →
            </button>
          </>
        }
      />

      <Notice tone="info" title={t("services.noteTitle")}>
        <p>{t("services.noteDetail")}</p>
        <code>nyro serve --help</code>
      </Notice>
    </>
  );
}

function CopyValue({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="icon-button"
      title={label}
      aria-label={label}
      onClick={() => {
        void navigator.clipboard.writeText(value);
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1200);
      }}
    >
      {copied ? <Check aria-hidden="true" /> : <Copy aria-hidden="true" />}
    </button>
  );
}
