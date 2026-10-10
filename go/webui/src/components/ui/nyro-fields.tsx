/* eslint-disable react-hooks/set-state-in-effect */

import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { Dispatch, KeyboardEvent as ReactKeyboardEvent, ReactNode, RefObject, SetStateAction } from "react";
import clsx from "clsx";
import { Check, ChevronDown } from "lucide-react";

import { helpTipLayout } from "@/lib/help-tip-layout";

type BaseFieldProps = {
  label?: string;
  className?: string;
  fullWidth?: boolean;
  /** Validation error: red control border (.is-error) + field-hint error. */
  error?: string;
  /** Regular helper text (field-hint). */
  hint?: string;
  /** Tip text for the "?" help button next to the field-label. */
  help?: string;
  /** Required asterisk (.required, purely visual; the native required attribute is not set to avoid triggering browser validation). */
  required?: boolean;
};

export type NyroTextFieldProps = BaseFieldProps &
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "size"> & {
    numbersOnly?: boolean;
  };

export type NyroTextareaFieldProps = BaseFieldProps &
  React.TextareaHTMLAttributes<HTMLTextAreaElement>;

/* "?" help button inside the field-label: the baseline .help round question
   mark + hover tip (nyro-app.css .help-tip, colors same as the baseline
   hover-tip). On hover the tip layer switches to fixed positioning and clamps
   to the viewport — the baseline hover-tip is itself a body-level fixed layer
   (the overflow of .drawer-body/.card would clip an edge-hugging absolutely
   positioned popup; fixed escapes the clipping ancestor); the .drawer open
   state carries a transform that rewrites the containing block of fixed, so
   the "zero out, read the baseline, then compensate" two-pass approach lands
   on the viewport target under any containing block. The placement geometry
   is the pure function helpTipLayout (lib/help-tip-layout, unit-testable). */
export function NyroHelpHint({ text }: { text: string }) {
  const buttonRef = useRef<HTMLButtonElement | null>(null);
  const tipRef = useRef<HTMLSpanElement | null>(null);

  const placeTip = () => {
    const button = buttonRef.current;
    const tip = tipRef.current;
    if (!button || !tip) return;
    tip.style.position = "fixed";
    tip.style.transform = "none";
    tip.style.bottom = "auto";
    tip.style.left = "0px";
    tip.style.top = "0px";
    const base = tip.getBoundingClientRect();
    if (!base.width || !base.height) return; // Not shown (e.g. focus without focus-visible); no placement needed
    const { left, top } = helpTipLayout(
      button.getBoundingClientRect(),
      { w: base.width, h: base.height },
      { w: window.innerWidth, h: window.innerHeight },
    );
    // base is the actual placement of the "zeroed-out position" in the viewport: target minus baseline yields the compensation under any containing block
    tip.style.left = `${left - base.left}px`;
    tip.style.top = `${top - base.top}px`;
  };

  return (
    <button
      type="button"
      className="help"
      aria-label="help"
      ref={buttonRef}
      onMouseEnter={placeTip}
      onFocus={placeTip}
    >
      ?
      <span className="help-tip" role="tooltip" ref={tipRef}>{text}</span>
    </button>
  );
}

function FieldHint({ error, hint }: { error?: string; hint?: string }) {
  if (error) return <span className="field-hint error">{error}</span>;
  if (hint) return <span className="field-hint">{hint}</span>;
  return null;
}

/* ── Baseline lifecycle of the select family ─────────────────────────────────────────
   The baseline toggleSelect/toggleCustomMulti "open me, close the others":
   only one select may be open on the whole page at any moment. The opening
   component registers its own close function in a module-level registry; a new
   open action first closes the other instances; it deregisters on
   close/unmount. */
const openSelectClosers = new Set<() => void>();

function claimSelectMenu(close: () => void) {
  for (const other of openSelectClosers) other();
  openSelectClosers.add(close);
}

function releaseSelectMenu(close: () => void) {
  openSelectClosers.delete(close);
}

