import { useEffect, useId, useMemo, useRef, useState } from "react";
import type { KeyboardEvent, ReactNode } from "react";
import clsx from "clsx";

type InputValue = string | number | readonly string[] | undefined;

function hasValue(value: InputValue): boolean {
  if (value === undefined) return false;
  if (Array.isArray(value)) return value.length > 0;
  return String(value).length > 0;
}

type BaseFieldProps = {
  label?: string;
  className?: string;
  fullWidth?: boolean;
};

export type NyroTextFieldProps = BaseFieldProps &
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "size"> & {
    numbersOnly?: boolean;
  };

export type NyroTextareaFieldProps = BaseFieldProps &
  React.TextareaHTMLAttributes<HTMLTextAreaElement>;

export function NyroTextField({
  label,
  className,
  fullWidth = true,
  value,
  placeholder,
  numbersOnly = false,
  onChange,
  onFocus,
  onBlur,
  ...rest
}: NyroTextFieldProps) {
  const inputId = useId();
  const [focused, setFocused] = useState(false);
  const showFloating = focused || hasValue(value);
  const displayLabel = label ?? placeholder ?? "";

  return (
    <div
      className={clsx(
        "nyro-field",
        fullWidth && "nyro-field-full",
        focused && "nyro-field-focused",
        showFloating && "nyro-field-has-value",
        rest.disabled && "nyro-field-disabled",
        className,
      )}
    >
      <fieldset className="nyro-field-outline" aria-hidden>
        <legend className="nyro-field-legend">
          <span>{displayLabel || " "}</span>
        </legend>
      </fieldset>
      {displayLabel && (
        <label className="nyro-field-label" htmlFor={inputId}>
          {displayLabel}
        </label>
      )}
      <input
        {...rest}
        id={inputId}
        value={value}
        placeholder=""
        className="nyro-field-input"
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
        onFocus={(event) => {
          setFocused(true);
          onFocus?.(event);
        }}
        onBlur={(event) => {
          setFocused(false);
          onBlur?.(event);
        }}
      />
    </div>
  );
}

