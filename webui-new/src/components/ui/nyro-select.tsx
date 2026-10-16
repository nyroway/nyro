import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
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
  placeholder?: string;
  searchable?: boolean;
  searchPlaceholder?: string;
  emptyText?: string;
  /** When the search has no exact match, submit the typed text as the value (custom model IDs). */
  allowCreate?: boolean;
};

type MenuBox = {
  top: number;
  left: number;
  width: number;
  maxHeight: number;
  adaptive: boolean;
};

function layoutSelectMenu(trigger: HTMLElement, adaptive: boolean): MenuBox {
  const rect = trigger.getBoundingClientRect();
  const gap = 8;
  const pad = 8;
  const preferred = 280;
  const spaceBelow = window.innerHeight - rect.bottom - gap - pad;
  const spaceAbove = rect.top - gap - pad;
  const openUp = spaceBelow < 120 && spaceAbove > spaceBelow;
  const maxHeight = Math.min(preferred, Math.max(96, openUp ? spaceAbove : spaceBelow));
  const width = Math.max(rect.width, 160);
  const estimatedWidth = adaptive ? Math.min(360, Math.max(width, 240)) : width;
  let left = rect.left;
  if (left + estimatedWidth > window.innerWidth - pad) {
    left = Math.max(pad, window.innerWidth - pad - estimatedWidth);
  }
  const top = openUp
    ? Math.max(pad, rect.top - gap - maxHeight)
    : rect.bottom + gap;
  return { top, left, width, maxHeight, adaptive };
}

export function isSelectMenuEventTarget(target: EventTarget | null) {
  return target instanceof Element && Boolean(target.closest(".select-menu"));
}

/** Baseline select: button trigger + portaled menu so drawers/cards cannot clip the list. */
export function NyroSelect({
  value,
  options,
  onChange,
  ariaLabel,
  filter = false,
  controlClassName,
  placeholder,
  searchable = false,
  searchPlaceholder,
  emptyText,
  allowCreate = false,
}: NyroSelectProps) {
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [box, setBox] = useState<MenuBox | null>(null);
  const selected = options.find((option) => option.value === value);
  const adaptive = Boolean(controlClassName?.includes("toolbar-filter-adaptive"));

  const filtered = useMemo(() => {
    if (!searchable) return options;
    const needle = query.trim().toLowerCase();
    if (!needle) return options;
    return options.filter((option) => option.label.toLowerCase().includes(needle));
  }, [options, query, searchable]);

  const createValue = query.trim();
  const showCreate = Boolean(
    allowCreate
      && createValue
      && !options.some((option) => option.value === createValue || option.label.toLowerCase() === createValue.toLowerCase()),
  );

  useLayoutEffect(() => {
    if (!open) {
      setBox(null);
      return;
    }
    const trigger = triggerRef.current;
    if (!trigger) return;
    const update = () => setBox(layoutSelectMenu(trigger, adaptive));
    update();
    window.addEventListener("resize", update);
    document.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      document.removeEventListener("scroll", update, true);
    };
  }, [open, options.length, adaptive]);

  useEffect(() => {
    if (!open) return;
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Node;
      if (rootRef.current?.contains(target) || menuRef.current?.contains(target)) return;
      setOpen(false);
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

  function selectValue(next: string) {
    onChange(next);
    setOpen(false);
    setQuery("");
  }

  const menu = open && box && typeof document !== "undefined"
    ? createPortal(
        <div
          ref={menuRef}
          id={menuId}
          className={clsx(
            "select-menu is-fixed",
            adaptive && "is-adaptive",
            filtered.length === 0 && !showCreate && "is-empty",
          )}
          role="listbox"
          aria-label={ariaLabel}
          style={{
            top: `${box.top}px`,
            left: `${box.left}px`,
            width: box.adaptive ? undefined : `${box.width}px`,
            minWidth: `${box.width}px`,
            maxHeight: `${box.maxHeight}px`,
          }}
          onWheel={(event) => event.stopPropagation()}
        >
          {searchable ? (
            <div className="searchable-select-search">
              <input
                className="searchable-select-input"
                value={query}
                placeholder={searchPlaceholder ?? placeholder ?? ""}
                aria-label={searchPlaceholder ?? ariaLabel ?? "Search"}
                autoFocus
                onChange={(event) => setQuery(event.target.value)}
                onKeyDown={(event) => {
                  if (event.key === "Enter") {
                    event.preventDefault();
                    if (filtered[0]) {
                      selectValue(filtered[0].value);
                      return;
                    }
                    if (showCreate) selectValue(createValue);
                  }
                }}
              />
            </div>
          ) : null}
          <div className="select-menu-list">
            {searchable && filtered.length === 0 && !showCreate && emptyText ? (
              <div className="searchable-select-empty">{emptyText}</div>
            ) : null}
            {showCreate ? (
              <button
                type="button"
                role="option"
                aria-selected={false}
                className="select-option"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => selectValue(createValue)}
              >
                <span>{createValue}</span>
              </button>
            ) : null}
            {filtered.map((option) => (
              <button
                key={option.value}
                type="button"
                role="option"
                aria-selected={option.value === value}
                className={option.value === value ? "select-option selected" : "select-option"}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => selectValue(option.value)}
              >
                <span>{option.label}</span>
              </button>
            ))}
          </div>
        </div>,
        document.body,
      )
    : null;

  return (
    <div ref={rootRef} className={clsx("select-control", controlClassName, open && "open")}>
      <button
        ref={triggerRef}
        type="button"
        className="field-control"
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? menuId : undefined}
        onClick={() => {
          setOpen((prev) => {
            const next = !prev;
            if (next) setQuery("");
            return next;
          });
        }}
      >
        {filter ? <Filter aria-hidden="true" /> : null}
        <span className={clsx("selected-value", !selected && "is-placeholder")}>
          {selected?.label ?? placeholder ?? ""}
        </span>
        <span className="control-icon">
          <ChevronDown aria-hidden="true" />
        </span>
      </button>
      {menu}
    </div>
  );
}
