import { useEffect } from "react";

import { localizedMessage } from "@/lib/messages";
import { showToast } from "@/lib/toast";

const COPY_ICON = `<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><rect x="9" y="9" width="13" height="13" rx="2"/><path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1"/></svg>`;

function isZhLocale() {
  return document.documentElement.lang === "zh-CN";
}

/** Rounded instant tip. Icons use data-tip. Identity values can add data-tip-copy to copy the shown text. */
export function HoverTipHost() {
  useEffect(() => {
    const tip = document.getElementById("hoverTip");
    if (!tip) return;

    let hideTimer: number | null = null;
    const cancelHide = () => {
      if (hideTimer != null) {
        window.clearTimeout(hideTimer);
        hideTimer = null;
      }
    };
    const hideNow = () => {
      cancelHide();
      tip.hidden = true;
    };
    const scheduleHide = (delay = 200) => {
      cancelHide();
      hideTimer = window.setTimeout(() => {
        tip.hidden = true;
        hideTimer = null;
      }, delay);
    };

    const render = (button: HTMLElement) => {
      const text = button.dataset.tip || "";
      const copyValue = button.dataset.tipCopy || "";
      tip.replaceChildren();
      const label = document.createElement("span");
      label.className = "hover-tip-text";
      label.textContent = text;
      tip.appendChild(label);
      if (copyValue) {
        tip.classList.add("is-copyable");
        tip.dataset.copyValue = copyValue;
        const copyButton = document.createElement("button");
        copyButton.type = "button";
        copyButton.className = "hover-tip-copy";
        copyButton.setAttribute("aria-label", localizedMessage(isZhLocale(), "common.copy"));
        copyButton.innerHTML = COPY_ICON;
        tip.appendChild(copyButton);
      } else {
        tip.classList.remove("is-copyable");
        delete tip.dataset.copyValue;
      }
    };

    const place = (button: HTMLElement) => {
      render(button);
      const box = button.getBoundingClientRect();
      tip.style.left = `${box.left + box.width / 2}px`;
      tip.style.top = `${box.top}px`;
      tip.hidden = !button.dataset.tip;
      if (tip.hidden) return;
      const rect = tip.getBoundingClientRect();
      const margin = 8;
      if (rect.left < margin) tip.style.left = `${margin + rect.width / 2}px`;
      else if (rect.right > window.innerWidth - margin) {
        tip.style.left = `${window.innerWidth - margin - rect.width / 2}px`;
      }
    };

    const onOver = (event: MouseEvent) => {
      const target = event.target as Element | null;
      if (target?.closest?.("#hoverTip")) {
        cancelHide();
        return;
      }
      const button = target?.closest?.("[data-tip]");
      if (!(button instanceof HTMLElement) || !button.dataset.tip) {
        if (tip.classList.contains("is-copyable") && !tip.hidden) scheduleHide(200);
        else hideNow();
        return;
      }
      cancelHide();
      place(button);
    };
    const onOut = (event: MouseEvent) => {
      const next = event.relatedTarget as Element | null;
      if (next?.closest?.("#hoverTip") || next?.closest?.("[data-tip]")) return;
      if (tip.classList.contains("is-copyable") && !tip.hidden) scheduleHide(200);
      else hideNow();
    };
    const onCopy = (event: MouseEvent) => {
      const copyButton = (event.target as Element | null)?.closest?.(".hover-tip-copy");
      if (!copyButton) return;
      event.preventDefault();
      event.stopPropagation();
      const value = tip.dataset.copyValue;
      if (!value) return;
      void navigator.clipboard.writeText(value).then(() => {
        showToast(localizedMessage(isZhLocale(), "common.copied"));
        scheduleHide(400);
      });
    };

    document.addEventListener("mouseover", onOver);
    document.addEventListener("mouseout", onOut);
    tip.addEventListener("click", onCopy);
    return () => {
      cancelHide();
      document.removeEventListener("mouseover", onOver);
      document.removeEventListener("mouseout", onOut);
      tip.removeEventListener("click", onCopy);
    };
  }, []);

  return <div className="hover-tip" id="hoverTip" hidden />;
}
