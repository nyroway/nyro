import type { ReactNode } from "react";

/* 设置页右侧表单卡：基线 config-card 模式（card + card-header + form，
   表单语言参照 providers.html 的新增提供商表单）。 */
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
