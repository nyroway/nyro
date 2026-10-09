import { get, put } from "./client";

// Settings are scalar key/value pairs. State-family keys must be committed
// together through setBulk — see lib/state-settings.ts and 改造方案 §8.4.
export const settingsApi = {
  get: (key: string) =>
    get<{ value?: string | null }>(`/settings/${key}`).then((value) => value.value ?? null),
  set: (key: string, value: string) =>
    put<{ value?: string | null }>(`/settings/${key}`, { value }).then(
      (value) => value.value ?? null,
    ),
  setBulk: (values: Record<string, string>) =>
    put<{ values?: Record<string, string> }>("/settings", { values }).then(
      (value) => value.values ?? {},
    ),
};
