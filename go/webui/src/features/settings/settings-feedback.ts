import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { localizedMessage, type MessageKey } from "@/lib/messages";
import { showToast } from "@/lib/toast";

/* Unified save feedback for the settings pages (redesign plan §9.1 ④ / §8.3):
   error = title + backend message combined into a single toast; success = the generic "Saved" message. */
export function reportSettingsError(
  isZh: boolean,
  titleKey: MessageKey,
  error: unknown,
  params?: Record<string, string | number>,
): void {
  showToast(
    `${localizedMessage(isZh, titleKey, params)} — ${localizeBackendErrorMessage(error, isZh)}`,
    "error",
    6000,
  );
}

export function reportSettingsSaved(isZh: boolean): void {
  showToast(localizedMessage(isZh, "common.saved"));
}
