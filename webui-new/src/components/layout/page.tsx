import type { ReactNode } from "react";

export function PageLayout({ header, children }: { header: ReactNode; children: ReactNode }) {
  return (
    <div className="content">
      {header}
      <div className="content-body">{children}</div>
    </div>
  );
}

export function PageHeader({
  title,
  description,
  actions,
}: {
  title: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <header className={actions ? "page-header has-actions" : "page-header"}>
      <div>
        <h1>{title}</h1>
        {description ? <p>{description}</p> : null}
      </div>
      {actions ? <div className="page-header-actions">{actions}</div> : null}
    </header>
  );
}
