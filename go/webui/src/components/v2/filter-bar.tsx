import type { ReactNode } from "react";

export function FilterBar({ children, summary }: { children: ReactNode; summary?: ReactNode }) {
  return (
    <section className="v2-filter-bar" aria-label="Filters">
      <div className="v2-filter-controls">{children}</div>
      {summary && <div className="v2-filter-summary">{summary}</div>}
    </section>
  );
}
