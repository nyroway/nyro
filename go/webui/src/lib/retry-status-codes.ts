export const DEFAULT_RETRY_STATUS_CODES = [429, 500, 502, 503, 504] as const;

export function parseRetryStatusCodes(input: string) {
  const codes: number[] = [];
  for (const token of input.split(/[\n,]/).map((value) => value.trim()).filter(Boolean)) {
    if (!/^\d+$/.test(token)) return { codes, invalid: token };
    const code = Number(token);
    if (code < 400 || code > 599) return { codes, invalid: token };
    if (!codes.includes(code)) codes.push(code);
  }
  return { codes, invalid: null };
}

export function encodeRetryStatusCodes(codes: readonly number[]) {
  return JSON.stringify(codes);
}

export function decodeRetryStatusCodes(raw: string | null | undefined): number[] {
  // Return default if absent
  if (raw === null || raw === undefined) {
    return [...DEFAULT_RETRY_STATUS_CODES];
  }

  // Try to parse as JSON first
  try {
    const parsed = JSON.parse(raw);
    if (Array.isArray(parsed) && parsed.every((code) => typeof code === "number" && code >= 400 && code <= 599)) {
      return parsed;
    }
  } catch {
    // Not JSON, fall through to legacy parsing
  }

  // Fall back to comma-separated legacy text
  const result = parseRetryStatusCodes(raw);
  if (result.invalid === null) {
    return result.codes;
  }

  // Return default for invalid input
  return [...DEFAULT_RETRY_STATUS_CODES];
}

export function sameRetryStatusCodes(left: readonly number[], right: readonly number[]): boolean {
  return left.length === right.length && left.every((code, index) => code === right[index]);
}
