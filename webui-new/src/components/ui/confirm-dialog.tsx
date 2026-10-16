import type { ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";

type ConfirmDialogProps = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  content?: ReactNode;
  hideCancel?: boolean;
  cancelText?: string;
  confirmText?: string;
  onConfirm: () => void;
  confirmClassName?: string;
};

function ConfirmDialog({
  open,
  onOpenChange,
  title,
  description,
  content,
  hideCancel = false,
  cancelText = "Cancel",
  confirmText = "Confirm",
  onConfirm,
  confirmClassName,
}: ConfirmDialogProps) {
  return (
    <DialogPrimitive.Root open={open} onOpenChange={onOpenChange}>
      <DialogPrimitive.Portal>
        <DialogPrimitive.Overlay className="modal-mask over-drawer" />
        <DialogPrimitive.Content className="modal">
          <header className="modal-header">
            <div className="modal-header-copy">
              <DialogPrimitive.Title className="modal-title">{title}</DialogPrimitive.Title>
              {description ? (
                <DialogPrimitive.Description className="modal-subtitle">{description}</DialogPrimitive.Description>
              ) : null}
            </div>
            <button type="button" className="modal-close" aria-label="Close" onClick={() => onOpenChange(false)}>×</button>
          </header>
          {content ? <div className="modal-body">{content}</div> : null}
          <footer className="modal-footer">
            {!hideCancel && (
              <button type="button" className="button" onClick={() => onOpenChange(false)}>
                {cancelText}
              </button>
            )}
            <button
              type="button"
              className={`button ${confirmClassName ?? "button-danger"}`}
              onClick={onConfirm}
            >
              {confirmText}
            </button>
          </footer>
        </DialogPrimitive.Content>
      </DialogPrimitive.Portal>
    </DialogPrimitive.Root>
  );
}

export { ConfirmDialog };
