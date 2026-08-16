function browserHostname(explicit?: string) {
  if (explicit) return explicit;
  return typeof window === "undefined" ? "127.0.0.1" : window.location.hostname;
}

function runtimeNetworkAddress(listen?: string, currentHostname?: string): string | null {
  const value = listen?.trim();
  if (!value) return null;

  let host = "";
  let port = "";
  if (value.startsWith("[")) {
    const closing = value.indexOf("]");
    if (closing < 0 || value[closing + 1] !== ":") return null;
    host = value.slice(1, closing);
    port = value.slice(closing + 2);
  } else {
    const separator = value.lastIndexOf(":");
    if (separator < 0) return null;
    host = value.slice(0, separator);
    port = value.slice(separator + 1);
    if (host.includes(":")) return null;
  }
  if (!/^\d+$/.test(port)) return null;
  if (!host || host === "0.0.0.0" || host === "::") host = browserHostname(currentHostname);
  const formattedHost = host.includes(":") ? `[${host}]` : host;
  return `${formattedHost}:${port}`;
}

export function runtimeHTTPURL(listen?: string, currentHostname?: string): string | null {
  const value = listen?.trim();
  if (!value) return null;
  if (/^https?:\/\//i.test(value)) return value.replace(/\/$/, "");
  const address = runtimeNetworkAddress(value, currentHostname);
  return address ? `http://${address}` : null;
}

export function runtimeRedisURL(listen?: string, currentHostname?: string): string | null {
  const address = runtimeNetworkAddress(listen, currentHostname);
  return address ? `redis://${address}` : null;
}
