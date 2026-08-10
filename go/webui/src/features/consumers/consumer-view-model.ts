import type { Consumer } from "@/lib/types";

export type ConsumerFilters = {
  query: string;
  status: "all" | "enabled" | "disabled";
};

export function filterConsumers(consumers: readonly Consumer[], filters: ConsumerFilters): Consumer[] {
  const query = filters.query.trim().toLocaleLowerCase();

  return consumers.filter((consumer) => {
    if (filters.status === "enabled" && !consumer.enabled) return false;
    if (filters.status === "disabled" && consumer.enabled) return false;
    if (!query) return true;

    return [
      consumer.id,
      consumer.name,
      ...(consumer.routes ?? []),
      ...(consumer.protocols ?? []),
      ...(consumer.ip_allowlist ?? []),
      ...Object.values(consumer.metadata ?? {}),
    ].some((value) => value.toLocaleLowerCase().includes(query));
  });
}
