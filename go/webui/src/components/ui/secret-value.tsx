import { useState } from "react";
import { Copy, Eye, EyeOff } from "lucide-react";

import { useLocale } from "@/lib/i18n";
import { localizedMessage } from "@/lib/messages";
import { showToast } from "@/lib/toast";

/* Read-only secret display: the baseline
   secret-control vocabulary — a password-style input + show/hide toggle
   (secret-toggle), plus inline copy; a successful copy is acknowledged via
   Toast (§9.5 ③). The api-keys page's one-time token display reuses the same
   component. */
export function SecretValue({ value, ariaLabel }: { value: string; ariaLabel: string }) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const [revealed, setRevealed] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      showToast(localizedMessage(isZh, "v2.api-keys.copied"));
    } catch {
      // Clipboard may be unavailable (insecure context); ignore silently.
    }
  }

  const toggleLabel = localizedMessage(isZh, revealed ? "v2.connect.hideKey" : "v2.connect.showKey");
  const copyLabel = localizedMessage(isZh, "common.copy");

  return (
    <span className="field-control secret-control">
      <input
        type={revealed ? "text" : "password"}
        value={value}
        readOnly
        autoComplete="off"
        spellCheck={false}
        aria-label={ariaLabel}
        onFocus={(event) => event.target.select()}
      />
      <button
        type="button"
        className="secret-toggle"
        aria-label={toggleLabel}
        title={toggleLabel}
        onClick={() => setRevealed((current) => !current)}
      >
        {revealed ? <EyeOff aria-hidden="true" /> : <Eye aria-hidden="true" />}
      </button>
      <button
        type="button"
        className="secret-toggle"
        aria-label={copyLabel}
        title={copyLabel}
        onClick={() => void copy()}
      >
        <Copy aria-hidden="true" />
      </button>
    </span>
  );
}
