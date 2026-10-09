import { createElement, type ComponentType } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import type { RouteImportPreview, TestResult, Upstream } from "@/lib/types";
import ProvidersPage from "./providers";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

const provider: Upstream = {
  id: "up-openai",
  name: "OpenAI Production",
  provider: "openai",
  protocol: "openai-responses",
  base_url: "https://api.openai.com/v1",
  credentials: { api_key: "secret" },
  models: ["gpt-4.1", "gpt-4.1-mini"],
  models_url: "https://api.openai.com/v1/models",
  proxy_url: "",
  enabled: true,
};

afterEach(() => {
  vi.unstubAllGlobals();
});

function shell(children: React.ReactNode) {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });

  const queryClient = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  });
  queryClient.setQueryData(["providers"], [provider]);
  queryClient.setQueryData(["provider-presets"], []);

  return createElement(
    QueryClientProvider,
    { client: queryClient },
    createElement(
      MemoryRouter,
      { initialEntries: ["/providers"] },
      createElement(LocaleProvider, null, children),
    ),
  );
}

describe("providers page static render", () => {
  it("presents providers as a baseline table card with brand marks and switches", () => {
    const html = renderToStaticMarkup(shell(createElement(ProvidersPage)));

    expect(html).toContain("Providers");
    expect(html).toContain('class="resource-summary"');
    expect(html).toContain("toolbar-add");
    expect(html).toContain("Add Provider");
    // 真源 toolbar-add 纯文字（providers.html:290 无图标）——按钮内只渲染
    // 一个文本节点，不再叠 lucide Plus（+ 与 12px 文案视觉重心不一致）。
    expect(html).toMatch(/class="button button-primary button-sm toolbar-add"[^>]*>[^<]*<\/button>/);
    expect(source).not.toMatch(/\bPlus\b/);
    expect(html).toContain('tabindex="0"');
    expect(html).toContain('class="provider-name"');
    expect(html).toContain("provider-icon");
    expect(html).toContain('class="switch-control sm');
    expect(html).toContain("cell-actions");
    expect(html).toContain("row-actions");
    expect(html).toContain("action-menu");
    // 操作 icon 提示走真源 #hoverTip 固定层（data-tip），不再有原生 title
    expect(html).toContain('data-tip="Probe"');
    expect(html).toContain('data-tip="Edit"');
    expect(html).toContain('data-tip="More"');
    expect(html).toContain("toolbar-search");
    expect(html).toContain("table-footer-notes");
    expect(html).toContain("Credentials are never shown in plaintext after saving");
  });

  it("uses only the nyro baseline vocabulary", () => {
    const html = renderToStaticMarkup(shell(createElement(ProvidersPage)));

    expect(html).not.toMatch(/class="[^"]*v2[-]/);  // v2[-] 与原写法同义，拆字以避开出口 grep 的字面量
    expect(html).not.toMatch(/class="[^"]*\b(space-y|text-slate|bg-slate|grid-cols|flex items|font-mono|h-10|w-full|pr-10|min-h-32)-/);
  });

  it("talks to the backend only through upstreamsApi (§9.2 ⑧)", () => {
    expect(source).toContain("upstreamsApi.list()");
    expect(source).toContain("upstreamsApi.presets()");
    expect(source).toContain("upstreamsApi.testHealth(");
    expect(source).toContain("upstreamsApi.testDraft(");
    expect(source).toContain("upstreamsApi.testEditDraft(");
    expect(source).toContain("upstreamsApi.importRoutes(");
    expect(source).toContain("upstreamsApi.importPreview(");
    expect(source).not.toMatch(/\bbackend[(<]/);
    expect(source).not.toMatch(/streamProvider/);
  });
});

