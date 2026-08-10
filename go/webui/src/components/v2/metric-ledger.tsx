import type { ReactNode } from "react";

export type MetricLedgerItem = {
  key: string;
  label: ReactNode;
  value: ReactNode;
  detail?: ReactNode;
  tone?: "default" | "success" | "danger" | "warning";
};

export function MetricLedger({ items }: { items: MetricLedgerItem[] }) {
  return (
    <section className="v2-metric-ledger" aria-label="Metrics">
      {items.map((item) => (
        <div className={`v2-ledger-item tone-${item.tone ?? "default"}`} key={item.key}>
          <span>{item.label}</span>
          <strong>{item.value}</strong>
          {item.detail && <small>{item.detail}</small>}
        </div>
      ))}
    </section>
  );
}
