import type { ReactNode } from "react";

export function SettingsFormSurface({ title, description, badge, children }: { title: ReactNode; description?: ReactNode; badge?: ReactNode; children: ReactNode }) {
  return (
    <section className="v2-setting-card">
      <header><div><h2>{title}</h2>{description && <p>{description}</p>}</div>{badge}</header>
      <div className="v2-setting-body">{children}</div>
    </section>
  );
}