/* Common behavior of the select toggle (baseline semantics):
   - The menu renders in place as a child of .select-control/.custom-multi (no
     body portal, no fixed/z-index); the open state is just an .open class, and
     the baseline CSS's .select-control.open .select-menu /
     .custom-multi.open .custom-multi-options takes over display — the menu thus
     naturally lives inside the stacking context of the drawer/modal and cannot
     be covered by it;
   - A document-level mousedown outside the root node closes the menu (the
     baseline document click listener); the root node covers both the trigger
     and the in-place menu;
   - On open, the menu is scrolled into the nearest scrollable ancestor's
     visible area (needed by forms at the bottom of a drawer). */
function useBaselineSelectOpen(
  rootRef: RefObject<HTMLElement | null>,
  open: boolean,
  setOpen: Dispatch<SetStateAction<boolean>>,
  menuRef: RefObject<HTMLElement | null>,
) {
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    claimSelectMenu(close);
    function onPointerDown(event: MouseEvent) {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    }
    document.addEventListener("mousedown", onPointerDown);
    return () => {
      releaseSelectMenu(close);
      document.removeEventListener("mousedown", onPointerDown);
    };
  }, [open, rootRef, setOpen]);

  useEffect(() => {
    if (open) menuRef.current?.scrollIntoView({ block: "nearest" });
  }, [open, menuRef]);
}

/* Top-anchored label form (D7-B): the label lives above the box permanently;
   only when a label is explicitly given is the placeholder kept inside the box
   as a supplementary hint, avoiding duplication with the label text. */
export function NyroTextField({
  label,
  className,
  fullWidth = true,
  value,
  placeholder,
  numbersOnly = false,
  error,
  hint,
  help,
  required,
  onChange,
  ...rest
}: NyroTextFieldProps) {
  const inputId = useId();
  const displayLabel = label ?? placeholder ?? "";
  const boxPlaceholder = label != null ? placeholder : undefined;

  return (
    <div className={clsx("field", fullWidth && "full", className)}>
      {displayLabel && (
        <label className="field-label" htmlFor={inputId}>
          {displayLabel}
          {required && <span className="required" aria-hidden="true">*</span>}
          {help && <NyroHelpHint text={help} />}
        </label>
      )}
      <input
        {...rest}
        id={inputId}
        value={value}
        placeholder={boxPlaceholder}
        className={clsx("field-control", error && "is-error")}
        inputMode={numbersOnly ? "numeric" : rest.inputMode}
        pattern={numbersOnly ? "[0-9]*" : rest.pattern}
        onChange={(event) => {
          if (numbersOnly) {
            const sanitized = event.target.value.replace(/[^0-9]/g, "");
            if (sanitized !== event.target.value) {
              event.target.value = sanitized;
            }
          }
          onChange?.(event);
        }}
      />
      <FieldHint error={error} hint={hint} />
    </div>
  );
}

export function NyroTextareaField({
  label,
  className,
  fullWidth = true,
  value,
  placeholder,
  rows = 5,
  error,
  hint,
  help,
  required,
  ...rest
}: NyroTextareaFieldProps) {
  const inputId = useId();
  const displayLabel = label ?? placeholder ?? "";
  const boxPlaceholder = label != null ? placeholder : undefined;

  return (
    <div className={clsx("field", fullWidth && "full", className)}>
      {displayLabel && (
        <label className="field-label" htmlFor={inputId}>
          {displayLabel}
          {required && <span className="required" aria-hidden="true">*</span>}
          {help && <NyroHelpHint text={help} />}
        </label>
      )}
      <textarea
        {...rest}
        id={inputId}
        value={value}
        rows={rows}
        placeholder={boxPlaceholder}
        className={clsx("field-control", error && "is-error")}
      />
      <FieldHint error={error} hint={hint} />
    </div>
  );
}

