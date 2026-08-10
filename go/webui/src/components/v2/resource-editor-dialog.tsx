import type { ReactNode } from "react";

import { Dialog, DialogContent, DialogDescription, DialogTitle } from "@/components/ui/dialog";

export type ResourceEditorDialogProps = {
  open: boolean;
  title: ReactNode;
  description?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  className?: string;
};

type ResourceEditorFrameProps = Omit<ResourceEditorDialogProps, "open" | "className">;

export function ResourceEditorFrame({
  title,
  description,
  onClose,
  children,
  footer,
}: ResourceEditorFrameProps) {
  return (
    <>
      <header className="v2-resource-dialog-head">
        <div>
          <DialogTitle>{title}</DialogTitle>
          {description && <DialogDescription>{description}</DialogDescription>}
        </div>
        <button type="button" className="v2-resource-dialog-close" onClick={onClose} aria-label="Close">×</button>
      </header>
      <div className="v2-resource-dialog-body">{children}</div>
      {footer && <footer className="v2-resource-dialog-foot">{footer}</footer>}
    </>
  );
}

export function ResourceEditorDialog({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  className = "",
}: ResourceEditorDialogProps) {
  return (
    <Dialog open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className={`v2-resource-dialog ${className}`.trim()} showCloseButton={false}>
        <ResourceEditorFrame
          title={title}
          description={description}
          onClose={onClose}
          footer={footer}
        >
          {children}
        </ResourceEditorFrame>
      </DialogContent>
    </Dialog>
  );
}
