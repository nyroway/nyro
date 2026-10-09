import type { ReactNode } from "react";
import * as DialogPrimitive from "@radix-ui/react-dialog";

/**
 * 确认/警示对话框：Radix 负责行为（焦点圈定/ESC/portal/aria），
 * 外观走 nyro 的 modal 词汇（go-webui-改造方案.md §6.3 / §9.6④）。
 */
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
        <DialogPrimitive.Overlay className="modal-mask" />
        <DialogPrimitive.Content className="modal">
          <header className="modal-header">
            <div className="modal-header-copy">
              <DialogPrimitive.Title className="modal-title">{title}</DialogPrimitive.Title>
              {description && (
                <DialogPrimitive.Description className="modal-subtitle">
                  {description}
                </DialogPrimitive.Description>
              )}
            </div>
            <button type="button" className="modal-close" aria-label="Close" onClick={() => onOpenChange(false)}>
              ×
            </button>
          </header>
          {content && <div className="modal-body">{content}</div>}
          <footer className="modal-footer">
            {!hideCancel && (
              <button type="button" className="button" onClick={() => onOpenChange(false)}>
                {cancelText}
              </button>
            )}
            <button
              type="button"
              /* confirmClassName 是完整类名（如 "button-primary"）：整体替换默认的
                 button-danger，而非叠加——叠加会让两个背景类同时落在按钮上，
                 red/蓝谁后定义谁生效（导入模型弹层的确认按钮因此一直是红色）。 */
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
