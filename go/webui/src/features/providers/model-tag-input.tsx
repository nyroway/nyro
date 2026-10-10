import { type ClipboardEvent, type KeyboardEvent, useId, useRef, useState } from "react";
import { X } from "lucide-react";

import { appendModelTags, parseModelTags, removeModelTagAt } from "@/features/providers/model-tags";

type ModelTagInputProps = {
  value: readonly string[];
  onChange: (next: string[]) => void;
  inputLabel: string;
  listLabel: string;
  placeholder: string;
  helpText: string;
  removeLabel: (model: string) => string;
};

export function ModelTagInput({
  value,
  onChange,
  inputLabel,
  listLabel,
  placeholder,
  helpText,
  removeLabel,
}: ModelTagInputProps) {
  const [draft, setDraft] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const helpId = useId();

  function focusInput() {
    inputRef.current?.focus();
  }

  function commit(tags: readonly string[]) {
    const next = appendModelTags(value, tags);
    if (next.length !== value.length || next.some((model, index) => model !== value[index])) {
      onChange(next);
    }
    setDraft("");
  }

  function commitDraft() {
    commit(parseModelTags(draft));
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.nativeEvent.isComposing) return;

    if (event.key === "Enter" || event.key === ",") {
      event.preventDefault();
      commitDraft();
      return;
    }

    if (event.key === "Backspace" && !draft && value.length > 0) {
      event.preventDefault();
      onChange(removeModelTagAt(value, value.length - 1));
    }
  }

  function handlePaste(event: ClipboardEvent<HTMLInputElement>) {
    const pasted = event.clipboardData.getData("text");
    if (!pasted) return;

    event.preventDefault();
    commit([...parseModelTags(draft), ...parseModelTags(pasted)]);
  }

  return (
    <>
      {/* §9.2 ④: baseline tags-control/tag-input vocabulary (keyboard/paste/IME logic kept as-is). */}
      <div className="field-control tags-control model-tag-input" onClick={focusInput}>
        <ul className="model-tag-list" aria-label={listLabel}>
          {value.map((model, index) => (
            <li key={model} className="tag">
              <code>{model}</code>
              <button
                type="button"
                className="tag-remove"
                aria-label={removeLabel(model)}
                onClick={(event) => {
                  event.stopPropagation();
                  onChange(removeModelTagAt(value, index));
                  focusInput();
                }}
              >
                <X aria-hidden="true" />
              </button>
            </li>
          ))}
        </ul>
        <input
          ref={inputRef}
          type="text"
          className="tag-input"
          aria-label={inputLabel}
          aria-describedby={helpId}
          placeholder={value.length === 0 ? placeholder : undefined}
          value={draft}
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          onChange={(event) => setDraft(event.target.value)}
          onKeyDown={handleKeyDown}
          onPaste={handlePaste}
          onBlur={commitDraft}
        />
      </div>
      <p id={helpId} className="field-hint">{helpText}</p>
    </>
  );
}
