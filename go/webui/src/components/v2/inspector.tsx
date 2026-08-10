import type { ReactNode } from "react";

import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

export type InspectorProps = {
  open: boolean;
  title: ReactNode;
  description?: ReactNode;
  dirty?: boolean;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
};

export function Inspector({ open, title, description, onClose, children, footer, className = "" }: InspectorProps) {
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className={`v2-inspector ${className}`.trim()} showCloseButton={false}>
        <header className="v2-inspector-head">
          <div>
            <DialogTitle>{title}</DialogTitle>
            {description && <DialogDescription>{description}</DialogDescription>}
          </div>
          <button type="button" className="v2-inspector-close" onClick={onClose} aria-label="Close">×</button>
        </header>
        <div className="v2-inspector-body">{children}</div>
        {footer && <footer className="v2-inspector-foot">{footer}</footer>}
      </DialogContent>
    </Dialog>
  );
}
