import { describe, expect, it } from "vitest";

import { runtimeHTTPURL, runtimeRedisURL } from "./runtime-service-url";

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

describe("runtimeRedisURL", () => {
  it("adds the Redis scheme to IPv4 and bracketed IPv6 listeners", () => {
    expect(runtimeRedisURL("127.0.0.1:16379")).toBe("redis://127.0.0.1:16379");
    expect(runtimeRedisURL("[::1]:16379")).toBe("redis://[::1]:16379");
  });

  it("uses the browser hostname for wildcard listeners", () => {
    expect(runtimeRedisURL(":16379", "console.example.com")).toBe("redis://console.example.com:16379");
    expect(runtimeRedisURL("0.0.0.0:16379", "console.example.com")).toBe("redis://console.example.com:16379");
  });

  it("rejects malformed or missing listeners", () => {
    expect(runtimeRedisURL("::16379", "console.example.com")).toBeNull();
    expect(runtimeRedisURL(undefined)).toBeNull();
  });
});
