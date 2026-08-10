import type { ReactNode } from "react";

export function EmptyState({ title, description, action }: { title: ReactNode; description?: ReactNode; action?: ReactNode }) {
  return (
    <div className="v2-empty-state">
      <strong>{title}</strong>
      {description && <p>{description}</p>}
      {action && <div>{action}</div>}
    </div>
  );
}
