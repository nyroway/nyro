/* eslint-disable react-hooks/set-state-in-effect */

import { useEffect, useState, useSyncExternalStore } from "react";
import clsx from "clsx";
import { CircleAlert, CircleCheck } from "lucide-react";

import { readToast, subscribeToToast, type ToastMessage } from "@/lib/toast";

const TONE_ICONS = {
  success: CircleCheck,
  error: CircleAlert,
} as const;

/* Singleton toast host: subscribes to the module state in lib/toast and drives
   the enter/exit transition with the baseline two-state .toast/.show toggle
   (enter waits one frame before adding .show; exit removes .show first, then
   detaches after a delay). */
export function ToastHost() {
  const toast = useSyncExternalStore(subscribeToToast, readToast, readToast);
  const [rendered, setRendered] = useState<ToastMessage | null>(null);
  const [shown, setShown] = useState(false);

  useEffect(() => {
    if (toast) {
      setRendered(toast);
      setShown(false);
      const frame = requestAnimationFrame(() => setShown(true));
      return () => cancelAnimationFrame(frame);
    }
    setShown(false);
    const timer = setTimeout(() => setRendered(null), 260);
    return () => clearTimeout(timer);
  }, [toast]);

  if (!rendered) return null;

  const Icon = TONE_ICONS[rendered.tone];
  return (
    <div
      className={clsx("toast", rendered.tone === "error" && "error", shown && "show")}
      role="status"
    >
      <span className="toast-icon">
        <Icon aria-hidden="true" />
      </span>
      <span>{rendered.text}</span>
    </div>
  );
}
