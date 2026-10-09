import type { ReactNode } from "react";

import * as DialogPrimitive from "@radix-ui/react-dialog";

/* 浮层两态（对齐真源 new-webui）：
   · 新增/编辑表单 → 右侧抽屉（providers.html #addProviderDrawer、
     waf-gateway.html #createDrawer：drawer drawer-wide 720px）；
   · 探测/一次性令牌等结果型浮层 → 居中 modal over-drawer
     （providers.html #probeModal，可叠在抽屉之上，z-40）。
   Radix 只负责行为（焦点圈定/ESC/portal/aria），data-state 显隐
   由 radix-bridge.css 翻译成基线的 .open 语义。 */

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
 * 模态三段式骨架（头部/滚动卡体/操作脚）。
 * 直接使用 Radix 原语，使骨架可脱离完整对话框单独静态渲染（测试用）。
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
 * 结果/探测模态：over-drawer 层级保证能从编辑抽屉之上打开。
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
 * 抽屉三段式骨架（drawer-header/滚动体/drawer-footer-end 操作脚）。
 * 与 Inspector 同一套 drawer 词汇，可脱离 Portal 单独静态渲染（测试用）。
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
 * 资源编辑抽屉：新增/编辑表单从右侧滑入（drawer-wide 720px，全高、
 * 卡体独立滚动），遮罩 drawer-mask。
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
