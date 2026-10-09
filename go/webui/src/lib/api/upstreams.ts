import type {
  CreateUpstream,
  ProviderHealthEvent,
  ProviderPresetDTO,
  RouteImportEvent,
  RouteImportPreview,
  UpdateUpstream,
  Upstream,
} from "@/lib/types";
import {
  decodeProviderHealthSSEFrame,
  decodeRouteImportSSEFrame,
  del,
  get,
  post,
  put,
  streamSSE,
} from "./client";

export const upstreamsApi = {
  list: (signal?: AbortSignal) => get<Upstream[]>("/upstreams", undefined, signal),
  create: (input: CreateUpstream) => post<Upstream>("/upstreams", input),
  update: (id: string, input: UpdateUpstream) => put<Upstream>(`/upstreams/${id}`, input),
  remove: (id: string) => del<void>(`/upstreams/${id}`),
  models: (id: string) =>
    get<{ models?: string[] }>(`/upstreams/${id}/models`).then((value) => value.models ?? []),
  presets: () => get<ProviderPresetDTO[]>("/provider-presets"),
  testHealth: (
    id: string,
    onEvent: (event: ProviderHealthEvent) => void,
    signal?: AbortSignal,
  ) => streamSSE(`/upstreams/${id}/test`, { signal }, decodeProviderHealthSSEFrame, onEvent),
  testDraft: (
    input: CreateUpstream,
    onEvent: (event: ProviderHealthEvent) => void,
    signal?: AbortSignal,
  ) =>
    streamSSE(
      "/upstreams/test-draft/stream",
      { body: input, signal },
      decodeProviderHealthSSEFrame,
      onEvent,
    ),
  testEditDraft: (
    id: string,
    input: CreateUpstream,
    onEvent: (event: ProviderHealthEvent) => void,
    signal?: AbortSignal,
  ) =>
    streamSSE(
      `/upstreams/${id}/test-draft/stream`,
      { body: input, signal },
      decodeProviderHealthSSEFrame,
      onEvent,
    ),
  importRoutes: (
    id: string,
    onEvent: (event: RouteImportEvent) => void,
    signal?: AbortSignal,
  ) =>
    streamSSE(
      `/upstreams/${id}/routes/import/stream`,
      { signal },
      decodeRouteImportSSEFrame,
      onEvent,
    ),
  importPreview: (id: string) =>
    get<RouteImportPreview>(`/upstreams/${id}/routes/import/preview`),
};
