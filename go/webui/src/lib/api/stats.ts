import type {
  ConsumerStats,
  RouteStats,
  StatsHourly,
  StatsOverview,
  UpstreamStats,
} from "@/lib/types";
import { get } from "./client";

function withHours(path: string, hours?: number) {
  return get(path, hours != null ? { hours } : undefined);
}

export const statsApi = {
  overview: (hours?: number) => withHours("/stats/overview", hours) as Promise<StatsOverview>,
  hourly: (hours?: number) => withHours("/stats/hourly", hours ?? 24) as Promise<StatsHourly[]>,
  byRoute: (hours?: number) => withHours("/stats/routes", hours) as Promise<RouteStats[]>,
  byUpstream: (hours?: number) => withHours("/stats/upstreams", hours) as Promise<UpstreamStats[]>,
  byConsumer: (hours?: number) =>
    withHours("/stats/consumers", hours) as Promise<ConsumerStats[]>,
};