export function NyroTextareaField({
  label,
  className,
  fullWidth = true,
  value,
  placeholder,
  onFocus,
  onBlur,
  rows = 5,
  ...rest
}: NyroTextareaFieldProps) {
  const inputId = useId();
  const [focused, setFocused] = useState(false);
  const showFloating = focused || hasValue(value);
  const displayLabel = label ?? placeholder ?? "";

  return (
    <div
      className={clsx(
        "nyro-field nyro-field-textarea",
        fullWidth && "nyro-field-full",
        focused && "nyro-field-focused",
        showFloating && "nyro-field-has-value",
        rest.disabled && "nyro-field-disabled",
        className,
      )}
    >
      <fieldset className="nyro-field-outline" aria-hidden>
        <legend className="nyro-field-legend">
          <span>{displayLabel || " "}</span>
        </legend>
      </fieldset>
      {displayLabel && (
        <label className="nyro-field-label" htmlFor={inputId}>
          {displayLabel}
        </label>
      )}
      <textarea
        {...rest}
        id={inputId}
        value={value}
        rows={rows}
        placeholder=""
        className="nyro-field-input nyro-field-textarea-input"
        onFocus={(event) => {
          setFocused(true);
          onFocus?.(event);
        }}
        onBlur={(event) => {
          setFocused(false);
          onBlur?.(event);
        }}
      />
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
  allowClear?: boolean;
  searchable?: boolean;
};

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
  allowClear = false,
  searchable = true,
  className,
  fullWidth = true,
}: NyroSearchSelectProps<T>) {
  const rootRef = useRef<HTMLDivElement>(null);
  const [focused, setFocused] = useState(false);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(0);
  const [query, setQuery] = useState("");

  const findEquals = (a: T, b: T) =>
    isOptionEqualToValue ? isOptionEqualToValue(a, b) : a === b;

  const filtered = useMemo(() => {
    if (!searchable) return options;
    const q = query.trim().toLowerCase();
    if (!q) return options;
    return options.filter((option) => getOptionLabel(option).toLowerCase().includes(q));
  }, [options, query, getOptionLabel, searchable]);

  useEffect(() => {
    if (!focused) {
      setQuery(value ? getOptionLabel(value) : "");
      setActiveIndex(0);
    }
  }, [focused, value, getOptionLabel]);

  useEffect(() => {
    function onClickOutside(event: MouseEvent) {
      if (!rootRef.current) return;
      if (!rootRef.current.contains(event.target as Node)) {
        setOpen(false);
        setFocused(false);
      }
    }
    window.addEventListener("mousedown", onClickOutside);
    return () => window.removeEventListener("mousedown", onClickOutside);
  }, []);

  const showFloating = focused || !!query || !!value;
  const displayLabel = label ?? placeholder ?? "";

  function selectOption(option: T) {
    onChange(option);
    setQuery(getOptionLabel(option));
    setOpen(false);
    setFocused(false);
  }

  function onKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (!open && (event.key === "ArrowDown" || event.key === "ArrowUp")) {
      setOpen(true);
      return;
    }
    if (event.key === "ArrowDown") {
      event.preventDefault();
      setActiveIndex((idx) => Math.min(filtered.length - 1, idx + 1));
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      setActiveIndex((idx) => Math.max(0, idx - 1));
      return;
    }
    if (event.key === "Enter" && open && filtered[activeIndex]) {
      event.preventDefault();
      selectOption(filtered[activeIndex]);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      setOpen(false);
      setFocused(false);
      return;
    }
  }

  useEffect(() => {
    if (activeIndex >= filtered.length) {
      setActiveIndex(Math.max(0, filtered.length - 1));
    }
  }, [activeIndex, filtered.length]);

  return (
    <div
      ref={rootRef}
      className={clsx(
        "nyro-field nyro-search",
        allowClear && value && "nyro-search-has-clear",
        fullWidth && "nyro-field-full",
        focused && "nyro-field-focused",
        showFloating && "nyro-field-has-value",
        className,
      )}
    >
      <fieldset className="nyro-field-outline" aria-hidden>
        <legend className="nyro-field-legend">
          <span>{displayLabel || " "}</span>
        </legend>
      </fieldset>
      {displayLabel && <span className="nyro-field-label">{displayLabel}</span>}
      <input
        value={query}
        placeholder=""
        className="nyro-field-input nyro-search-input"
        readOnly={!searchable}
        onFocus={() => {
          setFocused(true);
          setOpen(true);
        }}
        onClick={() => setOpen(true)}
        onChange={(event) => {
          if (!searchable) return;
          setQuery(event.target.value);
          setOpen(true);
          setActiveIndex(0);
        }}
        onKeyDown={onKeyDown}
      />

      {allowClear && value && (
        <button
          type="button"
          aria-label="Clear"
          className="nyro-search-clear"
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => {
            onChange(null);
            setQuery("");
            setOpen(false);
          }}
        >
          x
        </button>
      )}

      {open && (
        <div className="nyro-search-panel">
          {filtered.length === 0 ? (
            <div className="nyro-search-empty">{noOptionsText ?? "No options"}</div>
          ) : (
            filtered.map((option, index) => {
              const selected = value ? findEquals(option, value) : false;
              return (
                <button
                  key={`${getOptionLabel(option)}-${index}`}
                  type="button"
                  className={clsx(
                    "nyro-search-option",
                    index === activeIndex && "nyro-search-option-active",
                    selected && "nyro-search-option-selected",
                  )}
                  onMouseDown={(event) => event.preventDefault()}
                  onClick={() => selectOption(option)}
                >
                  {renderOption ? renderOption(option) : getOptionLabel(option)}
                </button>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
