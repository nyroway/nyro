import { useEffect, useRef, useState, type ReactNode } from "react";
import { MoreHorizontal } from "lucide-react";

/* 行内“更多”菜单（真源 .action-menu / .row-menu）：点图标展开小菜单，
   点菜单项或外部即收起；显隐交给 nyro-ui.css 的 .open 语义，
   这里只负责开合状态、外点关闭与贴底翻转。 */
export function RowActionMenu({ label, children }: { label: string; children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [open]);

  useEffect(() => {
    if (!open) return;
    // 真源 toggleRowMenu：展开后量面板底缘，越出视口 12px 安全界即翻到
    // 图标上方（.row-menu.flip-up，nyro-ui.css）。
    const panel = rootRef.current?.querySelector(".row-menu");
    if (!panel) return;
    panel.classList.remove("flip-up");
    const box = panel.getBoundingClientRect();
    if (box.bottom > window.innerHeight - 12) panel.classList.add("flip-up");
  }, [open]);

  return (
    <div className={open ? "action-menu open" : "action-menu"} ref={rootRef}>
      <button
        type="button"
        className="icon-action"
        data-tip={label}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={(event) => {
          event.stopPropagation();
          setOpen(!open);
        }}
      >
        <MoreHorizontal aria-hidden="true" />
      </button>
      {/* 菜单项自带 onClick：先执行动作，随后这里收起菜单并挡住行点击。 */}
      <div
        className="row-menu row-menu-wide"
        role="menu"
        onClick={(event) => {
          event.stopPropagation();
          setOpen(false);
        }}
      >
        {children}
      </div>
    </div>
  );
}
