import type { MessageKey } from "./messages";

export type SettingsSectionID = "forwarding" | "logs" | "metrics" | "traces" | "public" | "retention";

export const SETTINGS_SECTIONS: ReadonlyArray<{
  id: SettingsSectionID;
  group: "data-plane" | "control-plane";
  label: MessageKey;
}> = [
  { id: "forwarding", group: "data-plane", label: "settings.forwarding" },
  { id: "logs", group: "data-plane", label: "settings.logsExport" },
  { id: "metrics", group: "data-plane", label: "settings.metricsExport" },
  { id: "traces", group: "data-plane", label: "settings.tracesExport" },
  { id: "public", group: "control-plane", label: "settings.publicGateway" },
  { id: "retention", group: "control-plane", label: "settings.retention" },
];
