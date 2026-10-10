// Lightweight toast state: a module-level singleton,
// where the newest message directly replaces the previous one (the baseline .toast is a single bottom-right element),
// and on expiry ToastHost (components/ui/toast.tsx) plays the exit animation and then removes it.

export type ToastTone = "success" | "error";

export interface ToastMessage {
  id: number;
  tone: ToastTone;
  text: string;
}

let sequence = 0;
let current: ToastMessage | null = null;
let dismissTimer: ReturnType<typeof setTimeout> | null = null;
const listeners = new Set<() => void>();

function publish(next: ToastMessage | null) {
  current = next;
  for (const listener of listeners) listener();
}

export function subscribeToToast(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

export function readToast(): ToastMessage | null {
  return current;
}

/** Show a toast; error-tone messages pass a longer dwell time, leaving headroom for reading. */
export function showToast(text: string, tone: ToastTone = "success", duration = 4000) {
  if (dismissTimer) clearTimeout(dismissTimer);
  publish({ id: ++sequence, tone, text });
  dismissTimer = setTimeout(() => {
    publish(null);
    dismissTimer = null;
  }, duration);
}
