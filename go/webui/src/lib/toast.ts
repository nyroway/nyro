// 轻量 toast 状态（go-webui-改造方案.md §7.4）：模块级单例，
// 最新一条直接顶替上一条（基线 .toast 就是右下角单元素形态），
// 到期由 ToastHost（components/ui/toast.tsx）播放退场动画后摘除。

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

/** 显示一条 toast；错误类消息传更长的停留时间，给阅读留余量。 */
export function showToast(text: string, tone: ToastTone = "success", duration = 4000) {
  if (dismissTimer) clearTimeout(dismissTimer);
  publish({ id: ++sequence, tone, text });
  dismissTimer = setTimeout(() => {
    publish(null);
    dismissTimer = null;
  }, duration);
}
