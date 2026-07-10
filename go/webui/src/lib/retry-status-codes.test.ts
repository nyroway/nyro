import { describe, expect, it } from "vitest";
import { DEFAULT_RETRY_STATUS_CODES, decodeRetryStatusCodes, encodeRetryStatusCodes, parseRetryStatusCodes } from "./retry-status-codes";

describe("retry status codes", () => {
  it("accepts 400 through 599 and removes duplicates in entry order", () => {
    expect(parseRetryStatusCodes("429, 500\n429, 599")).toEqual({ codes: [429, 500, 599], invalid: null });
  });
  it("rejects non-integers and values outside the HTTP error range", () => {
    expect(parseRetryStatusCodes("399").invalid).toBe("399");
    expect(parseRetryStatusCodes("600").invalid).toBe("600");
    expect(parseRetryStatusCodes("500.5").invalid).toBe("500.5");
  });
  it("decodes existing JSON settings and serializes selected chips", () => {
    expect(decodeRetryStatusCodes("[429,500,502]")).toEqual([429, 500, 502]);
    expect(decodeRetryStatusCodes(null)).toEqual(DEFAULT_RETRY_STATUS_CODES);
    expect(encodeRetryStatusCodes([429, 500])).toBe("[429,500]");
  });
});
