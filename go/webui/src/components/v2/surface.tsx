import type { ReactNode } from "react";

export type SurfaceProps = {
  title?: ReactNode;
  description?: ReactNode;
  actions?: ReactNode;
  footer?: ReactNode;
  children: ReactNode;
  className?: string;
};

export function Surface({ title, description, actions, footer, children, className = "" }: SurfaceProps) {
  return (
    <section className={`v2-surface ${className}`.trim()}>
      {(title || description || actions) && (
        <header className="v2-surface-head">
          <div>
            {title && <h2>{title}</h2>}
            {description && <p>{description}</p>}
          </div>
          {actions && <div className="v2-surface-actions">{actions}</div>}
        </header>
      )}
      <div className="v2-surface-body">{children}</div>
      {footer && <footer className="v2-surface-foot">{footer}</footer>}
    </section>
  );
}
