import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { AppLayout } from "@/components/layout/app-layout";
import DashboardPage from "@/pages/dashboard";
import ProvidersPage from "@/pages/providers";
import ModelsPage from "@/pages/models-v2";
import ApiKeysPage from "@/pages/api-keys";
import ConnectPage from "@/pages/connect";
import NodesPage from "@/pages/nodes";
import ServicesPage from "@/pages/services";
import LogsPage from "@/pages/logs";
import StatsPage from "@/pages/stats-v2";
import SettingsPage from "@/pages/settings";
import type { GatewayNode, Route, Upstream } from "@/lib/types";

/* §12 视觉验收的结构化等价检查（go-webui-改造方案.md §12）：
   ① 每页静态渲染出的 class 都必须能在三份 CSS（nyro-ui / nyro-app /
      radix-bridge）里找到定义——渲染输出是 DOM 真值，零解析误报；
   ② 反向：nyro-app.css 定义的 class 不允许无人使用（防死 CSS 累积）。
      这一向用“渲染输出 ∪ 源码静态扫描（含测试断言）”，静态侧的枚举值
      误报只会多白名单、不会漏报。
   nyro-ui.css 是共享基线（真源同步），允许存在我们未用的类，不做反向检查。 */

// ── CSS 侧：选择器里出现的全部 class ─────────────────────────────
function cssClasses(css: string): Set<string> {
  const noComments = css.replace(/\/\*[\s\S]*?\*\//g, "");
  return new Set(
    [...noComments.matchAll(/\.([a-zA-Z][a-zA-Z0-9_-]*)/g)].map((m) => m[1]),
  );
}

const styleDir = __dirname;
const appCss = readFileSync(resolve(styleDir, "nyro-app.css"), "utf8");
const uiCss = readFileSync(resolve(styleDir, "nyro-ui.css"), "utf8");
const bridgeCss = readFileSync(resolve(styleDir, "radix-bridge.css"), "utf8");
const defined = new Set<string>([...cssClasses(appCss), ...cssClasses(uiCss), ...cssClasses(bridgeCss)]);

// ── 渲染侧：10 页 + 壳层，全部静态渲染后抽 class ──────────────────
const provider: Upstream = {
  id: "up-openai",
  name: "OpenAI Production",
  provider: "openai",
  protocol: "openai-responses",
  base_url: "https://api.openai.com/v1",
  models: ["gpt-4.1"],
  enabled: true,
};

const route: Route = {
  id: "route-1",
  model: "gpt-4.1",
  balance: "weighted",
  enable_auth: true,
  enabled: true,
  upstreams: [{
    id: "ru-1",
    route_id: "route-1",
    upstream_id: "up-openai",
    model: "gpt-4.1",
    weight: 100,
    priority: 0,
    enabled: true,
  }],
};

const node: GatewayNode = {
  node_id: "node-1",
  hostname: "gw-1",
  app_version: "2.0.0",
  service_port: "8080",
  remote_addr: "10.0.0.1:52000",
  conn_mode: "mtls",
  connected_at: "2026-09-24T08:00:00Z",
  applied_version: 7,
};

const overview = {
  total_requests: 1234,
  total_input_tokens: 90_000,
  total_output_tokens: 45_000,
  avg_duration_ms: 120,
  error_count: 34,
  p95_duration_ms: 240,
};
const hourly = [{
  hour: "2026-09-24T10:00:00Z",
  request_count: 120,
  error_count: 2,
  total_input_tokens: 1,
  total_output_tokens: 1,
  avg_duration_ms: 100,
}];
const routeStats = [{
  route_id: "route-1",
  route_model: "gpt-4.1",
  request_count: 500,
  total_input_tokens: 1,
  total_output_tokens: 1,
  avg_duration_ms: 120,
  error_count: 25,
  p95_duration_ms: 240,
}];
const upstreamStats = [{
  upstream_id: "up-openai",
  upstream_name: "OpenAI Production",
  request_count: 500,
  error_count: 0,
  avg_duration_ms: 120,
  p95_duration_ms: 240,
}];
const consumerStats = [{
  consumer_id: "cons-1",
  request_count: 400,
  total_input_tokens: 60_000,
  total_output_tokens: 30_000,
  cache_read_tokens: 12_000,
  last_used_at: Date.UTC(2026, 8, 24, 12, 0, 0),
}];

const PAGES: { path: string; element: ReactNode }[] = [
  { path: "/", element: createElement(DashboardPage) },
  { path: "/providers", element: createElement(ProvidersPage) },
  { path: "/models", element: createElement(ModelsPage) },
  { path: "/api-keys", element: createElement(ApiKeysPage) },
  { path: "/connect", element: createElement(ConnectPage) },
  { path: "/nodes", element: createElement(NodesPage) },
  { path: "/services", element: createElement(ServicesPage) },
  { path: "/logs", element: createElement(LogsPage) },
  { path: "/stats", element: createElement(StatsPage) },
  { path: "/settings", element: createElement(SettingsPage) },
];

afterEach(() => {
  vi.unstubAllGlobals();
});

function seedAll(queryClient: QueryClient) {
  queryClient.setQueryData(["providers"], [provider]);
  queryClient.setQueryData(["provider-presets"], []);
  queryClient.setQueryData(["routes"], [route]);
  queryClient.setQueryData(["consumers"], []);
  queryClient.setQueryData(["nodes"], [node]);
  queryClient.setQueryData(["runtime-services"], [{ id: "control-plane", status: "running" }]);
  queryClient.setQueryData(["gateway-status"], { status: "ok", version: "2.0.0" });
  // dashboard 与 stats 两页的键一个带 hours 一个不带，都铺上
  for (const key of [["stats-overview"], ["stats-overview", 24]]) {
    queryClient.setQueryData(key, overview);
  }
  for (const key of [["stats-hourly"], ["stats-hourly", 24]]) {
    queryClient.setQueryData(key, hourly);
  }
  for (const key of [["stats-routes"], ["stats-routes", 24]]) {
    queryClient.setQueryData(key, routeStats);
  }
  for (const key of [["stats-upstreams"], ["stats-upstreams", 24]]) {
    queryClient.setQueryData(key, upstreamStats);
  }
  queryClient.setQueryData(["stats-consumers", 24], consumerStats);
}

function renderAll(): string {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  seedAll(queryClient);

  const parts = PAGES.map(({ path, element }) =>
    renderToStaticMarkup(createElement(
      QueryClientProvider,
      { client: queryClient },
      createElement(
        MemoryRouter,
        { initialEntries: [path] },
        createElement(LocaleProvider, null, element),
      ),
    )),
  );
  // 壳层与命令面板（关闭态）也走一遍
  parts.push(renderToStaticMarkup(createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/"] },
      createElement(LocaleProvider, null, createElement(AppLayout)),
    ),
  )));
  return parts.join("\n");
}

