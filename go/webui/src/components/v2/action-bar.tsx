import type { ReactNode } from "react";

export function ActionBar({ status, secondary, primary }: { status?: ReactNode; secondary?: ReactNode; primary: ReactNode }) {
  return (
    <div className="v2-action-bar">
      <div className="v2-action-status">{status}</div>
      <div className="v2-action-buttons">{secondary}{primary}</div>
    </div>
  );
}
