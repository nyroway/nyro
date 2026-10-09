import { createElement, type ReactNode } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type { GatewayNode, GatewayStatus } from "@/lib/types";
import { AppLayout } from "./app-layout";

const source = readFileSync(resolve(__dirname, "app-layout.tsx"), "utf8");
const shellSource = readFileSync(resolve(__dirname, "nyro-app-shell.tsx"), "utf8");
const uiCss = readFileSync(resolve(__dirname, "../../styles/nyro-ui.css"), "utf8");
const appCss = readFileSync(resolve(__dirname, "../../styles/nyro-app.css"), "utf8");

afterEach(() => {
  vi.unstubAllGlobals();
});

function withShellContext(children: ReactNode, locale: "en-US" | "zh-CN" = "en-US") {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: (key: string) => key === "nyro-locale" ? locale : null,
      setItem: () => undefined,
    },
  });

  return createElement(
    MemoryRouter,
    { initialEntries: ["/"] },
    createElement(LocaleProvider, null, children),
  );
}

function renderAppLayout(locale: "en-US" | "zh-CN" = "en-US", nodes?: GatewayNode[]) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  const status: GatewayStatus = { status: "ok", version: "2.0.0" };
  queryClient.setQueryData(["gateway-status"], status);
  if (nodes) queryClient.setQueryData(["nodes"], nodes);

  return renderToStaticMarkup(
    createElement(
      QueryClientProvider,
      { client: queryClient },
      withShellContext(createElement(AppLayout), locale),
    ),
  );
}

describe("application shell", () => {
  it("keeps the topbar limited to search, readiness, language, and theme controls", () => {
    const html = renderAppLayout();

    expect(html).toContain("Search pages or resources");
    expect(html).toContain("Status unknown");
    expect(html).toContain('aria-label="Language"');
    expect(html).toContain('title="Theme"');
    expect(html).not.toContain('aria-label="About Nyro"');
  });

  it("renders the version inside the brand lockup and keeps the external links in the sidebar", () => {
    const html = renderAppLayout();
    const brand = html.indexOf("CONSOLE");
    const versionLabel = html.indexOf("Version", brand);
    const version = html.indexOf("2.0.0", versionLabel);
    const github = html.indexOf("GitHub");
    const documentation = html.indexOf("Documentation");

    // nyro 壳设计：版本随品牌锁定在侧栏顶部，外链收在侧栏底部。
    expect(brand).toBeGreaterThanOrEqual(0);
    expect(versionLabel).toBeGreaterThan(brand);
    expect(version).toBeGreaterThan(versionLabel);
    expect(github).toBeGreaterThan(version);
    expect(documentation).toBeGreaterThan(github);
  });

  it("does not duplicate the built-in service summary in the sidebar", () => {
    expect(renderAppLayout()).not.toContain("Built-in services healthy");
  });

  it("uses the concise key label in the Chinese navigation", () => {
    const html = renderAppLayout("zh-CN");

    expect(html).toContain('href="/api-keys"');
    expect(html).toContain("<span>密钥</span>");
    expect(html).not.toContain(">API 密钥</a>");
  });

  it("groups the sidebar like the baseline (overview | upstream | runtime | divider | settings) with icons", () => {
    const html = renderAppLayout("zh-CN");

    // 基线分组（真源 providers.html .nav）：概览（无组名）→ 上游 → 运行 → nav-divider → 设置（无组名）
    const overview = html.indexOf("<span>概览</span>");
    const upstream = html.indexOf(">上游</p>");
    const runtime = html.indexOf(">运行</p>");
    const divider = html.indexOf('class="nav-divider"');
    const settings = html.indexOf("<span>设置</span>");

    expect(overview).toBeGreaterThanOrEqual(0);
    expect(upstream).toBeGreaterThan(overview);
    expect(runtime).toBeGreaterThan(upstream);
    expect(divider).toBeGreaterThan(runtime);
    expect(settings).toBeGreaterThan(divider);
    // 每个导航项都带基线 nav-icon（lucide 会附加 lucide-* 库标记类）
    expect(html).toMatch(/class="[^"]*\bnav-icon\b/);
    // 旧四组分组（配置/访问/可观测/系统）不再出现
    expect(html).not.toContain(">配置</p>");
    expect(html).not.toContain(">访问</p>");
    expect(html).not.toContain(">可观测</p>");
    expect(html).not.toContain(">系统</p>");
  });

  it("keeps the topbar from shrinking inside the fixed-shell flex column (live-verified)", () => {
    // 固定壳列里 topbar 是 flex 子项，默认 flex-shrink:1 会在内容超高时被压扁
    // （基线 56px → 39.75px，与侧栏 brand 错位）——必须锁死不收缩。
    expect(appCss).toMatch(/\.app-shell \.main > \.topbar\s*\{[^}]*flex:\s*0 0 auto/s);
  });

  it("renders the skip link before the shell landmarks", () => {
    const html = renderAppLayout();

    expect(html.indexOf('class="skip-link"')).toBeGreaterThanOrEqual(0);
    expect(html.indexOf('class="skip-link"')).toBeLessThan(html.indexOf('class="sidebar"'));
  });

  it("mirrors data-theme onto <body> so baseline body-prefixed dark rules apply", () => {
    // 真源的暗色开关规则写在 body[data-theme="dark"] 前缀上（nyro-ui.css 里
    // 仅有的 4 条 body 前缀规则）；data-theme 只挂 <html> 时它们全部失配，
    // 暗色下开关仍呈亮色（ON 态白轨白钮、滑块不可见）。壳须同步镜像到 <body>。
    expect(shellSource).toContain("document.documentElement.dataset.theme = theme");
    expect(shellSource).toContain("document.body.dataset.theme = theme");
    // 同步副本必须保有这些真源规则（挂载点修复依赖它们，防手工漂移）
    expect(uiCss).toContain('body[data-theme="dark"] .switch-control.on { background: #ffffff; }');
    expect(uiCss).toContain('body[data-theme="dark"] .switch-control.on::after { background: #000000; }');
  });
});

