import type { LogPage, LogQuery, RequestLog } from "@/lib/types";
import { del, get, type QueryParams } from "./client";

export const logsApi = {
  query: (query: LogQuery, signal?: AbortSignal) => {
    // Map explicitly (interfaces carry no index signature) and skip empty
    // filters, matching the legacy query_logs param handling.
    const params: QueryParams = {};
    for (const [key, value] of Object.entries(query)) {
      if (value != null) params[key] = value;
    }
    return get<LogPage>("/logs", params, signal);
  },
  get: (id: string) => get<RequestLog>(`/logs/${id}`),
  clear: () => del<{ cleared: number }>("/logs"),
};
