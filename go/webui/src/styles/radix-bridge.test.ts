import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

/* Radix 把 Overlay 与 Content 渲染为兄弟节点，而基线 HTML 里 modal 是
   modal-mask 的子元素——基线的 flex 居中与层叠上下文都来自那个父子结构。
   兄弟形态的浮层必须自带定位与层级（radix-bridge.css），否则会落进文档流
   并被遮罩盖住。静态渲染测试看不见布局，2026-09 的活体浏览器验收发现过
   一次，在此锁死。 */
const bridge = readFileSync(resolve(__dirname, "radix-bridge.css"), "utf8");

describe("radix sibling overlays (live-verified)", () => {
  it("centers every Radix-mounted modal over its mask", () => {
    expect(bridge).toContain(".modal-mask[data-state] ~ .modal");
    expect(bridge).toMatch(/\.modal-mask\[data-state\] ~ \.modal\s*\{[^}]*position:\s*fixed/s);
    expect(bridge).toMatch(/\.modal-mask\[data-state\] ~ \.modal\s*\{[^}]*transform:\s*translate\(-50%, -50%\)/s);
  });

  it("stacks the over-drawer variant above drawers", () => {
    expect(bridge).toMatch(/\.modal-mask\.over-drawer\[data-state\] ~ \.modal\s*\{[^}]*z-index:\s*40/s);
  });

  it("keeps centering inside the entry/exit animations", () => {
    // 关键帧若不带居中变换，动画期间浮层会跳到错误位置
    expect(bridge).toMatch(/@keyframes modal-in-centered\s*\{[^@]*translate\(-50%, calc\(-50% \+ 8px\)\)/s);
  });

  it("no longer uses the never-matching descendant selectors", () => {
    // Overlay/Content 是兄弟：后代选择器 .modal-mask .modal 永远选不中
    expect(bridge).not.toMatch(/\.modal-mask\[data-state[^\]]*\]\s+\.modal\b/);
  });
});
