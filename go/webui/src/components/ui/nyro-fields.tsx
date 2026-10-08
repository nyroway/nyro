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
  /** 校验错误：控件红框（.is-error）+ field-hint error。 */
  error?: string;
  /** 常规辅助说明（field-hint）。 */
  hint?: string;
  /** field-label 旁 "?" 帮助按钮的提示文案。 */
  help?: string;
  /** 必填星标（.required，纯视觉，不设原生 required 以免触发浏览器校验）。 */
  required?: boolean;
};

export type NyroTextFieldProps = BaseFieldProps &
  Omit<React.InputHTMLAttributes<HTMLInputElement>, "size"> & {
    numbersOnly?: boolean;
  };

export type NyroTextareaFieldProps = BaseFieldProps &
  React.TextareaHTMLAttributes<HTMLTextAreaElement>;

/* field-label 内的 "?" 帮助按钮：基线 .help 圆形问号 + 悬停提示
   （nyro-app.css 的 .help-tip，配色同基线 hover-tip）。
   提示层悬停时切 fixed 定位并按视口钳制——基线 hover-tip 就是 body 级
   固定层（.drawer-body/.card 的 overflow 会裁掉贴边的绝对定位弹层，
   fixed 脱离裁剪祖先）；.drawer 打开态带 transform 会改写 fixed 的
   包含块，用“归零读基线再补偿”两遍法保证任何包含块下都落在视口目标。
   落点几何是纯函数 helpTipLayout（lib/help-tip-layout，可单测）。 */
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
    if (!base.width || !base.height) return; // 未显示（如非 focus-visible 聚焦）无需定位
    const { left, top } = helpTipLayout(
      button.getBoundingClientRect(),
      { w: base.width, h: base.height },
      { w: window.innerWidth, h: window.innerHeight },
    );
    // base 是“归零定位”在视口里的实际落点：目标减基线即得任意包含块下的补偿
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

/* ── 下拉家族的基线生命周期 ─────────────────────────────────────────
   基线 toggleSelect/toggleCustomMulti 的“开我关它”：同一时刻全页只允许
   一个下拉打开。打开的组件把自身关闭函数登记进模块级注册表，新的打开
   动作先关闭其它实例；关闭/卸载时注销。 */
const openSelectClosers = new Set<() => void>();

function claimSelectMenu(close: () => void) {
  for (const other of openSelectClosers) other();
  openSelectClosers.add(close);
}

function releaseSelectMenu(close: () => void) {
  openSelectClosers.delete(close);
}

/* 下拉开关公共行为（基线语义）：
   - 菜单原位渲染为 .select-control/.custom-multi 的子元素（无 body 门户、
     无 fixed/z-index），打开态只是一个 .open 类，由真源 CSS 的
     .select-control.open .select-menu / .custom-multi.open .custom-multi-options
     接管显示——菜单因此天然处于抽屉/弹窗的层叠上下文内，不会被遮挡；
   - 文档级 mousedown 落在根节点之外即关闭（基线 document click 监听），
     根节点同时覆盖触发器与原位菜单；
   - 打开后把菜单滚入最近滚动祖先的可视区（抽屉底部表单需要）。 */
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

/* 置顶标签形态（D7-B）：标签常驻框外上方；只有显式给出 label 时才在
   框内保留 placeholder 作为补充提示，避免与置顶标签文字重复。 */
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
  /** searchable 菜单顶部检索框的占位文案（基线 “检索模型”）。 */
  searchPlaceholder?: string;
  /** 触发器前置图标（工具条筛选器的漏斗图标，基线 .toolbar-filter 结构）。 */
  leadingIcon?: ReactNode;
  /** 附加到 .select-control 的类（如工具条里的 toolbar-filter）。 */
  controlClassName?: string;
  /** 无可见标签时的可访问名称（工具条筛选器）。 */
  ariaLabel?: string;
  /** 搜索无匹配时把输入文本直接作为新选项提交（如自定义上游模型 ID）。 */
  createOptionFromInput?: (input: string) => T;
};

/* 单选下拉：严格按基线结构——触发器是 button.field-control（可选前置图标 +
   span.selected-value + span.control-icon 折角），菜单 select-menu 原位挂在
   .select-control 下，由 .open 类驱动真源 CSS。searchable 变体的检索框在
   菜单顶部（searchable-select-search，基线 waf-gateway 默认模型选择），
   触发器本身是纯按钮而非输入框。 */
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
  // 键盘高亮仅在用户真正用方向键导航后出现：打开菜单时 activeIndex 只是
  // 预置在当前选中项上（供回车直选），不渲染 .active 灰底——未选中项
  // 保持白底，只有选中项（.selected）才是灰底。
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

  // 列表随检索收缩时收敛键盘高亮下标。
  useEffect(() => {
    if (activeIndex >= filtered.length) {
      setActiveIndex(Math.max(0, filtered.length - 1));
    }
  }, [activeIndex, filtered.length]);

  function closeMenu(focusTrigger = false) {
    setOpen(false);
    if (focusTrigger) triggerRef.current?.focus({ preventScroll: true });
  }

  // 基线在打开时清空检索词回到完整列表，键盘光标落在当前选中项上。
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

  // 键盘高亮项滚入可视区（长列表在限高菜单内需要）。
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
  /** 无可见标签时的可访问名称。 */
  ariaLabel?: string;
};

/* 多选：基线 custom-multi 结构——触发器是 button.field-control.tags-control
   .multi-display（.tag 胶囊 + .tag-remove × + 折角），菜单 custom-multi-options
   原位挂在 .custom-multi 下；custom-multi-option 带 .multi-check 复选方块，
   点击只切换选中并保持菜单打开（基线 toggleCustomMultiOption）。 */
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
                {/* 基线用 span.tag-remove（button 内不能嵌 button） */}
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
