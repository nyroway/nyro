import type { ReactNode } from "react";

export type StatusTone = "success" | "warning" | "danger" | "neutral" | "info";

export function Status({ tone, children }: { tone: StatusTone; children: ReactNode }) {
  return (
    <span className={`tag${tone !== "info" ? ` tag-${tone}` : ""}`}>
      <i className="mini-dot" aria-hidden="true" />
      {children}
    </span>
  );
}
