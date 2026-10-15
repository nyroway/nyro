import { useEffect, useId, useRef, useState } from "react";
import { ChevronDown, Filter } from "lucide-react";
import clsx from "clsx";

export type NyroSelectOption = {
  value: string;
  label: string;
};

type NyroSelectProps = {
  value: string;
  options: readonly NyroSelectOption[];
  onChange: (value: string) => void;
  ariaLabel?: string;
  /** Toolbar filters use the funnel icon from the provider list. */
  filter?: boolean;
  controlClassName?: string;
};

/** Baseline select: button trigger, in-place menu, same colors as the provider page. */
export function NyroSelect({
  value,
  options,
  onChange,
  ariaLabel,
  filter = false,
  controlClassName,
}: NyroSelectProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const selected = options.find((option) => option.value === value);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: MouseEvent) {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === "Escape") setOpen(false);
    }
    document.addEventListener("mousedown", onPointerDown);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("mousedown", onPointerDown);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [open]);

  return (
    <div ref={rootRef} className={clsx("select-control", controlClassName, open && "open")}>
      <button
        type="button"
        className="field-control"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => setOpen((prev) => !prev)}
      >
        {filter ? <Filter aria-hidden="true" /> : null}
        <span className={clsx("selected-value", !selected && "is-placeholder")}>
          {selected?.label ?? ""}
        </span>
        <span className="control-icon">
          <ChevronDown aria-hidden="true" />
        </span>
      </button>
      {open ? (
        <div id={menuId} className="select-menu" role="listbox" aria-label={ariaLabel}>
          {options.map((option) => (
            <button
              key={option.value}
              type="button"
              role="option"
              aria-selected={option.value === value}
              className={option.value === value ? "select-option selected" : "select-option"}
              onClick={() => {
                onChange(option.value);
                setOpen(false);
              }}
            >
              <span>{option.label}</span>
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
