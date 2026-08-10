import type { ReactNode } from "react";

export function Notice({ tone = "info", title, children }: { tone?: "info" | "warning" | "danger" | "success"; title?: ReactNode; children: ReactNode }) {
  return (
    <div className={`v2-notice notice-${tone}`} role={tone === "danger" ? "alert" : "status"}>
      {title && <strong>{title}</strong>}
      <div>{children}</div>
    </div>
  );
}
