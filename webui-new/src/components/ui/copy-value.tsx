import { Copy } from "lucide-react";

import { copyableTipProps } from "@/lib/copyable";
import { localizedMessage } from "@/lib/messages";
import { showToast } from "@/lib/toast";

export async function copyText(value: string, isZh: boolean) {
  await navigator.clipboard.writeText(value);
  showToast(localizedMessage(isZh, "common.copied"));
}

export function CopyValue({ value, isZh, display }: { value: string; isZh: boolean; display?: string }) {
  if (!value) return <>—</>;
  const shown = display ?? value;
  return (
    <span className="copy-value">
      <code {...(display ? {} : copyableTipProps(value))}>{shown}</code>
      <button
        type="button"
        aria-label={localizedMessage(isZh, "common.copy")}
        onClick={() => void copyText(value, isZh)}
      >
        <Copy />
      </button>
    </span>
  );
}
