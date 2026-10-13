import type { ReactNode } from "react";

import * as DialogPrimitive from "@radix-ui/react-dialog";

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

/**
 * Details drawer: Radix handles behavior (focus trap/ESC/portal/aria), while the look
 * follows nyro's drawer vocabulary.
 */
export function Inspector({ open, title, description, onClose, children, footer, className = "" }: InspectorProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="drawer-mask" />
        <DialogPrimitive.Content className={`drawer ${className}`.trim()}>
          <header className="drawer-header">
            <div className="drawer-header-copy">
              <DialogPrimitive.Title className="drawer-title">{title}</DialogPrimitive.Title>
              {description && <DialogPrimitive.Description className="drawer-subtitle">{description}</DialogPrimitive.Description>}
            </div>
            <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
          </header>
          <div className="drawer-body">{children}</div>
          {footer && <footer className="drawer-footer">{footer}</footer>}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
