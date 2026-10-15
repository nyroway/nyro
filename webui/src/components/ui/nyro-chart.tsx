/* eslint-disable react-refresh/only-export-components */
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";

/* Under SSR (renderToStaticMarkup tests) useLayoutEffect only spams a
   server-side warning; the "before paint" semantics are only needed in the
   browser — a module-level conditional alias, not a per-render conditional
   call. */
const useIsomorphicLayoutEffect = typeof document === "undefined" ? useEffect : useLayoutEffect;

/* NyroChart: TS-ification of the baseline chart algorithm from waf-gateway.html
   lines 754–880 (§7.4). Shape aligns with the A+B spec — gradient stops use
   var(--ink-strong) (visible in both light and dark themes); the SVG is drawn
   1:1 at the container's measured size (ResizeObserver re-render, no
   stretching). Shared by dashboard and stats: data + value/error dual lines +
   tooltip crosshair snapping. Popup placement is a React adaptation-layer
   enhancement (chartTooltipLayout): the baseline anchors the tooltip centered
   above the hover point, but .card overflow:hidden clips the popup at
   edge-hugging points — this is edge-aware, sliding with a horizontal clamp
   and flipping below the point when there is no room above. */

export type NyroChartDatum = {
  label: string;
  value: number;
  error?: number;
};

export type NyroChartProps = {
  data: NyroChartDatum[];
  ariaLabel: string;
  valueLabel: string;
  errorLabel?: string;
  formatValue?: (value: number) => string;
};

type ChartTick = { label: string; value: number };

const compactTick = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

/** Monotone cubic interpolation (Fritsch–Carlson tangent limiting, no overshoot at bends). */
export function monotonePath(pts: Array<{ x: number; y: number }>): string {
  if (pts.length < 2) return "";
  const n = pts.length;
  const dx: number[] = [];
  const m: number[] = [];
  for (let i = 0; i < n - 1; i++) {
    dx[i] = pts[i + 1].x - pts[i].x;
    m[i] = (pts[i + 1].y - pts[i].y) / dx[i];
  }
  const t: number[] = [m[0]];
  for (let i = 1; i < n - 1; i++) {
    if (m[i - 1] * m[i] <= 0) {
      t[i] = 0;
    } else {
      const w1 = 2 * dx[i] + dx[i - 1];
      const w2 = dx[i] + 2 * dx[i - 1];
      t[i] = (w1 + w2) / (w1 / m[i - 1] + w2 / m[i]);
    }
  }
  t[n - 1] = m[n - 2];
  let d = `M${pts[0].x} ${pts[0].y}`;
  const r = (value: number) => Math.round(value * 10) / 10;
  for (let i = 0; i < n - 1; i++) {
    const h = dx[i] / 3;
    d += ` C${r(pts[i].x + h)} ${r(pts[i].y + t[i] * h)}`
      + ` ${r(pts[i + 1].x - h)} ${r(pts[i + 1].y - t[i + 1] * h)}`
      + ` ${pts[i + 1].x} ${pts[i + 1].y}`;
  }
  return d;
}

/** Generates neat Y-axis ticks from the data peak; the top tick is the plot's upper bound. */
export function buildTicks(maxValue: number): ChartTick[] {
  if (maxValue <= 0) return [{ label: "0", value: 0 }];
  const raw = maxValue / 4;
  const magnitude = 10 ** Math.floor(Math.log10(raw));
  const normalized = raw / magnitude;
  const step = (normalized <= 1 ? 1 : normalized <= 2 ? 2 : normalized <= 5 ? 5 : 10) * magnitude;
  const top = Math.max(step, Math.ceil(maxValue / step) * step);
  const ticks: ChartTick[] = [];
  for (let value = top; value >= 0; value -= step) {
    ticks.push({ label: compactTick.format(value), value });
  }
  return ticks;
}

/* ── Edge-aware popup placement (React adaptation layer, §7.4)──────────────────────
   The baseline anchors the tooltip centered above the hover point (the
   baseline .chart-tooltip's translate(-50%, -110%)), but .card overflow:hidden
   would clip part of the popup at edge-hugging points (points at the far
   left/right, or near the top of the plot area). This computes absolute
   placement from the measured container and popup sizes: horizontally the
   anchor is clamped into the safe range (sliding inward at the edges, while
   the crosshair still marks the data point exactly); vertically, when there is
   no room above, it flips to 14px below the point. The outputs left/top are
   pixel offsets relative to the .chart-wrap padding box. */
