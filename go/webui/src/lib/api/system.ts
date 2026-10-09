import type { GatewayNode, GatewayStatus, RuntimeService } from "@/lib/types";
import { get } from "./client";

export const systemApi = {
  status: () => get<GatewayStatus>("/status"),
  nodes: () => get<GatewayNode[]>("/nodes"),
  runtimeServices: () => get<RuntimeService[]>("/runtime/services"),
};
