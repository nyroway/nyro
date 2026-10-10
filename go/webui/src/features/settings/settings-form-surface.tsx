import type { ReactNode } from "react";

/* Right-side form card for settings pages: baseline config-card pattern (card + card-header + form,
   form language modeled on providers.html's add-provider form). */
export function SettingsFormSurface({ title, description, badge, children }: { title: ReactNode; description?: ReactNode; badge?: ReactNode; children: ReactNode }) {
  return (
    <section className="card config-card">
      <header className="card-header">
        <div className="card-header-copy">
          <h2 className="card-title">{title}</h2>
          {description && <p className="card-subtitle">{description}</p>}
        </div>
        {badge}
      </header>
      <form className="form" onSubmit={(event) => event.preventDefault()}>
        {children}
      </form>
    </section>
  );
}
