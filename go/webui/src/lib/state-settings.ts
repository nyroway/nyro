export type StateSettingsDraft = { type: "memory" | "redis"; url: string };

export function stateSettingsFromValues(type: string | null, url: string | null): StateSettingsDraft {
  return {
    type: type?.trim() === "redis" ? "redis" : "memory",
    url: url?.trim() ?? "",
  };
}

export function validateStateSettings(value: StateSettingsDraft): "invalid" | null {
  if (value.type === "memory") return null;

  const rawURL = value.url.trim();
  try {
    const parsed = new URL(rawURL);
    return parsed.protocol === "redis:" && Boolean(parsed.hostname) && !parsed.hash && !rawURL.includes("#")
      ? null
      : "invalid";
  } catch {
    return "invalid";
  }
}

export function stateSettingsPayload(value: StateSettingsDraft): Record<string, string> {
  return {
    "state.type": value.type,
    "state.url": value.type === "redis" ? value.url.trim() : "",
  };
}

export function sameStateSettings(left: StateSettingsDraft, right: StateSettingsDraft): boolean {
  const a = stateSettingsPayload(left);
  const b = stateSettingsPayload(right);
  return a["state.type"] === b["state.type"] && a["state.url"] === b["state.url"];
}
