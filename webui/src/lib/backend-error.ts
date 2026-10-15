import { localizedMessage } from "@/lib/messages";
type ErrorPayload = {
  code?: string;
  message?: string;
  params?: Record<string, unknown>;
};

function extractRawErrorMessage(error: unknown): string {
  if (typeof error === "string") return error;
  if (error instanceof Error && typeof error.message === "string") return error.message;
  if (error && typeof error === "object") {
    const obj = error as Record<string, unknown>;
    if (typeof obj.message === "string") return obj.message;
    if (typeof obj.error === "string") return obj.error;
  }
  return String(error);
}

function parseErrorPayload(raw: string): ErrorPayload | null {
  const text = raw.trim();
  if (!text.startsWith("{") || !text.endsWith("}")) return null;
  try {
    const parsed = JSON.parse(text) as ErrorPayload;
    if (!parsed || typeof parsed !== "object") return null;
    return parsed;
  } catch {
    return null;
  }
}

function extractName(params?: Record<string, unknown>) {
  if (!params || typeof params !== "object") return "";
  const value = params.name;
  return typeof value === "string" ? value : "";
}

function extractRouteCount(params?: Record<string, unknown>) {
  if (!params || typeof params !== "object") return 0;
  const value = params.routeCount;
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

function extractProtocol(params?: Record<string, unknown>) {
  if (!params || typeof params !== "object") return "";
  const value = params.protocol;
  return typeof value === "string" ? value : "";
}

function extractModel(params?: Record<string, unknown>) {
  if (!params || typeof params !== "object") return "";
  const value = params.model;
  return typeof value === "string" ? value : "";
}

export function localizeBackendErrorMessage(error: unknown, isZh: boolean): string {
  const raw = extractRawErrorMessage(error);
  const payload = parseErrorPayload(raw);
  if (!payload?.code) return raw;

  const name = extractName(payload.params);
  switch (payload.code) {
    case "PROVIDER_NAME_CONFLICT":
      return localizedMessage(isZh, "errors.providerNameConflict", {
        a: name || localizedMessage(isZh, "errors.nameMissing"),
      });
    case "ROUTE_NAME_CONFLICT":
      return localizedMessage(isZh, "errors.routeNameConflict", {
        a: name || localizedMessage(isZh, "errors.nameMissing"),
      });
    case "API_KEY_NAME_CONFLICT":
      return localizedMessage(isZh, "errors.apiKeyNameConflict", {
        a: name || localizedMessage(isZh, "errors.nameMissing"),
      });
    case "PROVIDER_IN_USE": {
      const routeCount = extractRouteCount(payload.params);
      return routeCount > 0
        ? localizedMessage(isZh, "errors.providerInUse", { a: routeCount })
        : localizedMessage(isZh, "errors.providerInUseGeneric");
    }
    case "ROUTE_PROTOCOL_MODEL_CONFLICT": {
      const protocol = extractProtocol(payload.params) || "openai";
      const model = extractModel(payload.params) || "(unknown)";
      return localizedMessage(isZh, "errors.routeAlreadyExistsIngressProtocolVirtualModelIDIsAlready", { a: protocol, b: model });
    }
    default:
      return payload.message || raw;
  }
}