function htmlClasses(html: string): Set<string> {
  const names = new Set<string>();
  for (const m of html.matchAll(/class="([^"]+)"/g)) {
    for (const token of m[1].split(/\s+/)) names.add(token);
  }
  return names;
}

// ── 静态侧：源码（含测试断言）里的 class 字面量，仅用于反向死 CSS 检查 ──
function collectSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = resolve(dir, entry.name);
    if (entry.isDirectory()) out.push(...collectSourceFiles(path));
    else if (/\.(ts|tsx)$/.test(entry.name)) out.push(path);
  }
  return out;
}

/** 平衡花括号抽取 className={...} 表达式文本。 */
function classNameExpressions(text: string): string[] {
  const exprs: string[] = [];
  for (const m of text.matchAll(/className=\{/g)) {
    let depth = 1;
    let i = m.index! + m[0].length;
    let templateDepth = 0; // >0 表示在模板串的 ${} 里
    let inString: string | null = null;
    while (i < text.length && depth > 0) {
      const ch = text[i];
      if (ch === "\\") { i += 2; continue; }
      if (inString) {
        if (ch === inString) inString = null;
        i += 1;
        continue;
      }
      if (ch === '"' || ch === "'") { inString = ch; i += 1; continue; }
      if (ch === "`") { i += 1; continue; }
      if (ch === "$" && text[i + 1] === "{") { depth += 1; templateDepth += 1; i += 2; continue; }
      if (ch === "{") { depth += 1; i += 1; continue; }
      if (ch === "}") {
        depth -= 1;
        if (templateDepth > 0) templateDepth -= 1;
        i += 1;
        continue;
      }
      i += 1;
    }
    exprs.push(text.slice(m.index! + m[0].length, i - 1));
  }
  return exprs;
}

/** class 词元形状：小写开头 kebab（过滤 locale 枚举、数字、运算符等非类字面量）。 */
const CLASS_TOKEN = /^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$/;

function staticClassNames(): Set<string> {
  const names = new Set<string>();
  for (const file of collectSourceFiles(resolve(styleDir, ".."))) {
    const text = readFileSync(file, "utf8");
    for (const m of text.matchAll(/(?:class|className)="([^"]+)"/g)) {
      for (const token of m[1].split(/\s+/)) {
        if (CLASS_TOKEN.test(token)) names.add(token);
      }
    }
    for (const expr of classNameExpressions(text)) {
      for (const s of expr.matchAll(/"([^"]+)"/g)) {
        for (const token of s[1].split(/\s+/)) {
          if (CLASS_TOKEN.test(token)) names.add(token);
        }
      }
      for (const t of expr.matchAll(/`([^`]+)`/g)) {
        for (const part of t[1].split(/\$\{[^}]*\}/)) {
          for (const token of part.split(/\s+/)) {
            if (CLASS_TOKEN.test(token)) names.add(token);
          }
        }
      }
    }
  }
  return names;
}

const renderedClasses = htmlClasses(renderAll());
const usedClasses = new Set<string>([...renderedClasses, ...staticClassNames()]);

/** 动态词干：`tok-${kind}`、`cols-${n}`、`tone-${tone}` 这类模板拼接前缀。
    以词干开头的已定义类视为可达——组件 API 允许任意值，样式侧备好变体
    （如 cols-6 当前没有页面用，但 MetricLedger 传 6 项时就会命中）。 */
function dynamicStems(): Set<string> {
  const stems = new Set<string>();
  for (const file of collectSourceFiles(resolve(styleDir, ".."))) {
    const text = readFileSync(file, "utf8");
    // 直接匹配模板串开头的动态词干（允许前导空格），嵌套模板同样命中：
    // `tok-${kind}`、` cols-${n}`、` tone-${tone}`。
    for (const m of text.matchAll(/`\s*([a-z][a-z0-9-]*-)\$\{/g)) {
      stems.add(m[1]);
    }
  }
  return stems;
}

const stems = dynamicStems();
const reachableViaStem = (name: string) =>
  [...stems].some((stem) => name.startsWith(stem));

/** lucide-react 给每个图标 SVG 打的库内标记类（lucide、lucide-check…），
    图标外观由 svg 选择器控制，这些类本身无需样式定义。 */
const LIBRARY_MARKER = /^lucide(-|$)/;

describe("class vocabulary completeness (§12)", () => {
  it("renders a meaningful class surface from all 10 pages and the shell", () => {
    expect(renderedClasses.size).toBeGreaterThan(80);
    expect(usedClasses.size).toBeGreaterThan(120);
  });

  it("has a CSS definition for every class in the rendered DOM", () => {
    const undefinedClasses = [...renderedClasses].filter(
      (name) => !defined.has(name) && !LIBRARY_MARKER.test(name),
    );
    expect(undefinedClasses).toEqual([]);
  });

  it("keeps nyro-app.css free of unused rules", () => {
    const unused = [...cssClasses(appCss)].filter(
      (name) => !usedClasses.has(name) && !reachableViaStem(name),
    );
    expect(unused).toEqual([]);
  });
});
