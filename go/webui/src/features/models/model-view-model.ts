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

/* 上游多选驱动目标增删：勾选的提供商保留/追加目标卡，取消勾选的移除
   其全部目标（一个提供商可有多张不同模型的目标卡）。 */
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
