import type { ReactNode } from "react";

export function DashboardWorkspace({ children }: { children: ReactNode }) {
  return <section className="v2-dashboard-workspace">{children}</section>;
}