export type NyroSearchSelectProps<T> = BaseFieldProps & {
  options: T[];
  value: T | null;
  onChange: (option: T | null) => void;
  getOptionLabel: (option: T) => string;
  isOptionEqualToValue?: (option: T, value: T) => boolean;
  placeholder?: string;
  noOptionsText?: string;
  renderOption?: (option: T) => ReactNode;
  searchable?: boolean;
  /** Placeholder text for the search box at the top of the searchable menu (baseline "Search models"). */
  searchPlaceholder?: string;
  /** Leading icon for the trigger (the funnel icon of the toolbar filter, baseline .toolbar-filter structure). */
  leadingIcon?: ReactNode;
  /** Class attached to .select-control (e.g. toolbar-filter inside the toolbar). */
  controlClassName?: string;
  /** Accessible name when there is no visible label (toolbar filter). */
  ariaLabel?: string;
  /** When the search has no match, submits the input text directly as a new option (e.g. a custom upstream model ID). */
  createOptionFromInput?: (input: string) => T;
};

/* Single-select: strictly follows the baseline structure — the trigger is a
   button.field-control (optional leading icon + span.selected-value +
   span.control-icon chevron); the select-menu hangs in place under
   .select-control, driven by the .open class in the baseline CSS. In the
   searchable variant the search box sits at the top of the menu
   (searchable-select-search, the baseline waf-gateway default-model picker);
   the trigger itself is a plain button, not an input. */
