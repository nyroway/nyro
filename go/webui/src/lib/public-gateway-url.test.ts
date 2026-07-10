import { describe, expect, it } from "vitest";
import { normalizePublicGatewayURL } from "./public-gateway-url";

describe("normalizePublicGatewayURL", () => {
  it("trims a root HTTP(S) URL and removes its trailing slash", () => {
    expect(normalizePublicGatewayURL("  https://ai.example.com/  ")).toBe("https://ai.example.com");
    expect(normalizePublicGatewayURL("http://127.0.0.1:19530")).toBe("http://127.0.0.1:19530");
    expect(normalizePublicGatewayURL("   ")).toBe("");
  });

  it.each([
    "ftp://ai.example.com",
    "https://ai.example.com/v1",
    "https://ai.example.com?tenant=one",
    "https://ai.example.com?",
    "https://ai.example.com#fragment",
    "https://ai.example.com#",
    "https://user:pass@ai.example.com",
  ])("rejects non-root public URL %s", (value) => {
    expect(normalizePublicGatewayURL(value)).toBeNull();
  });
});
