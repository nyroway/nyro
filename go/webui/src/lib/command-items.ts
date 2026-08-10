import type { Consumer, Route, Upstream } from "./types";

export type ResourceCommand = {
  id: string;
  kind: "provider" | "model" | "consumer";
  label: string;
  href: string;
  keywords: string[];
};

export function buildResourceCommands(
  upstreams: ReadonlyArray<Pick<Upstream, "id" | "name" | "provider" | "enabled">>,
  routes: ReadonlyArray<Pick<Route, "id" | "model" | "enabled" | "enable_auth">>,
  consumers: ReadonlyArray<Pick<Consumer, "id" | "name" | "enabled" | "keys">>,
): ResourceCommand[] {
  return [
    ...upstreams.map((upstream) => ({
      id: `provider:${upstream.id}`,
      kind: "provider" as const,
      label: upstream.name,
      href: `/providers?focus=${encodeURIComponent(upstream.id)}`,
      keywords: [upstream.name, upstream.provider, "provider", "upstream"].filter((value): value is string => Boolean(value)),
    })),
    ...routes.map((route) => ({
      id: `model:${route.id}`,
      kind: "model" as const,
      label: route.model,
      href: `/models?focus=${encodeURIComponent(route.id)}`,
      keywords: [route.model, "model", "route"],
    })),
    ...consumers.map((consumer) => ({
      id: `consumer:${consumer.id}`,
      kind: "consumer" as const,
      label: consumer.name,
      href: `/api-keys?focus=${encodeURIComponent(consumer.id)}`,
      keywords: [consumer.name, "consumer", "api key"],
    })),
  ];
}
