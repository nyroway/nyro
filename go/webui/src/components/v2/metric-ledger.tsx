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
    <section className="metric-strip" aria-label="Metrics">
      <div className={`metric-strip-stats${items.length !== 5 ? ` cols-${items.length}` : ""}`}>
        {items.map((item) => (
          <div
            className={`metric-cell${item.tone && item.tone !== "default" ? ` tone-${item.tone}` : ""}`}
            key={item.key}
          >
            <span>{item.label}</span>
            <strong>{item.value}</strong>
            {item.detail && <i className="metric-hint">{item.detail}</i>}
          </div>
        ))}
      </div>
    </section>
  );
}