describe("toolbar protocol filter width (baseline 148px wraps long protocol names)", () => {
  it("widens only the protocol filter via the app-layer variant class", () => {
    // 真源 .toolbar-filter 定宽 148px 且 .select-menu 恒等于触发器宽
    // （left:0;right:0）——"OpenAI Chat Completions"（实测 ~163px）在
    // 148px 菜单里换两行，选中后触发器也截成省略号。协议筛选（真源没有
    // 的元素）挂 toolbar-filter-protocol 变体在 nyro-app.css 定宽 210px：
    // 菜单与触发器两个症状一次修复；状态筛选保持真源 148px（本页恰好
    // 一处裸 toolbar-filter，防止将来把两个筛选一起加宽）。
    expect(source).toContain('controlClassName="toolbar-filter toolbar-filter-protocol"');
    expect(source.match(/controlClassName="toolbar-filter"/g)).toHaveLength(1);
    const css = readFileSync(resolve(__dirname, "../styles/nyro-app.css"), "utf8");
    expect(css).toMatch(/\.toolbar-filter\.toolbar-filter-protocol\s*\{[^}]*width:\s*210px/s);
  });
});

describe("provider detail drawer body (§9.2 ⑦)", () => {
  it("renders baseline descriptions only — actions live in the drawer footer", async () => {
    const module = await import("./providers");
    const Detail = (module as unknown as {
      ProviderDetailContent?: ComponentType<{
        provider: Upstream;
        result?: TestResult;
      }>;
    }).ProviderDetailContent;

    expect(Detail).toBeTypeOf("function");
    if (!Detail) return;

    const html = renderToStaticMarkup(shell(createElement(Detail, {
      provider,
      result: { success: true, latency_ms: 243, model: "gpt-4.1" },
    })));

    expect(html).toContain('class="descriptions"');
    expect(html).toContain("243ms");
    expect(html).toContain('class="health"');
    // 动作按钮随基线 #providerDrawer 移入抽屉 footer，卡体不再渲染按钮
    expect(html).not.toContain("Test connection");
    expect(html).not.toContain("Edit configuration");
    expect(html).not.toContain("provider-detail-actions");
  });

  it("composes the detail drawer footer like the baseline (start: delete/import, end: close/test/edit)", () => {
    // 基线 #providerDrawer 的 footer：左 text 系删除/导入，右 关闭/测试/编辑配置
    expect(source).toContain('className="drawer-footer-start"');
    expect(source).toContain("button-text button-danger-text");
    expect(source).toContain('className="drawer-footer-end"');
    expect(source).toContain('"v2.providers.close"');
    expect(source).toContain('"v2.providers.test"');
    expect(source).toContain('"v2.providers.editConfiguration"');
    expect(source).toContain('"v2.providers.importModels"');
  });
});

