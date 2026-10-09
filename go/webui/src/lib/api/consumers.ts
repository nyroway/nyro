import type {
  Consumer,
  ConsumerKey,
  CreateConsumer,
  CreateConsumerKey,
  UpdateConsumer,
  UpdateConsumerKey,
} from "@/lib/types";
import { del, get, post, put } from "./client";

export const consumersApi = {
  list: (signal?: AbortSignal) => get<Consumer[]>("/consumers", undefined, signal),
  create: (input: CreateConsumer) => post<Consumer>("/consumers", input),
  update: (id: string, input: UpdateConsumer) => put<Consumer>(`/consumers/${id}`, input),
  remove: (id: string) => del<void>(`/consumers/${id}`),
  // The raw token is only exposed in the creation response (one-time display).
  addKey: (id: string, input: CreateConsumerKey) =>
    post<ConsumerKey>(`/consumers/${id}/keys`, input),
  updateKey: (id: string, keyId: string, input: UpdateConsumerKey) =>
    put<ConsumerKey>(`/consumers/${id}/keys/${keyId}`, input),
  removeKey: (id: string, keyId: string) => del<void>(`/consumers/${id}/keys/${keyId}`),
};
