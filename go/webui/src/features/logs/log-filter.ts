import type { LogQuery } from "@/lib/types";

export type LogStatusFilter = "all" | "ok" | "error";

export function logStatusFilterValue(filter: LogQuery): LogStatusFilter {
  if (filter.status_min === 200 && filter.status_max === 299) return "ok";
  if (filter.status_min === 400 && filter.status_max == null) return "error";
  return "all";
}

export function applyLogStatusFilter(filter: LogQuery, status: LogStatusFilter): LogQuery {
  if (status === "ok") return { ...filter, status_min: 200, status_max: 299 };
  if (status === "error") return { ...filter, status_min: 400, status_max: undefined };
  return { ...filter, status_min: undefined, status_max: undefined };
}
