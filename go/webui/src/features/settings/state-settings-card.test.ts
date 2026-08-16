import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";

import { StateSettingsCard, StateURLField } from "./state-settings-card";

describe("state settings card", () => {
  it("hides the complete Redis URL until explicitly revealed", () => {
    const props = {
      isZh: false,
      value: "redis://redis.internal:6379/2",
      invalid: false,
      disabled: false,
      builtInRedisURL: null,
      onChange: vi.fn(),
      onToggleReveal: vi.fn(),
    };

    const hidden = renderToStaticMarkup(createElement(StateURLField, { ...props, reveal: false }));
    expect(hidden).toContain('type="password"');
    expect(hidden).toContain('aria-label="Show"');

    const revealed = renderToStaticMarkup(createElement(StateURLField, { ...props, reveal: true }));
    expect(revealed).toContain('type="text"');
    expect(revealed).toContain('aria-label="Hide"');
  });

  it("enables the built-in shortcut only when a usable address is available", () => {
    const props = {
      isZh: false,
      value: "",
      reveal: false,
      invalid: true,
      disabled: false,
      onChange: vi.fn(),
      onToggleReveal: vi.fn(),
    };
    const button = (markup: string) => markup.match(/<button[^>]*aria-label="Use built-in"[^>]*>/)?.[0] ?? "";

    const unavailable = renderToStaticMarkup(createElement(StateURLField, {
      ...props,
      builtInRedisURL: null,
    }));
    expect(button(unavailable)).toContain(' disabled=""');

    const available = renderToStaticMarkup(createElement(StateURLField, {
      ...props,
      builtInRedisURL: "redis://127.0.0.1:16379",
    }));
    expect(button(available)).not.toContain(' disabled=""');
  });

  it("blocks editing when either stored setting cannot be loaded", () => {
    const client = new QueryClient({
      defaultOptions: { queries: { enabled: false, retry: false, staleTime: Infinity, refetchOnMount: false } },
    });
    const failed = client.getQueryCache().build(client, {
      queryKey: ["setting", "state.type"],
      queryFn: () => Promise.reject(new Error("offline")),
    });
    failed.setState({
      ...failed.state,
      status: "error",
      fetchStatus: "idle",
      error: new Error("offline"),
      errorUpdateCount: 1,
      errorUpdatedAt: Date.now(),
    });
    client.setQueryData(["setting", "state.url"], "redis://redis.internal:6379/0");

    const markup = renderToStaticMarkup(createElement(
      QueryClientProvider,
      { client },
      createElement(StateSettingsCard, { isZh: false, onError: vi.fn(), builtInRedisURL: null }),
    ));
    expect(markup).toContain("could not be loaded");
    expect(markup).not.toContain('type="password"');
  });

  it("renders a successfully loaded Redis setting as hidden", () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } });
    client.setQueryData(["setting", "state.type"], "redis");
    client.setQueryData(["setting", "state.url"], "redis://redis.internal:6379/0");

    const markup = renderToStaticMarkup(createElement(
      QueryClientProvider,
      { client },
      createElement(StateSettingsCard, {
        isZh: false,
        onError: vi.fn(),
        builtInRedisURL: "redis://127.0.0.1:16379",
      }),
    ));
    expect(markup).toContain('type="password"');
  });
});
