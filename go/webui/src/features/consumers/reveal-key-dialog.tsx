import type { ReactNode } from "react";

import { useLocale } from "@/lib/i18n";
import { localizedMessage } from "@/lib/messages";
import { SecretValue } from "@/components/ui/secret-value";
import { Notice } from "@/components/v2/notice";
import { ResourceEditorDialog } from "@/components/v2/resource-editor-dialog";
import type { RevealedKey } from "./consumer-form";

/* §9.4 注意（安全）：创建成功返回的 raw token 只在创建响应里出现一次。
   对话框 body 单独导出，使其可脱离 Radix Portal 做静态渲染测试。 */

export function RevealKeyDialogBody({ revealed }: { revealed: RevealedKey }) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  return (
    <div className="reveal-key">
      <Notice tone="warning">
        {localizedMessage(isZh, "consumers.revealOnce", { name: revealed.name })}
      </Notice>
      <SecretValue value={revealed.token} ariaLabel={localizedMessage(isZh, "v2.connect.apiKey")} />
    </div>
  );
}

/**
 * 一次性令牌展示（§9.4）：modal 内 secret-control + 复制按钮 + “仅此一次”
 * alert-warning 警示。`revealed` 为 null 时对话框关闭——调用方必须在关闭时
 * 清空该状态，保证“关闭即不可再取”。
 */
export function RevealKeyDialog({
  revealed,
  onClose,
}: {
  revealed: RevealedKey | null;
  onClose: () => void;
}) {
  const { locale } = useLocale();
  const isZh = locale === "zh-CN";
  const footer: ReactNode = (
    <button type="button" className="button button-primary" onClick={onClose}>
      {localizedMessage(isZh, "v2.providers.close")}
    </button>
  );

  return (
    <ResourceEditorDialog
      open={Boolean(revealed)}
      title={localizedMessage(isZh, "v2.api-keys.keyGenerated")}
      description={revealed?.name}
      onClose={onClose}
      footer={footer}
    >
      {revealed ? <RevealKeyDialogBody revealed={revealed} /> : null}
    </ResourceEditorDialog>
  );
}
