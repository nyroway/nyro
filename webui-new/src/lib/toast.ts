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

export function showToast(text: string, tone: ToastTone = "success", duration = 4000) {
  if (dismissTimer) clearTimeout(dismissTimer);
  publish({ id: ++sequence, tone, text });
  dismissTimer = setTimeout(() => {
    publish(null);
    dismissTimer = null;
  }, duration);
}
