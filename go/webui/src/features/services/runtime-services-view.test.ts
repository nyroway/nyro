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
  it("presents the V2 service summary, table, and startup note in order", () => {
    const html = renderServices();
    const ribbon = html.indexOf("v2-services-ribbon");
    const table = html.indexOf("v2-service-table");
    const note = html.indexOf("v2-service-note");

    expect(ribbon).toBeGreaterThanOrEqual(0);
    expect(table).toBeGreaterThan(ribbon);
    expect(note).toBeGreaterThan(table);
    expect(html).toContain("<table");
    expect(html).toContain("services.tableTitle");
    expect(html).toContain("services.noteTitle");
  });

  it("keeps every runtime component as a compact service row", () => {
    const html = renderServices();

    expect(html.match(/class="v2-service-name"/g)).toHaveLength(4);
    expect(html).toContain("common.running");
    expect(html).toContain("common.disabled");
  });
});