describe("application shell API migration (Phase 3)", () => {
  it("talks to the backend only through the typed api client", () => {
    expect(source).toContain("systemApi.nodes()");
    expect(source).toContain("systemApi.status()");
    expect(source).not.toMatch(/\bbackend[(<]/);
    // the legacy shell escape hatch is gone
    expect(source).not.toContain("useSearchParams");
    expect(source).not.toContain("shell=v2");
    expect(source).not.toContain("Sidebar");
  });
});

describe("action-icon hover tips follow the baseline hover-tip layer", () => {
  it("mounts the body-level #hoverTip fixed layer and drives it by delegation", () => {
    // 真源 providers.html：.icon-action 的提示走 body 级 .hover-tip 固定层
    // （syncActionTips + 委托 mouseover），不用原生 title。
    expect(shellSource).toContain('<div className="hover-tip" id="hoverTip" hidden />');
    expect(shellSource).toContain('closest?.(".icon-action")');
    expect(shellSource).toContain('button.dataset.tip || button.getAttribute("aria-label")');
    expect(shellSource).toContain('box.left + box.width / 2');
    expect(shellSource).toContain('if (!next || !next.closest?.(".icon-action")) tip.hidden = true;');
    // 固定层样式来自同步的 nyro-ui.css（fixed + 上浮 transform），锁依赖防漂移
    expect(uiCss).toMatch(/\.hover-tip\s*\{[^}]*position:\s*fixed/s);
    expect(uiCss).toMatch(/\.hover-tip\s*\{[^}]*transform:\s*translate\(-50%,\s*calc\(-100% - 6px\)\)/s);
  });

  it("keeps every icon-action button on data-tip, never the native title", () => {
    // 真源终态：aria-label + data-tip、无 title（原生提示会和固定层叠加）。
    const files = [
      "pages/providers.tsx",
      "pages/models-v2.tsx",
      "pages/api-keys.tsx",
      "components/v2/row-action-menu.tsx",
    ];
    for (const file of files) {
      const src = readFileSync(resolve(__dirname, "../../" + file), "utf8");
      // 每个按钮标签开头（到首个 ">" 前）都带 data-tip 且无 title
      const tags = src.match(/<button[^>]*icon-action[^>]*/gs) ?? [];
      expect(tags.length, `${file} has icon-action buttons`).toBeGreaterThan(0);
      for (const tag of tags) {
        expect(tag.includes("data-tip="), `${file}: ${tag.slice(0, 80)}…`).toBe(true);
        expect(tag.includes("title="), `${file}: ${tag.slice(0, 80)}…`).toBe(false);
      }
      // 总量核对：data-tip 数不少于 icon-action 数（防漏改新按钮）
      const icons = (src.match(/icon-action/g) ?? []).length;
      const tips = (src.match(/data-tip=/g) ?? []).length;
      expect(tips, file).toBeGreaterThanOrEqual(icons);
    }
  });
});

describe("row menus overflow the list card without scrollbars (baseline)", () => {
  it("lets page-level table cards overflow like .content > .table-card in the baseline", () => {
    // 真源 nyro-ui.css 的 .content > .table-card { overflow: visible } 让行内
    // 菜单自由溢出卡缘；app 的 PageLayout 多一层 .content-body，等价改写。
    expect(appCss).toMatch(/\.content-body > \.table-card\s*\{[^}]*overflow:\s*visible/s);
  });

  it("scopes the log-table horizontal scroll so menus never grow scrollHeight", () => {
    // overflow-x:auto 会把 overflow-y:visible 计算成 auto——全表生效时
    // “更多”菜单下探即撑出竖向滚动条；只能挂在 .log-table 上。
    expect(appCss).toMatch(/\.table-card\.log-table \.table-scroll\s*\{[^}]*overflow-x:\s*auto/s);
    expect(appCss).not.toMatch(/^\.table-card \.table-scroll\s*\{/m);
    // 翻转类与行内菜单定位来自同步的 nyro-ui.css，锁依赖
    expect(uiCss).toMatch(/\.row-menu\.flip-up\s*\{[^}]*bottom:\s*calc\(100% \+ 4px\)/s);
    expect(uiCss).toMatch(/\.action-menu\.open \.row-menu\s*\{[^}]*display:\s*flex/s);
  });
});

describe("toolbar add buttons follow the baseline text-only form", () => {
  it("renders toolbar-add without icons on every list page (providers.html:290)", () => {
    // 真源的 toolbar-add 是纯文字按钮（<span>新增提供商</span>，三处出现均无
    // 图标）；app 曾自行叠加 lucide Plus——16px/描边 2 的 + 与 12px 文案的
    // 视觉重心不一致，正是“+号与文案不在同一行”的观感来源。统一还原纯文字。
    const files = [
      "pages/providers.tsx",
      "pages/models-v2.tsx",
      "pages/api-keys.tsx",
    ];
    for (const file of files) {
      const src = readFileSync(resolve(__dirname, "../../" + file), "utf8");
      const btns = src.match(/<button[^>]*toolbar-add[\s\S]*?<\/button>/g) ?? [];
      expect(btns.length, `${file} has a toolbar-add button`).toBeGreaterThanOrEqual(1);
      for (const btn of btns) {
        expect(btn.includes("<Plus"), `${file}: toolbar-add embeds lucide Plus`).toBe(false);
        expect(btn.includes("<svg"), `${file}: toolbar-add embeds an icon`).toBe(false);
      }
    }
  });
});
