import type { CreateRoute, Route, UpdateRoute } from "@/lib/types";
import { del, get, post, put } from "./client";

export const routesApi = {
  list: (signal?: AbortSignal) => get<Route[]>("/routes", undefined, signal),
  create: (input: CreateRoute) => post<Route>("/routes", input),
  update: (id: string, input: UpdateRoute) => put<Route>(`/routes/${id}`, input),
  remove: (id: string) => del<void>(`/routes/${id}`),
};
