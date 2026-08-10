import { describe, expect, it } from "vitest";

import { runtimeHTTPURL } from "./runtime-service-url";

describe("runtimeHTTPURL", () => {
  it("adds an HTTP scheme to a listen address", () => {
    expect(runtimeHTTPURL("127.0.0.1:14318")).toBe("http://127.0.0.1:14318");
  });

  it("uses the browser hostname for wildcard listeners", () => {
    expect(runtimeHTTPURL(":14318", "console.example.com")).toBe("http://console.example.com:14318");
    expect(runtimeHTTPURL("0.0.0.0:14318", "console.example.com")).toBe("http://console.example.com:14318");
  });

  it("preserves explicit HTTP URLs and rejects missing values", () => {
    expect(runtimeHTTPURL("https://observe.example.com")).toBe("https://observe.example.com");
    expect(runtimeHTTPURL(undefined)).toBeNull();
  });
});