export function chartTooltipLayout(
  point: { x: number; y: number },
  wrap: { width: number; height: number; padL: number; padT: number },
  tip: { w: number; h: number },
  margin = 8,
): { left: number; top: number; transform: string } {
  // Convert the data point (SVG coordinates) into the wrap's padding box
  const px = wrap.padL + point.x;
  const py = wrap.padT + point.y;
  // Horizontal: centered anchoring, clamped inward when out of bounds; when the clamp range is empty (popup wider than the container), center it entirely
  const minLeft = margin + tip.w / 2;
  const maxLeft = wrap.width - margin - tip.w / 2;
  const left = minLeft <= maxLeft
    ? Math.min(Math.max(px, minLeft), maxLeft)
    : wrap.width / 2;
  // Vertical: floats above by default (baseline -110%, leaving a 10%-height
  // gap); the bottom edge is clamped first, and when the top edge does not fit
  // it flips below the point, guaranteed to stay inside the bottom edge.
  const aboveTop = Math.min(py, wrap.height - margin + tip.h * 0.1);
  if (aboveTop - tip.h * 1.1 >= margin) {
    return { left, top: aboveTop, transform: "translate(-50%, -110%)" };
  }
  const belowTop = Math.max(margin, Math.min(py, wrap.height - margin - 14 - tip.h));
  return { left, top: belowTop, transform: "translate(-50%, 14px)" };
}

