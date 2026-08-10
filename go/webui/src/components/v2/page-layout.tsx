import type { ReactNode } from "react";

export type PageLayoutProps = {
  header: ReactNode;
  children: ReactNode;
};

export function PageLayout({ header, children }: PageLayoutProps) {
  return (
    <div className="v2-page-layout">
      {header}
      <div className="v2-page-body">{children}</div>
    </div>
  );
}
