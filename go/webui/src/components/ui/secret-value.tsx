import { useState } from "react";
import { Copy, Eye, EyeOff } from "lucide-react";

import { useLocale } from "@/lib/i18n";
import { localizedMessage } from "@/lib/messages";
import { showToast } from "@/lib/toast";

/* 只读密文展示（go-webui-改造方案.md §9.5 ②）：基线 secret-control 词汇
   ——密码形态输入框 + 显隐切换（secret-toggle），附内联复制；复制成功经
   Toast 反馈（§9.5 ③）。api-keys 页的一次性令牌展示复用同一组件。 */
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
