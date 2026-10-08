import type { ReactNode } from "react";

export function FilterBar({ children, summary }: { children: ReactNode; summary?: ReactNode }) {
  return (
    <section className="table-toolbar" aria-label="Filters">
      {children}
      {summary && <span className="table-summary">{summary}</span>}
    </section>
  );
}
