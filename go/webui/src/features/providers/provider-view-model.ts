import type { Upstream } from "@/lib/types";

export type ProviderFilters = {
  query: string;
  protocol: string;
  enabled: "all" | "enabled" | "disabled";
};

export function filterProviders(providers: readonly Upstream[], filters: ProviderFilters): Upstream[] {
  const query = filters.query.trim().toLocaleLowerCase();

  return providers.filter((provider) => {
    if (filters.protocol !== "all" && provider.protocol !== filters.protocol) return false;
    if (filters.enabled === "enabled" && !provider.enabled) return false;
    if (filters.enabled === "disabled" && provider.enabled) return false;
    if (!query) return true;

    return [
      provider.id,
      provider.name,
      provider.provider,
      provider.protocol,
      provider.base_url,
      provider.proxy_url,
      ...(provider.models ?? []),
    ].some((value) => value?.toLocaleLowerCase().includes(query));
  });
}
