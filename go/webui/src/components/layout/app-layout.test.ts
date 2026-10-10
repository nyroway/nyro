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

    // Nyro shell design: the version is locked with the brand at the top of the sidebar, and external links are kept at the bottom of the sidebar.
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

    // Baseline grouping (baseline providers.html .nav): Overview (no group name) → Upstream → Runtime → nav-divider → Settings (no group name)
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
    // Every nav item carries the baseline nav-icon (lucide appends lucide-* library marker classes)
    expect(html).toMatch(/class="[^"]*\bnav-icon\b/);
    // The old four-group layout (Config/Access/Observability/System) no longer appears
    expect(html).not.toContain(">配置</p>");
    expect(html).not.toContain(">访问</p>");
    expect(html).not.toContain(">可观测</p>");
    expect(html).not.toContain(">系统</p>");
  });

  it("keeps the topbar from shrinking inside the fixed-shell flex column (live-verified)", () => {
    // In the fixed shell column the topbar is a flex child, and the default flex-shrink:1 squashes it when content is too tall
    // (baseline 56px → 39.75px, misaligned with the sidebar brand) — it must be locked so it never shrinks.
    expect(appCss).toMatch(/\.app-shell \.main > \.topbar\s*\{[^}]*flex:\s*0 0 auto/s);
  });

  it("renders the skip link before the shell landmarks", () => {
    const html = renderAppLayout();

    expect(html.indexOf('class="skip-link"')).toBeGreaterThanOrEqual(0);
    expect(html.indexOf('class="skip-link"')).toBeLessThan(html.indexOf('class="sidebar"'));
  });

  it("mirrors data-theme onto <body> so baseline body-prefixed dark rules apply", () => {
    // The baseline's dark-mode switch rules are written with a body[data-theme="dark"] prefix (the only 4
    // body-prefixed rules in nyro-ui.css); with data-theme set on <html> alone they all fail to match, so
    // in dark mode the switch still renders light (white track and knob when ON, slider invisible). The shell must mirror it onto <body>.
    expect(shellSource).toContain("document.documentElement.dataset.theme = theme");
    expect(shellSource).toContain("document.body.dataset.theme = theme");
    // The synced copy must keep these baseline rules (the mount-point fix depends on them; guards against manual drift)
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
    // Baseline providers.html: .icon-action tips go through the body-level .hover-tip fixed layer
    // (syncActionTips + delegated mouseover), not the native title.
    expect(shellSource).toContain('<div className="hover-tip" id="hoverTip" hidden />');
    expect(shellSource).toContain('closest?.(".icon-action")');
    expect(shellSource).toContain('button.dataset.tip || button.getAttribute("aria-label")');
    expect(shellSource).toContain('box.left + box.width / 2');
    expect(shellSource).toContain('if (!next || !next.closest?.(".icon-action")) tip.hidden = true;');
    // The fixed layer's styles come from the synced nyro-ui.css (fixed + upward transform); lock the dependency against drift
    expect(uiCss).toMatch(/\.hover-tip\s*\{[^}]*position:\s*fixed/s);
    expect(uiCss).toMatch(/\.hover-tip\s*\{[^}]*transform:\s*translate\(-50%,\s*calc\(-100% - 6px\)\)/s);
  });

  it("keeps every icon-action button on data-tip, never the native title", () => {
    // Baseline end state: aria-label + data-tip, no title (native tips would stack on top of the fixed layer).
    const files = [
      "pages/providers.tsx",
      "pages/models-v2.tsx",
      "pages/api-keys.tsx",
      "components/v2/row-action-menu.tsx",
    ];
    for (const file of files) {
      const src = readFileSync(resolve(__dirname, "../../" + file), "utf8");
      // The head of every button tag (up to the first ">") carries data-tip and no title
      const tags = src.match(/<button[^>]*icon-action[^>]*/gs) ?? [];
      expect(tags.length, `${file} has icon-action buttons`).toBeGreaterThan(0);
      for (const tag of tags) {
        expect(tag.includes("data-tip="), `${file}: ${tag.slice(0, 80)}…`).toBe(true);
        expect(tag.includes("title="), `${file}: ${tag.slice(0, 80)}…`).toBe(false);
      }
      // Total count check: the number of data-tip is no less than the number of icon-action (guards against missing newly added buttons)
      const icons = (src.match(/icon-action/g) ?? []).length;
      const tips = (src.match(/data-tip=/g) ?? []).length;
      expect(tips, file).toBeGreaterThanOrEqual(icons);
    }
  });
});

describe("row menus overflow the list card without scrollbars (baseline)", () => {
  it("lets page-level table cards overflow like .content > .table-card in the baseline", () => {
    // The baseline nyro-ui.css rule .content > .table-card { overflow: visible } lets row menus
    // overflow the card edge freely; the app's PageLayout adds a .content-body layer, so this is the equivalent rewrite.
    expect(appCss).toMatch(/\.content-body > \.table-card\s*\{[^}]*overflow:\s*visible/s);
  });

  it("scopes the log-table horizontal scroll so menus never grow scrollHeight", () => {
    // overflow-x:auto computes overflow-y:visible as auto — applied to the whole
    // table, the "More" menu reaching down would stretch out a vertical scrollbar; it can only hang on .log-table.
    expect(appCss).toMatch(/\.table-card\.log-table \.table-scroll\s*\{[^}]*overflow-x:\s*auto/s);
    expect(appCss).not.toMatch(/^\.table-card \.table-scroll\s*\{/m);
    // The flip class and row-menu positioning come from the synced nyro-ui.css; lock the dependency
    expect(uiCss).toMatch(/\.row-menu\.flip-up\s*\{[^}]*bottom:\s*calc\(100% \+ 4px\)/s);
    expect(uiCss).toMatch(/\.action-menu\.open \.row-menu\s*\{[^}]*display:\s*flex/s);
  });
});

describe("toolbar add buttons follow the baseline text-only form", () => {
  it("renders toolbar-add without icons on every list page (providers.html:290)", () => {
    // The baseline's toolbar-add is a text-only button (<span>Add provider</span>, all three occurrences carry
    // no icon); the app had layered lucide Plus on top — the 16px/stroke-2 + and the 12px label have
    // mismatched visual weight, which is exactly the source of the "+ and label not on one line" look. Uniformly restore text-only.
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
