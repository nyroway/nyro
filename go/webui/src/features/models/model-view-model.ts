import type { Route, Upstream } from "@/lib/types";

export type ModelFilters = {
  query: string;
  status: "all" | "enabled" | "disabled";
};

export type RouteTargetDraft = {
  id?: string;
  upstream_id: string;
  model: string;
  weight: number;
  priority: number;
  enabled: boolean;
};

/* Upstream multi-select drives target add/remove: checked providers keep/append target cards, while unchecked ones
   have all of their targets removed (one provider can have multiple target cards with different models). */
export function toggleTargetsProviders(
  targets: readonly RouteTargetDraft[],
  next: readonly Upstream[],
): RouteTargetDraft[] {
  const nextIds = new Set(next.map((provider) => provider.id));
  const kept = targets.filter((target) => nextIds.has(target.upstream_id));
  const added = next
    .filter((provider) => !targets.some((target) => target.upstream_id === provider.id))
    .map((provider) => ({ upstream_id: provider.id, model: "", weight: 0, priority: 1, enabled: true }));
  return [...kept, ...added];
}

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
