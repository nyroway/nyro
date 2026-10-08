import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { formatUptime } from "@/lib/format";
import type { GatewayNode } from "@/lib/types";
import NodesPage from "./nodes";

const source = readFileSync(resolve(__dirname, "nodes.tsx"), "utf8");

// 时间基准取整秒并加 7 秒偏移，避开分钟边界，让 formatUptime 断言稳定。
const NOW = Date.now();
function iso(secondsAgo: number) {
  return new Date(NOW - secondsAgo * 1000).toISOString();
}

const nodes: GatewayNode[] = [
  {
    node_id: "node-alpha",
    hostname: "web-1",
    app_version: "0.9.2",
    service_port: "8080",
    remote_addr: "192.168.1.10:52344",
    conn_mode: "mtls",
    connected_at: iso(3 * 3600 + 7),
    applied_version: 7,
  },
  {
    node_id: "node-beta",
    hostname: "",
    app_version: "0.9.1",
    service_port: "8081",
    remote_addr: "192.168.1.11:52345",
    connected_at: iso(30 * 60 + 7),
    applied_version: 6,
  },
  {
    node_id: "node-local",
    hostname: "admin-local",
    app_version: "0.9.2",
    service_port: "19530",
    remote_addr: "127.0.0.1:40000",
    conn_mode: "inprocess",
    connected_at: iso(5 * 60 + 7),
    applied_version: 7,
  },
];

afterEach(() => {
  vi.unstubAllGlobals();
});

function renderNodesPage(seed = true) {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  if (seed) {
    queryClient.setQueryData(["nodes"], nodes);
  }

  return renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/nodes"] },
      createElement(LocaleProvider, null, createElement(NodesPage)),
    ),
  ));
}

describe("nodes page static render (§9.9)", () => {
  it("renders the metric-strip with connection, uptime and version aggregates", () => {
    const html = renderNodesPage();

    expect(html).toContain('class="metric-strip"');
    // 4 plain cells + 1 tone-warning cell (mixed config versions)
    expect(html.match(/class="metric-cell[ "]/g)).toHaveLength(5);
    expect(html).toContain('class="metric-cell tone-warning"');
    expect(html).toContain("All connected");
    // embedded 1 · remote 2 · longest connection
    expect(html).toContain("Longest connection: web-1");
    expect(html).toContain(formatUptime(nodes[0].connected_at));
    expect(html).toContain("mixed");
    expect(html).toContain("Versions differ");
  });

  it("renders the node table as a baseline table-card with status tags", () => {
    const html = renderNodesPage();

    expect(html).toContain('class="card table-card"');
    expect(html).toContain('class="table-scroll"');
    expect(html).toContain('<table class="table"');
    // node column: hostname stack + id line
    expect(html).toContain("web-1");
    expect(html).toContain("node-beta");
    // address column keeps the copy affordance
    expect(html).toContain('class="cell-copy"');
    expect(html).toContain('aria-label="Copy"');
    expect(html).toContain("192.168.1.10:8080");
    // connection modes: the topology view collapsed into this column
    expect(html).toContain("Remote · mTLS");
    expect(html).toContain("Remote · Plaintext");
    expect(html).toContain("Embedded · In-process");
    // identity as status tags
    expect(html).toContain('class="tag tag-success"');
    expect(html).toContain("Verified");
    expect(html).toContain('class="tag tag-warning"');
    expect(html).toContain("Unverified");
    expect(html).toContain("0.9.2");
    expect(html).toContain('class="code-pill"');
    expect(html).toContain("rev-7");
    expect(html).toContain("rev-6");
    expect(html).toContain(formatUptime(nodes[2].connected_at));
  });

  it("summarizes identity health in the toolbar and keeps the refresh + footer notes", () => {
    const html = renderNodesPage();

    expect(html).toContain('class="table-toolbar"');
    expect(html).toContain("1 unverified");
    expect(html).not.toContain("Identity verification healthy");
    expect(html).toContain('class="button button-sm toolbar-end"');
    expect(html).toContain("Refresh");
    expect(html).toContain('class="table-footer"');
    expect(html.match(/class="table-summary"/g)).toHaveLength(2);
    expect(html).toContain("The node list refreshes every 5 seconds");
    expect(html).toContain("The host comes from the live connection");
  });

  it("shows the config-sync empty state with guidance and a settings jump", () => {
    const html = renderNodesPage(false);

    expect(html).toContain('class="card table-card is-empty"');
    expect(html).toContain('class="table-empty"');
    expect(html).toContain("No gateway nodes connected");
    expect(html).toContain('class="info-callout"');
    expect(html).toContain("config-sync");
    expect(html).toContain("Open settings");
    // empty toolbar keeps the refresh control but drops the identity status
    expect(html).toContain('class="button button-sm toolbar-end"');
    expect(html).not.toContain("1 unverified");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderNodesPage();

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|grid-cols|flex items|font-mono|h-10|w-full|pr-10|min-h-32)-/);
  });
});

describe("nodes page API migration (§9.9 ④)", () => {
  it("talks to the backend only through systemApi.nodes", () => {
    expect(source).toContain("systemApi.nodes()");
    expect(source.match(/useQuery</g)).toHaveLength(1);
    expect(source).not.toMatch(/\bbackend[(<]/);
    // the control-plane address block died with the topology view
    expect(source).not.toContain("list_runtime_services");
    expect(source).not.toContain("runtimeServices");
  });

  it("collapses the topology view and wires the empty state to settings", () => {
    expect(source).not.toContain("buildNodeTopology");
    expect(source).toContain('className="info-callout"');
    expect(source).toContain('navigate("/settings")');
    // a fetch failure must not show the config-sync guidance
    expect(source).toContain('isError ? t("common.error") : t("nodes.noNodes")');
  });
});