export function NyroChart({ data, ariaLabel, valueLabel, errorLabel, formatValue }: NyroChartProps) {
  const gradientId = `chartArea-${useId().replace(/[^a-zA-Z0-9]/g, "")}`;
  const svgRef = useRef<SVGSVGElement | null>(null);
  const wrapRef = useRef<HTMLDivElement | null>(null);
  const tooltipRef = useRef<HTMLDivElement | null>(null);
  const [size, setSize] = useState<{ width: number; height: number } | null>(null);
  // The wrap's padding box size + the SVG's offset within it: the coordinate
  // system for popup placement (baseline .chart-wrap { padding: 16px 20px 12px
  // 12px }, measured rather than hardcoded).
  const [wrapBox, setWrapBox] = useState<{ width: number; height: number; padL: number; padT: number } | null>(null);
  // The popup's actual size varies with copy/language (CSS min-width is only a floor); re-measured after the hover point changes.
  const [tipSize, setTipSize] = useState<{ w: number; h: number } | null>(null);
  const [hoverIndex, setHoverIndex] = useState<number | null>(null);
  const format = formatValue ?? ((value: number) => value.toLocaleString("en-US"));

  useEffect(() => {
    const svg = svgRef.current;
    if (!svg || typeof ResizeObserver === "undefined") return;
    const measure = () => {
      const box = svg.getBoundingClientRect();
      setSize({ width: Math.max(320, Math.round(box.width)), height: Math.max(160, Math.round(box.height)) });
      const wrap = wrapRef.current;
      if (wrap) {
        const wrapRect = wrap.getBoundingClientRect();
        setWrapBox({
          width: Math.round(wrapRect.width),
          height: Math.round(wrapRect.height),
          padL: Math.round(box.left - wrapRect.left),
          padT: Math.round(box.top - wrapRect.top),
        });
      }
    };
    measure();
    const observer = new ResizeObserver(measure);
    observer.observe(svg);
    return () => observer.disconnect();
  }, []);

  // The popup content changes with the hover point (label/value); the size is
  // measured synchronously in a layout effect — the placement is corrected
  // before paint, so no uncorrected position flashes. Nothing is measured for
  // an empty hover (the content is empty).
  useIsomorphicLayoutEffect(() => {
    const el = tooltipRef.current;
    if (!el || hoverIndex == null) return;
    const w = el.offsetWidth;
    const h = el.offsetHeight;
    setTipSize((current) => (current && current.w === w && current.h === h ? current : { w, h }));
  }, [hoverIndex]);

  const chart = useMemo(() => {
    if (!size || data.length < 2) return null;
    const { width, height } = size;
    const plotL = 46;
    const plotR = width - 14;
    const plotT = 14;
    const plotB = height - 26;
    const plotH = plotB - plotT;
    const ticks = buildTicks(Math.max(...data.map((datum) => datum.value)));
    const valueMax = Math.max(ticks[0].value, 1);
    const hasError = errorLabel != null && data.some((datum) => (datum.error ?? 0) > 0);
    const errMax = Math.max(1, ...data.map((datum) => datum.error ?? 0));
    const errTop = plotT + plotH * 0.62;
    const errBottom = plotB - 8;
    const step = (plotR - plotL) / (data.length - 1);
    const round = (value: number) => Math.round(value * 10) / 10;
    const points = data.map((datum, i) => ({
      x: round(plotL + step * i),
      y: round(plotB - (datum.value / valueMax) * plotH),
      ey: round(errBottom - ((datum.error ?? 0) / errMax) * (errBottom - errTop)),
      datum,
    }));
    const linePath = monotonePath(points);
    const errPath = hasError ? monotonePath(points.map((point) => ({ x: point.x, y: point.ey }))) : "";
    return {
      width,
      height,
      plotL,
      plotR,
      plotT,
      plotB,
      plotH,
      ticks,
      valueMax,
      hasError,
      points,
      linePath,
      errPath,
      areaPath: `${linePath} L${plotR} ${plotB} L${plotL} ${plotB} Z`,
    };
  }, [size, data, errorLabel]);

  const hovered = hoverIndex != null ? chart?.points[hoverIndex] : undefined;
  // Pixel-exact anchoring (replacing the baseline demo's % positioning — the %
  // is computed against the SVG width yet resolves on the wrap's padding box,
  // which is already off by a few pixels); chartTooltipLayout clamps/flips at
  // the edges.
  const tooltipLayout = hovered && chart
    ? chartTooltipLayout(
        { x: hovered.x, y: hovered.y },
        wrapBox ?? { width: chart.width, height: chart.height, padL: 0, padT: 0 },
        tipSize ?? { w: 128, h: 72 },
      )
    : null;

  return (
    <div className="chart-wrap" ref={wrapRef}>
      <div
        ref={tooltipRef}
        className="chart-tooltip"
        style={tooltipLayout
          ? {
            left: `${tooltipLayout.left}px`,
            top: `${tooltipLayout.top}px`,
            transform: tooltipLayout.transform,
            opacity: 1,
          }
          : undefined}
      >
        {hovered && (
          <>
            {hovered.datum.label}
            <strong>{format(hovered.datum.value)} {valueLabel}</strong>
            {errorLabel != null && (
              <span className="chart-tooltip-err">{hovered.datum.error ?? 0} {errorLabel}</span>
            )}
          </>
        )}
      </div>
      <svg
        ref={svgRef}
        className="chart"
        role="img"
        aria-label={ariaLabel}
        viewBox={chart ? `0 0 ${chart.width} ${chart.height}` : undefined}
        onMouseMove={chart ? (event: React.MouseEvent<SVGSVGElement>) => {
          const box = event.currentTarget.getBoundingClientRect();
          const x = Math.min(Math.max(event.clientX - box.left, chart.plotL), chart.plotR);
          let nearest = 0;
          for (let i = 1; i < chart.points.length; i++) {
            if (Math.abs(chart.points[i].x - x) < Math.abs(chart.points[nearest].x - x)) nearest = i;
          }
          setHoverIndex(nearest);
        } : undefined}
        onMouseLeave={() => setHoverIndex(null)}
      >
        {chart && (
          <>
            <defs>
              <linearGradient id={gradientId} x1="0" x2="0" y1="0" y2="1">
                <stop offset="0" style={{ stopColor: "var(--ink-strong)", stopOpacity: 0.12 }} />
                <stop offset=".55" style={{ stopColor: "var(--ink-strong)", stopOpacity: 0.05 }} />
                <stop offset="1" style={{ stopColor: "var(--ink-strong)", stopOpacity: 0 }} />
              </linearGradient>
            </defs>
            {chart.ticks.map((tick) => {
              const y = Math.round((chart.plotB - (tick.value / chart.valueMax) * chart.plotH) * 10) / 10;
              return (
                <g key={tick.value}>
                  <line className="chart-grid" x1={chart.plotL} y1={y} x2={chart.plotR} y2={y} />
                  <text className="chart-label" x={chart.plotL - 8} y={y + 4} textAnchor="end">{tick.label}</text>
                </g>
              );
            })}
            <path className="chart-area" d={chart.areaPath} style={{ fill: `url(#${gradientId})` }} />
            {chart.hasError && <path className="chart-line error" d={chart.errPath} />}
            <path className="chart-line" d={chart.linePath} />
            {[0, 1, 2, 3, 4].map((k) => {
              const i = Math.round(((chart.points.length - 1) * k) / 4);
              const anchor = i === 0 ? "start" : i === chart.points.length - 1 ? "end" : "middle";
              const x = i === 0 ? chart.plotL - 2 : i === chart.points.length - 1 ? chart.plotR + 2 : chart.points[i].x;
              return (
                <text className="chart-label" key={k} x={x} y={chart.height - 8} textAnchor={anchor}>
                  {chart.points[i].datum.label}
                </text>
              );
            })}
            {hovered && (
              <>
                <line
                  className="chart-cross"
                  x1={hovered.x}
                  y1={chart.plotT}
                  x2={hovered.x}
                  y2={chart.plotB}
                  style={{ opacity: 1 }}
                />
                <circle className="chart-point-ring" cx={hovered.x} cy={hovered.y} r={6.5} />
                <circle className="chart-point" cx={hovered.x} cy={hovered.y} r={3.5} />
              </>
            )}
          </>
        )}
      </svg>
    </div>
  );
}