export function NyroSearchSelect<T>({
  options,
  value,
  onChange,
  getOptionLabel,
  isOptionEqualToValue,
  label,
  placeholder,
  noOptionsText,
  renderOption,
  searchable = true,
  searchPlaceholder,
  leadingIcon,
  className,
  fullWidth = true,
  error,
  hint,
  help,
  required,
  controlClassName,
  ariaLabel,
  createOptionFromInput,
}: NyroSearchSelectProps<T>) {
  const rootRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const inputId = useId();
  const menuId = `${inputId}-menu`;
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  // The keyboard highlight appears only after the user actually navigates with
  // the arrow keys: on open, activeIndex is merely preseeded on the currently
  // selected option (so Enter picks it directly) and does not render the
  // .active gray background — unselected options stay white; only the selected
  // option (.selected) is gray.
  const [navigated, setNavigated] = useState(false);
  const [query, setQuery] = useState("");

  useBaselineSelectOpen(rootRef, open, setOpen, menuRef);

  const findEquals = (a: T, b: T) =>
    isOptionEqualToValue ? isOptionEqualToValue(a, b) : a === b;

  const filtered = useMemo(() => {
    if (!searchable) return options;
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((option) => getOptionLabel(option).toLowerCase().includes(q));
  }, [options, query, getOptionLabel, searchable]);

  // Converge the keyboard highlight index as the list shrinks with the search.
  useEffect(() => {
    if (activeIndex >= filtered.length) {
      setActiveIndex(Math.max(0, filtered.length - 1));
    }
  }, [activeIndex, filtered.length]);

  function closeMenu(focusTrigger = false) {
    setOpen(false);
    if (focusTrigger) triggerRef.current?.focus({ preventScroll: true });
  }

  // The baseline clears the search term on open, returning to the full list, with the keyboard cursor landing on the currently selected option.
  function openMenu() {
    setQuery("");
    setNavigated(false);
    const idx = value == null
      ? -1
      : options.findIndex((option) => findEquals(option, value));
    setActiveIndex(idx >= 0 ? idx : 0);
    setOpen(true);
  }

  function selectOption(option: T) {
    onChange(option);
    closeMenu(true);
  }

  function handleKeyDown(event: ReactKeyboardEvent<HTMLElement>) {
    if (event.key === "Escape") {
      event.preventDefault();
      closeMenu(true);
      return;
    }
    if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      event.preventDefault();
      openMenu();
      return;
    }
    if (!open) return;
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setNavigated(true);
      setActiveIndex((idx) => Math.min(filtered.length - 1, idx + 1));
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setNavigated(true);
      setActiveIndex((idx) => Math.max(0, idx - 1));
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      const active = filtered[activeIndex];
      if (active) {
        selectOption(active);
        return;
      }
      const input = query.trim();
      if (createOptionFromInput && input) {
        const exact = filtered.find(
          (option) => getOptionLabel(option).toLowerCase() === input.toLowerCase(),
        );
        selectOption(exact ?? createOptionFromInput(input));
      }
    }
  }

  // Scroll the keyboard-highlighted option into view (needed by long lists inside a height-capped menu).
  useEffect(() => {
    if (!open) return;
    document.getElementById(`${menuId}-opt-${activeIndex}`)?.scrollIntoView({ block: "nearest" });
  }, [activeIndex, open, menuId]);

  const displayLabel = label ?? placeholder ?? "";

  return (
    <div ref={rootRef} className={clsx("field", fullWidth && "full", className)}>
      {displayLabel && (
        <span className="field-label" id={`${inputId}-label`}>
          {displayLabel}
          {required && <span className="required" aria-hidden="true">*</span>}
          {help && <NyroHelpHint text={help} />}
        </span>
      )}
      <div className={clsx("select-control", controlClassName, open && "open")}>
        <button
          type="button"
          ref={triggerRef}
          className={clsx("field-control", error && "is-error")}
          role="combobox"
          aria-expanded={open}
          aria-haspopup="listbox"
          aria-controls={open ? menuId : undefined}
          aria-activedescendant={open && !searchable && filtered[activeIndex] != null
            ? `${menuId}-opt-${activeIndex}`
            : undefined}
          aria-labelledby={displayLabel ? `${inputId}-label` : undefined}
          aria-label={displayLabel ? undefined : ariaLabel}
          onClick={() => (open ? closeMenu() : openMenu())}
          onKeyDown={handleKeyDown}
        >
          {leadingIcon}
          <span className={clsx("selected-value", value == null && "is-placeholder")}>
            {value != null ? getOptionLabel(value) : placeholder}
          </span>
          <span className="control-icon"><ChevronDown aria-hidden="true" /></span>
        </button>
        {open && (
          <div
            ref={menuRef}
            id={menuId}
            className={clsx("select-menu", filtered.length === 0 && "is-empty")}
            role="listbox"
            aria-label={ariaLabel ?? displayLabel}
          >
            {searchable && (
              <div className="searchable-select-search">
                <input
                  className="searchable-select-input"
                  value={query}
                  placeholder={searchPlaceholder ?? placeholder ?? ""}
                  aria-label={searchPlaceholder ?? ariaLabel ?? (displayLabel || "Search")}
                  aria-controls={menuId}
                  aria-autocomplete="list"
                  aria-activedescendant={filtered[activeIndex] != null
                    ? `${menuId}-opt-${activeIndex}`
                    : undefined}
                  autoFocus
                  onChange={(event) => {
                    setQuery(event.target.value);
                    setActiveIndex(0);
                  }}
                  onKeyDown={handleKeyDown}
                />
              </div>
            )}
            {(searchable || filtered.length === 0) && (
              <div className="searchable-select-empty">{noOptionsText ?? "No options"}</div>
            )}
            {filtered.map((option, index) => {
              const selected = value != null && findEquals(option, value);
              return (
                <button
                  key={`${getOptionLabel(option)}-${index}`}
                  type="button"
                  id={`${menuId}-opt-${index}`}
                  role="option"
                  aria-selected={selected}
                  className={clsx(
                    "select-option",
                    index === activeIndex && navigated && "active",
                    selected && "selected",
                  )}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => selectOption(option)}
                >
                  <span>{renderOption ? renderOption(option) : getOptionLabel(option)}</span>
                  {selected && <Check aria-hidden="true" />}
                </button>
              );
            })}
          </div>
        )}
      </div>
      <FieldHint error={error} hint={hint} />
    </div>
  );
}

export type NyroMultiCheckProps<T> = BaseFieldProps & {
  options: T[];
  selected: T[];
  onChange: (next: T[]) => void;
  getOptionValue: (option: T) => string;
  getOptionLabel: (option: T) => string;
  renderOption?: (option: T) => ReactNode;
  placeholder?: string;
  /** Accessible name when there is no visible label. */
  ariaLabel?: string;
};

