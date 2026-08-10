import type { Route } from "@/lib/types";

export type ModelFilters = {
  query: string;
  status: "all" | "enabled" | "disabled";
};

export function filterRoutes(routes: readonly Route[], filters: ModelFilters): Route[] {
  const query = filters.query.trim().toLocaleLowerCase();

  return routes.filter((route) => {
    if (filters.status === "enabled" && !route.enabled) return false;
    if (filters.status === "disabled" && route.enabled) return false;
    if (!query) return true;

    return [
      route.id,
      route.model,
      route.balance,
      ...(route.upstreams ?? []).flatMap((target) => [target.upstream_id, target.model]),
    ].some((value) => value.toLocaleLowerCase().includes(query));
  });
}
