import type { ReactNode } from "react";

export type PageLayoutProps = {
  header: ReactNode;
  children: ReactNode;
};

export function PageLayout({ header, children }: PageLayoutProps) {
  return (
    <div className="content">
      {header}
      <div className="content-body">{children}</div>
    </div>
  );
}
