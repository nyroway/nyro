import type { ReactNode } from "react";

export function FormSection({ title, description, children }: { title: ReactNode; description?: ReactNode; children: ReactNode }) {
  return (
    <section className="form-section">
      <h3 className="form-section-title">{title}</h3>
      {description && <p className="form-section-desc">{description}</p>}
      <div className="form-grid">{children}</div>
    </section>
  );
}
