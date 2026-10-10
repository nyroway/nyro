import { useEffect, useRef, useState, type ReactNode } from "react";
import { MoreHorizontal } from "lucide-react";

/* Inline "More" menu (baseline .action-menu / .row-menu): clicking the icon opens a small menu,
   clicking a menu item or outside collapses it; show/hide is left to nyro-ui.css's .open semantics,
   and this component only handles open/close state, outside-click closing, and bottom-edge flip. */
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
    // Baseline toggleRowMenu: after expanding, measure the panel's bottom edge; if it exceeds
    // the viewport's 12px safety margin, flip it above the icon (.row-menu.flip-up, nyro-ui.css).
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
      {/* Menu items carry their own onClick: run the action first, then collapse the menu here and block the row click. */}
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
