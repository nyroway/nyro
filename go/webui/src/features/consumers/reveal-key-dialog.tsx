import type { ReactNode } from "react";

import { useLocale } from "@/lib/i18n";
import { localizedMessage } from "@/lib/messages";
import { SecretValue } from "@/components/ui/secret-value";
import { Notice } from "@/components/v2/notice";
import { ResourceEditorDialog } from "@/components/v2/resource-editor-dialog";
import type { RevealedKey } from "./consumer-form";

/* §9.4 note (security): the raw token returned on successful creation appears exactly once, in the creation response.
   The dialog body is exported separately so it can be statically rendered for tests without the Radix Portal. */

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
 * One-time token reveal (§9.4): secret-control + copy button + "shown only once"
 * alert-warning notice inside the modal. When `revealed` is null the dialog is closed — the caller must
 * clear that state on close, guaranteeing "once closed, it can never be fetched again".
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
