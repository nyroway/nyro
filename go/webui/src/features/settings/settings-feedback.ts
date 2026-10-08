import { localizeBackendErrorMessage } from "@/lib/backend-error";
import { localizedMessage, type MessageKey } from "@/lib/messages";
import { showToast } from "@/lib/toast";

/* 设置页统一保存反馈（改造方案 §9.1 ④ / §8.3）：
   错误 = 标题 + 后端消息合成一条 toast，成功 = 通用已保存。 */
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
