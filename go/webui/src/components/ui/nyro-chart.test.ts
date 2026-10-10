import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { NyroChart, buildTicks, chartTooltipLayout, monotonePath } from "./nyro-chart";

const source = readFileSync(resolve(__dirname, "nyro-chart.tsx"), "utf8");

/* NyroChart is the TS-ification of the chart algorithm at waf-gateway.html
   lines 754–880 (§7.4 / D5): geometry goes through pure functions (exactly
   assertable), and the component's static render only verifies the scaffold —
   path drawing depends on the container size measured by ResizeObserver and
   only happens in a browser environment. */

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
  // Baseline geometry: .chart-wrap { padding: 16px 20px 12px 12px }, .chart
  // height 218px → wrap ≈ 600×246; popup min-width 128px, three lines of copy
  // ≈ 72px tall.
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
    // Sparse data reduced to two points puts the first point at plotL=46: centering would push the left half out of the card → clamp to margin + w/2
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
    // Point near the top of the plot area (plotT=14): no room above for the -110% popup → flip below the point
    const layout = chartTooltipLayout({ x: 288, y: 14 }, wrap, tip);
    expect(layout.transform).toBe("translate(-50%, 14px)");
    expect(layout.top).toBe(30); // padT + 14; ample room below, no need to move up
    expect(layout.top + 14 + tip.h).toBeLessThanOrEqual(wrap.height - 8 + 0.5);
  });

  it("keeps the box inside the wrap across the whole plot plane", () => {
    // Whole-plane scan: at any point, all four edges of the popup fall inside the container (margin 8px)
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
    // Regression: the baseline demo's % positioning (computed against the SVG
    // width, resolved on the wrap padding box — already off by a few pixels,
    // and always clipped by .card overflow:hidden at the edges) has been
    // replaced with pixel anchoring.
    expect(source).toContain("chartTooltipLayout(");
    expect(source).not.toContain("* 100}%");
    // Measure the container/popup sizes instead of hardcoding the baseline padding
    expect(source).toContain("getBoundingClientRect()");
    expect(source).toContain("offsetWidth");
    expect(source).toContain("offsetHeight");
  });
});