/* Multi-select: the baseline custom-multi structure — the trigger is
   button.field-control.tags-control .multi-display (.tag chips + .tag-remove ×
   + chevron); the custom-multi-options menu hangs in place under .custom-multi;
   custom-multi-option carries a .multi-check checkbox, and clicking only
   toggles the selection and keeps the menu open (baseline
   toggleCustomMultiOption). */
export function NyroMultiCheck<T>({
  options,
  selected,
  onChange,
  getOptionValue,
  getOptionLabel,
  renderOption,
  label,
  placeholder,
  className,
  fullWidth = true,
  error,
  help,
  required,
  ariaLabel,
}: NyroMultiCheckProps<T>) {
  const rootRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const labelId = useId();
  const menuId = `${labelId}-menu`;
  const [open, setOpen] = useState(false);

  useBaselineSelectOpen(rootRef, open, setOpen, menuRef);

  const selectedValues = useMemo(
    () => new Set(selected.map(getOptionValue)),
    [selected, getOptionValue],
  );

  function toggle(option: T) {
    const value = getOptionValue(option);
    onChange(
      selectedValues.has(value)
        ? selected.filter((item) => getOptionValue(item) !== value)
        : [...selected, option],
    );
  }

  return (
    <div ref={rootRef} className={clsx("field", fullWidth && "full", className)}>
      {label && (
        <span className="field-label" id={labelId}>
          {label}
          {required && <span className="required" aria-hidden="true">*</span>}
          {help && <NyroHelpHint text={help} />}
        </span>
      )}
      <div className={clsx("custom-multi", open && "open")}>
        <button
          type="button"
          className={clsx("field-control tags-control multi-display", error && "is-error")}
          role="combobox"
          aria-expanded={open}
          aria-haspopup="listbox"
          aria-controls={open ? menuId : undefined}
          aria-labelledby={label ? labelId : undefined}
          aria-label={label ? undefined : ariaLabel}
          onClick={() => setOpen((current) => !current)}
          onKeyDown={(event) => {
            if (event.target !== event.currentTarget) return;
            if (event.key === "Escape") {
              event.preventDefault();
              setOpen(false);
            } else if (event.key === "Enter" || event.key === " " || event.key === "ArrowDown") {
              event.preventDefault();
              setOpen(true);
            }
          }}
        >
          {selected.length === 0 ? (
            <span className="selected-value">{placeholder}</span>
          ) : (
            selected.map((option) => (
              <span className="tag" key={getOptionValue(option)}>
                {renderOption ? renderOption(option) : getOptionLabel(option)}
                {/* The baseline uses span.tag-remove (a button cannot nest a button) */}
                <span
                  className="tag-remove"
                  role="button"
                  tabIndex={0}
                  aria-label={`Remove ${getOptionLabel(option)}`}
                  onClick={(event) => {
                    event.stopPropagation();
                    toggle(option);
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter" || event.key === " ") {
                      event.preventDefault();
                      event.stopPropagation();
                      toggle(option);
                    }
                  }}
                >
                  ×
                </span>
              </span>
            ))
          )}
          <span className="control-icon"><ChevronDown aria-hidden="true" /></span>
        </button>
        {open && (
          <div
            ref={menuRef}
            id={menuId}
            className={clsx("custom-multi-options", options.length === 0 && "is-empty")}
            role="listbox"
            aria-multiselectable="true"
          >
            {options.length === 0 && (
              <div className="searchable-select-empty">{placeholder}</div>
            )}
            {options.map((option) => {
              const value = getOptionValue(option);
              const checked = selectedValues.has(value);
              return (
                <button
                  key={value}
                  type="button"
                  role="option"
                  aria-selected={checked}
                  className={clsx("custom-multi-option", checked && "checked")}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => toggle(option)}
                >
                  <span className="multi-check" aria-hidden="true">
                    <Check />
                  </span>
                  <span>{renderOption ? renderOption(option) : getOptionLabel(option)}</span>
                </button>
              );
            })}
          </div>
        )}
      </div>
      <FieldHint error={error} />
    </div>
  );
}
