import type { ReactNode } from "react";

import * as DialogPrimitive from "@radix-ui/react-dialog";

/* Two popup states (aligned with the baseline new-webui):
   · Add/edit forms → right-side drawer (providers.html #addProviderDrawer,
     waf-gateway.html #createDrawer: drawer drawer-wide 720px);
   · Result-style popups such as probe/one-time tokens → centered modal over-drawer
     (providers.html #probeModal, can stack above the drawer, z-40).
   Radix only handles behavior (focus trap/ESC/portal/aria); data-state show/hide
   is translated by radix-bridge.css into the baseline's .open semantics. */

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

/**
 * Three-section modal skeleton (header/scrollable card body/action footer).
 * Uses Radix primitives directly so the skeleton can be statically rendered
 * standalone, detached from the full dialog (for tests).
 */
export function ResourceEditorFrame({
  title,
  description,
  onClose,
  children,
  footer,
}: ResourceEditorFrameProps) {
  return (
    <>
      <header className="modal-header">
        <div className="modal-header-copy">
          <DialogPrimitive.Title className="modal-title">{title}</DialogPrimitive.Title>
          {description && <DialogPrimitive.Description className="modal-subtitle">{description}</DialogPrimitive.Description>}
        </div>
        <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
      </header>
      <div className="modal-body">{children}</div>
      {footer && <footer className="modal-footer">{footer}</footer>}
    </>
  );
}

/**
 * Result/probe modal: the over-drawer layer guarantees it can open above the edit drawer.
 */
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
    <DialogPrimitive.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="modal-mask over-drawer" />
        <DialogPrimitive.Content className={`modal modal-form ${className}`.trim()}>
          <ResourceEditorFrame
            title={title}
            description={description}
            onClose={onClose}
            footer={footer}
          >
            {children}
          </ResourceEditorFrame>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

/**
 * Three-section drawer skeleton (drawer-header/scrollable body/drawer-footer-end action footer).
 * Shares the same drawer vocabulary as Inspector; can be statically rendered
 * standalone without the Portal (for tests).
 */
export function ResourceEditorDrawerFrame({
  title,
  description,
  onClose,
  children,
  footer,
}: ResourceEditorFrameProps) {
  return (
    <>
      <header className="drawer-header">
        <div className="drawer-header-copy">
          <DialogPrimitive.Title className="drawer-title">{title}</DialogPrimitive.Title>
          {description && <DialogPrimitive.Description className="drawer-subtitle">{description}</DialogPrimitive.Description>}
        </div>
        <button type="button" className="modal-close" onClick={onClose} aria-label="Close">×</button>
      </header>
      <div className="drawer-body">{children}</div>
      {footer && (
        <footer className="drawer-footer">
          <div className="drawer-footer-end">{footer}</div>
        </footer>
      )}
    </>
  );
}

/**
 * Resource editor drawer: add/edit forms slide in from the right (drawer-wide 720px,
 * full height, card body scrolls independently), masked by drawer-mask.
 */
export function ResourceEditorDrawer({
  open,
  title,
  description,
  onClose,
  children,
  footer,
  className = "",
}: ResourceEditorDialogProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="drawer-mask" />
        <DialogPrimitive.Content className={`drawer drawer-wide ${className}`.trim()}>
          <ResourceEditorDrawerFrame
            title={title}
            description={description}
            onClose={onClose}
            footer={footer}
          >
            {children}
          </ResourceEditorDrawerFrame>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}
