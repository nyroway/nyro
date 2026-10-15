import type { ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";

type FrameProps = {
  title: ReactNode;
  description?: ReactNode;
  onClose: () => void;
  children: ReactNode;
  footer?: ReactNode;
  footerStart?: ReactNode;
};

export function Modal({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  className = "",
}: FrameProps & { open: boolean; className?: string }) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="modal-mask over-drawer" />
        <DialogPrimitive.Content className={`modal modal-form ${className}`.trim()}>
          <header className="modal-header">
            <div className="modal-header-copy">
              <DialogPrimitive.Title className="modal-title">{title}</DialogPrimitive.Title>
              {description ? <DialogPrimitive.Description className="modal-subtitle">{description}</DialogPrimitive.Description> : null}
            </div>
            <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
          </header>
          <div className="modal-body">{children}</div>
          {footer ? <footer className="modal-footer">{footer}</footer> : null}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

export function Drawer({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  footerStart,
  wide = true,
}: FrameProps & { open: boolean; wide?: boolean }) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="drawer-mask" />
        <DialogPrimitive.Content className={wide ? "drawer drawer-wide" : "drawer"}>
          <header className="drawer-header">
            <div className="drawer-header-copy">
              <DialogPrimitive.Title className="drawer-title">{title}</DialogPrimitive.Title>
              {description ? <DialogPrimitive.Description className="drawer-subtitle">{description}</DialogPrimitive.Description> : null}
            </div>
            <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
          </header>
          <div className="drawer-body">{children}</div>
          {footer || footerStart ? (
            <footer className="drawer-footer">
              {footerStart ? <div className="drawer-footer-start">{footerStart}</div> : null}
              {footer ? <div className="drawer-footer-end">{footer}</div> : null}
            </footer>
          ) : null}
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
