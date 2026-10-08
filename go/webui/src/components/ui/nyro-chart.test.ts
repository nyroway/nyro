import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { NyroChart, buildTicks, chartTooltipLayout, monotonePath } from "./nyro-chart";

const source = readFileSync(resolve(__dirname, "nyro-chart.tsx"), "utf8");

/* NyroChart 是 waf-gateway.html 754–880 行图表算法的 TS 化（§7.4 / D5）：
   几何走纯函数（可精确断言），组件静态渲染只验支架——路径绘制依赖
   ResizeObserver 量得的容器尺寸，仅浏览器环境发生。 */

describe("monotonePath (Fritsch–Carlson monotone cubic)", () => {
  it("interpolates a straight segment with its own tangent", () => {
    expect(monotonePath([{ x: 0, y: 0 }, { x: 10, y: 10 }])).toBe("M0 0 C3.3 3.3 6.7 6.7 10 10");
  });

  it("flattens the tangent at a direction change so the curve cannot overshoot", () => {
    // direction change at (10,10): incoming tangent 1, outgoing tangent 0 →
    // the zero tangent flattens both the segment's second control point and the next one
    expect(monotonePath([{ x: 0, y: 0 }, { x: 10, y: 10 }, { x: 20, y: 10 }]))
      .toBe("M0 0 C3.3 3.3 6.7 10 10 10 C13.3 10 16.7 10 20 10");
  });

  it("returns an empty path for fewer than two points", () => {
    expect(monotonePath([{ x: 0, y: 0 }])).toBe("");
    expect(monotonePath([])).toBe("");
  });
});

describe("buildTicks", () => {
  it("picks a nice step and descends to zero", () => {
    expect(buildTicks(7).map((tick) => tick.value)).toEqual([8, 6, 4, 2, 0]);
    expect(buildTicks(100).map((tick) => tick.value)).toEqual([100, 50, 0]);
  });

  it("always covers the series maximum with its top tick", () => {
    for (const max of [1, 7, 99, 1234, 9999]) {
      const ticks = buildTicks(max);
      expect(ticks[0].value).toBeGreaterThanOrEqual(max);
      expect(ticks[ticks.length - 1].value).toBe(0);
    }
  });

  it("formats tick labels compactly and handles an all-zero series", () => {
    expect(buildTicks(1000).map((tick) => tick.label)).toEqual(["1K", "500", "0"]);
    expect(buildTicks(0)).toEqual([{ label: "0", value: 0 }]);
  });
});

describe("chartTooltipLayout (edge-aware tooltip anchoring)", () => {
  // 真源几何：.chart-wrap { padding: 16px 20px 12px 12px }，.chart 高 218px →
  // wrap ≈ 600×246；弹层 min-width 128px、三行文案 ≈ 72px 高。
  const wrap = { width: 600, height: 246, padL: 12, padT: 16 };
  const tip = { w: 128, h: 72 };

  it("keeps the baseline anchoring (above, centered) for mid-chart points", () => {
    expect(chartTooltipLayout({ x: 288, y: 120 }, wrap, tip)).toEqual({
      left: 300, // padL + point.x
      top: 136, // padT + point.y
      transform: "translate(-50%, -110%)",
    });
  });

  it("slides the tooltip inward when the point hugs the left edge", () => {
    // 稀疏数据只剩两点时首点贴 plotL=46：居中会让左半出卡片 → 钳到 margin + w/2
    const layout = chartTooltipLayout({ x: 46, y: 120 }, wrap, tip);
    expect(layout.left).toBe(8 + 64);
    expect(layout.left - tip.w / 2).toBeGreaterThanOrEqual(8 - 0.5);
    expect(layout.transform).toBe("translate(-50%, -110%)");
  });

  it("slides the tooltip inward when the point hugs the right edge", () => {
    const layout = chartTooltipLayout({ x: 586, y: 120 }, wrap, tip); // plotR = 600-14
    expect(layout.left).toBe(600 - 8 - 64);
    expect(layout.left + tip.w / 2).toBeLessThanOrEqual(wrap.width - 8 + 0.5);
  });

  it("flips below the point when there is no room above", () => {
    // 点贴近绘图区顶部（plotT=14）：上方放不下 -110% 的弹层 → 翻到点下方
    const layout = chartTooltipLayout({ x: 288, y: 14 }, wrap, tip);
    expect(layout.transform).toBe("translate(-50%, 14px)");
    expect(layout.top).toBe(30); // padT + 14，下方空间充足无需上移
    expect(layout.top + 14 + tip.h).toBeLessThanOrEqual(wrap.height - 8 + 0.5);
  });

  it("keeps the box inside the wrap across the whole plot plane", () => {
    // 全平面扫描：任何点位的弹层四条边都落在容器（margin 8px）内
    for (let x = 0; x <= 600; x += 37) {
      for (let y = 0; y <= 246; y += 23) {
        const { left, top, transform } = chartTooltipLayout({ x, y }, wrap, tip);
        const boxTop = transform === "translate(-50%, -110%)" ? top - tip.h * 1.1 : top + 14;
        expect(left - tip.w / 2).toBeGreaterThanOrEqual(8 - 0.5);
        expect(left + tip.w / 2).toBeLessThanOrEqual(wrap.width - 8 + 0.5);
        expect(boxTop).toBeGreaterThanOrEqual(8 - 0.5);
        expect(boxTop + tip.h).toBeLessThanOrEqual(wrap.height - 8 + 0.5);
      }
    }
  });

  it("centers the tooltip when it is wider than the wrap", () => {
    expect(chartTooltipLayout({ x: 46, y: 120 }, wrap, { w: 700, h: 72 }).left).toBe(300);
  });
});

describe("NyroChart static render", () => {
  it("exposes the baseline chart scaffold with an accessible name", () => {
    const html = renderToStaticMarkup(createElement(NyroChart, {
      data: [
        { label: "10:00", value: 120, error: 2 },
        { label: "11:00", value: 180, error: 0 },
        { label: "12:00", value: 90, error: 5 },
      ],
      ariaLabel: "Hourly requests and errors",
      valueLabel: "Requests",
      errorLabel: "Errors",
    }));

    expect(html).toContain('class="chart-wrap"');
    expect(html).toContain('class="chart-tooltip"');
    expect(html).toContain('class="chart"');
    expect(html).toContain('role="img"');
    expect(html).toContain('aria-label="Hourly requests and errors"');
  });

  it("wires the tooltip to the measured edge-aware layout in pixels", () => {
    // 回归：基线 demo 的 % 定位（按 SVG 宽度算、解析在 wrap padding box 上，
    // 自带几像素偏差且贴边必被 .card overflow:hidden 裁掉）已换成像素锚定。
    expect(source).toContain("chartTooltipLayout(");
    expect(source).not.toContain("* 100}%");
    // 实测容器/弹层尺寸而非硬编码真源 padding
    expect(source).toContain("getBoundingClientRect()");
    expect(source).toContain("offsetWidth");
    expect(source).toContain("offsetHeight");
  });
});
