import { IS_TAURI } from "@/lib/backend";

export async function openExternalUrl(url: string) {
  if (IS_TAURI) {
    const { openUrl } = await import("@tauri-apps/plugin-opener");
    await openUrl(url);
    return;
  }

  window.open(url, "_blank", "noopener,noreferrer");
}