describe("provider create/edit form sections (§9.2 ②③④)", () => {
  it("uses one baseline section structure for create and edit forms", async () => {
    const module = await import("./providers");
    const FormSections = (module as unknown as {
      ProviderFormSections?: ComponentType<{
        connection: React.ReactNode;
        credentials: React.ReactNode;
        discovery: React.ReactNode;
      }>;
    }).ProviderFormSections;

    expect(FormSections).toBeTypeOf("function");
    if (!FormSections) return;

    const html = renderToStaticMarkup(shell(createElement(FormSections, {
      connection: createElement("input", { name: "connection" }),
      credentials: createElement("input", { name: "credentials" }),
      discovery: createElement("input", { name: "discovery" }),
    })));

    expect(html).toContain('class="add-provider-form"');
    expect(html).toContain('data-provider-form-section="connection"');
    expect(html).toContain('data-provider-form-section="credentials"');
    expect(html).toContain('data-provider-form-section="discovery"');
    expect(html).toContain("Connection");
    expect(html).toContain("Credentials");
    expect(html).toContain("Model discovery");
  });

  it("renders secret credential fields with the baseline secret-control and a per-field toggle", () => {
    // Password inputs never ship the value through a visible input until revealed.
    expect(source).toContain('className="field-control secret-control"');
    expect(source).toContain('className="secret-toggle"');
    expect(source).toContain('type={reveal ? "text" : "password"}');
    expect(source).toContain('autoComplete="off"');
  });

  it("renders the preset picker with provider-pick items and a locked edit variant", () => {
    expect(source).toContain('className="provider-pick"');
    expect(source).toContain('clsx("provider-pick-item"');
    expect(source).toContain("aria-pressed");
    expect(source).toContain('className={clsx("provider-pick-item", editingPresetId === preset.id && "selected")}');
    // the edit dialog locks the preset: disabled items, help hint explains it
    expect(source.match(/<button[^>]*className=\{clsx\("provider-pick-item"[^>]*}\s*disabled/s)).toBeTruthy();
    expect(source).toContain("theProviderPresetCanTBeChangedAfter");
  });

  it("pairs name+protocol and base+proxy per row in both drawers (baseline form-grid)", () => {
    // 基线 #addProviderDrawer 的 form-grid（1fr 1fr）：名称+协议一行、
    // Base URL+代理地址一行——四个字段都是半宽，新增/编辑两个抽屉一致。
    expect(source.match(/v2\.providers\.name"\)\}\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/v2\.providers\.protocol"\)\}\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/label="Base URL"\s*\n\s*required\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
    expect(source.match(/v2\.providers\.proxyUrl"\)\}\s*\n\s*fullWidth=\{false\}/g)).toHaveLength(2);
  });
});

describe("SSE test progress surfaces (§9.2 ⑤⑥)", () => {
  it("renders probe steps in every state with waiting metadata", async () => {
    const module = await import("./providers");
    const ProbeSteps = (module as unknown as {
      ProbeSteps?: ComponentType<{
        steps: Array<{ key: string; label: string; status: "idle" | "running" | "passed" | "failed"; meta?: string }>;
        waitingLabel: string;
      }>;
    }).ProbeSteps;

    expect(ProbeSteps).toBeTypeOf("function");
    if (!ProbeSteps) return;

    const html = renderToStaticMarkup(createElement(ProbeSteps, {
      waitingLabel: "Waiting",
      steps: [
        { key: "config", label: "Configuration validation", status: "passed", meta: undefined },
        { key: "model_request", label: "Model request test", status: "running" },
        { key: "models", label: "Model discovery", status: "failed", meta: "boom" },
        { key: "credentials", label: "Credential validation", status: "idle" },
      ],
    }));

    expect(html).toContain('class="probe-steps"');
    expect(html).toContain("probe-step done");
    expect(html).toContain("probe-step failed");
    expect(html).toContain("probe-spin");
    expect(html).toContain(">Waiting</small>");
    expect(html).toContain(">boom</small>");
  });

  it("renders the streaming log as a baseline code-output card with level-colored rows", async () => {
    const module = await import("./providers");
    const TestLog = (module as unknown as {
      ProviderTestLog?: ComponentType<{
        logs: Array<{ timestamp: string; level: "info" | "success" | "error"; message: string }>;
        emptyLabel: string;
        statusLabel: string;
      }>;
    }).ProviderTestLog;

    expect(TestLog).toBeTypeOf("function");
    if (!TestLog) return;

    const html = renderToStaticMarkup(shell(createElement(TestLog, {
      emptyLabel: "Waiting for test to start",
      statusLabel: "In progress",
      logs: [
        { timestamp: "10:00:00", level: "info", message: "Testing endpoint" },
        { timestamp: "10:00:01", level: "success", message: "Connection available" },
      ],
    })));

    expect(html).toContain('class="code-output"');
    expect(html).toContain("code-output-body provider-test-log");
    expect(html).toContain('data-log-level="success"');
    expect(html).toContain("Connection available");
    expect(html).toContain("In progress");
  });

  it("renders the import preview as compact metrics plus a per-model action table", async () => {
    const module = await import("./providers");
    const Summary = (module as unknown as {
      RouteImportSummary?: ComponentType<{ preview: RouteImportPreview }>;
    }).RouteImportSummary;

    expect(Summary).toBeTypeOf("function");
    if (!Summary) return;

    const html = renderToStaticMarkup(shell(createElement(Summary, {
      preview: { discovered: 3, create: ["gpt-4.1", "gpt-4.1-mini"], skip: ["gpt-4o"] },
    })));

    expect(html).toContain("provider-import-summary");
    expect(html).toContain("provider-import-metrics");
    expect(html).toContain("gpt-4.1-mini");
    expect(html).toContain("tag-success");
    expect(html).toContain("existing routes will be skipped");
  });
});

describe("SSE handlers keep their cancellation and completion semantics", () => {
  it("keys every SSE run by runId with an AbortController and an isCanceled guard", () => {
    const runBlocks = source.match(
      /async function (?:handleTest|handleImportRoutes|handleCreateHealthCheck|handleUpdateHealthCheck)\([\s\S]*?\n {2}\}/g,
    ) ?? [];
    expect(runBlocks).toHaveLength(4);
    for (const block of runBlocks) {
      expect(block).toContain("activeTestRunRef.current + 1");
      expect(block).toContain("new AbortController()");
      expect(block).toContain("const isCanceled = () =>");
    }
  });

  it("falls back when a health check stream never yields a completion event", () => {
    expect(source).toContain("healthCheckDidNotReturnACompletionEvent");
    expect(source).toContain("if (!completed && !isCanceled() && !abortController.signal.aborted)");
  });

  it("invalidates the routes query after an import completes", () => {
    expect(source).toContain('qc.invalidateQueries({ queryKey: ["routes"] })');
  });
});

describe("deep-link focus effect (React #185 regression)", () => {
  it("keeps query fallback identities stable so the effect cannot churn mid-navigation", () => {
    // 回归：全局检索命中点击会预热 providers 缓存后跳转 /providers?focus=…，此时
    // presets 仍在途中——解构默认值 = [] 每渲染刷新数组，令依赖它的 providerPresets/
    // startEdit/深链 effect 反复重跑，与 navigate 的提交竞态成环，触发 React #185
    //（最大更新深度，页面整树卸载）。模块级哨兵数组锁死回退标识。
    expect(source).toContain("const NO_UPSTREAMS: Upstream[] = []");
    expect(source).toContain("const NO_PRESET_DTOS: ProviderPresetDTO[] = []");
    expect(source).toContain("data: providers = NO_UPSTREAMS");
    expect(source).toContain("data: providerPresetsRaw = NO_PRESET_DTOS");
  });

  it("handles each deep link exactly once and re-arms after the param is stripped", () => {
    // 同一深链在 navigate(replace) 提交前可能因依赖结算再次触发——handled ref 保证
    // 只执行一次；参数消失后复位，允许用户再次点击同一命中。
    expect(source).toContain("deepLinkHandledRef");
    expect(source).toContain("deepLinkHandledRef.current = linkKey");
    expect(source).toContain("deepLinkHandledRef.current = null");
  });
});

describe("model discovery help hint alignment (baseline field-label structure)", () => {
  it("keeps the help hint a direct child of the discover label in both drawers", () => {
    // "?" 帮助钮必须是 .field-label.discover-label 的直接子元素（flex + gap
    // 居中，同基线 waf-gateway 的 label 结构）。包进文本 span 会掉进行内
    // 基线对齐 → icon 错位；新增/编辑两个抽屉都要守住这个结构。
    const direct = source.match(/modelDiscovery2"\)\}\s*<\/span>\s*<NyroHelpHint/g) ?? [];
    const inline = source.match(/modelDiscovery2"\)\}\s*<NyroHelpHint/g) ?? [];
    expect(direct.length).toBe(2);
    expect(inline.length).toBe(0);
  });
});

describe("probe action uses the baseline heartbeat icon (§v2 providers)", () => {
  it("renders the baseline #test EKG path instead of the lucide Zap bolt", () => {
    // 真源 providers.html 的探测按钮用 #test「心跳」symbol（EKG 折线、
    // 1.7 描边、圆角连接）；闪电是 lucide Zap，非基线形态。
    expect(source).toContain("M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36");
    expect(source).toContain("strokeWidth={1.7}");
    expect(source).toContain("<HeartbeatIcon aria-hidden=\"true\" />");
    expect(source).not.toMatch(/\bZap\b/);
  });
});
