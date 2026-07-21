// Helpers for rendering a connected gateway's real service address, shared by
// the Nodes page (a table column) and the Connect page (base_url fallback when
// gateway.public_url is unset). The two inputs come from independent sources:
// remote_addr is the gRPC peer address admin observed (host + ephemeral source
// port), and service_port is the port the gateway self-reports from its own
// --listen. The source port in remote_addr has no relation to the service
// port, so it is stripped.

// formatAddressHost strips the ephemeral source port from a config-sync gRPC
// peer address, leaving just the host (handles bracketed IPv6).
export function formatAddressHost(addr: string): string {
  if (!addr) return "";
  if (addr.startsWith("[")) {
    // Bracketed IPv6, e.g. "[::1]:54321" -> "[::1]".
    const bracketEnd = addr.indexOf("]");
    return bracketEnd >= 0 ? addr.slice(0, bracketEnd + 1) : addr;
  }
  const lastColon = addr.lastIndexOf(":");
  if (lastColon <= 0) return addr;
  return addr.slice(0, lastColon);
}

// formatServiceAddress combines the host from remote_addr with the
// self-reported service_port into "host:port". Either side can be missing (an
// older gateway build, or a connection still mid-handshake), so each falls
// back independently rather than collapsing to "-" when only one is present.
export function formatServiceAddress(remoteAddr: string, servicePort: string): string {
  const host = formatAddressHost(remoteAddr);
  if (!host && !servicePort) return "-";
  if (!host) return `:${servicePort}`;
  if (!servicePort) return host;
  return `${host}:${servicePort}`;
}
