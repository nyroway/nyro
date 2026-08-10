type SettingsValue = string | number | boolean | null | undefined | number[];
type SettingsRecord = Record<string, SettingsValue>;

function normalize(value: SettingsValue): string | number | boolean | null | number[] {
  if (Array.isArray(value)) return [...value].sort((left, right) => left - right);
  if (typeof value === "string") return value.trim();
  return value ?? null;
}

export function isSettingsDirty(initial: SettingsRecord, current: SettingsRecord): boolean {
  const keys = new Set([...Object.keys(initial), ...Object.keys(current)]);
  return [...keys].some((key) => JSON.stringify(normalize(initial[key])) !== JSON.stringify(normalize(current[key])));
}
