import type { ReactNode } from "react";

export function FormSection({ title, description, children }: { title: ReactNode; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="v2-form-section">
      <header><h3>{title}</h3>{description && <p>{description}</p>}</header>
      <div className="v2-form-grid">{children}</div>
    </section>
  );
}
