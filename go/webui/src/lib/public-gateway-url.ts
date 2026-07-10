export function normalizePublicGatewayURL(input: string): string | null {
  const value = input.trim();
  if (!value) return "";

  try {
    const url = new URL(value);
    if (
      (url.protocol !== "http:" && url.protocol !== "https:")
      || !url.hostname
      || value.includes("?")
      || value.includes("#")
      || url.username
      || url.password
      || (url.pathname !== "" && url.pathname !== "/")
      || url.search
      || url.hash
    ) {
      return null;
    }
    return url.origin;
  } catch {
    return null;
  }
}
