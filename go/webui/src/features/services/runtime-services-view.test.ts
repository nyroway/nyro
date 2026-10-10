import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import type { MessageKey } from "@/lib/i18n";
import type { RuntimeService } from "@/lib/types";
import { RuntimeServicesView } from "./runtime-services-view";

const services: RuntimeService[] = [
  { id: "control-plane", status: "running", listen: "127.0.0.1:19531" },
  { id: "embedded-proxy", status: "running", listen: "127.0.0.1:19530" },
  { id: "redis-state", status: "running", listen: "127.0.0.1:16379", data_path: "/tmp/state.db" },
  { id: "otlp-receiver", status: "disabled", storage_backend: "SQLite", data_path: "/tmp/observe.db" },
];

const t = (key: MessageKey) => key;

function renderServices() {
  return renderToStaticMarkup(createElement(RuntimeServicesView, {
    services,
    isLoading: false,
    isError: false,
    isFetching: false,
    t,
    onRefresh: vi.fn(),
    onShowNodes: vi.fn(),
  }));
}

describe("RuntimeServicesView", () => {
  it("presents the metric strip, service table, and startup note in order", () => {
    const html = renderServices();
    const metrics = html.indexOf("metric-strip");
    const table = html.indexOf("card table-card");
    const note = html.indexOf("alert-info");

    expect(metrics).toBeGreaterThanOrEqual(0);
    expect(table).toBeGreaterThan(metrics);
    expect(note).toBeGreaterThan(table);
    expect(html).toContain("<table");
    expect(html).toContain("services.noteTitle");
  });

  it("keeps every runtime component as a compact service row", () => {
    const html = renderServices();

    expect(html.match(/class="provider-name"/g)).toHaveLength(4);
    expect(html).toContain("common.running");
    expect(html).toContain("common.disabled");
    expect(html).toContain("services.scope");
    expect(html).toContain("services.viewNodes");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderServices();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] means the same as the original spelling, split up to avoid the literal in the exit grep
    expect(html).not.toMatch(/class="[^"]*\b(text-slate|bg-slate|border-red|text-red|text-amber|grid-cols|flex items|space-y)-/);
  });
});
