import type { ReactNode } from "react";

export type StatusTone = "success" | "warning" | "danger" | "neutral" | "info";

export function Status({ tone, children }: { tone: StatusTone; children: ReactNode }) {
  return <span className={`v2-status status-${tone}`}><i aria-hidden="true" />{children}</span>;
}
