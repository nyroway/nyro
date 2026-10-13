/* Placement geometry for the baseline hover-tip fixed layer (pure functions, directly coverable by unit tests):
   horizontal = centered above the icon, clamped inward into the viewport on overflow; vertical = floats above the icon by default,
   flipping below the icon when it does not fit above. Lives in lib rather than a component file — a module that exports components
   may only export components (react-refresh/only-export-components), otherwise hot reload
   degrades to a full-page refresh. */
export function helpTipLayout(
  icon: { left: number; top: number; right: number; bottom: number },
  tip: { w: number; h: number },
  viewport: { w: number; h: number },
  margin = 8,
  gap = 6,
): { left: number; top: number } {
  // Horizontal: centered above the icon, clamped inward into the viewport on overflow (when the popup is wider than the viewport it hugs the left safe margin)
  const left = Math.min(
    Math.max((icon.left + icon.right) / 2 - tip.w / 2, margin),
    Math.max(margin, viewport.w - tip.w - margin),
  );
  // Vertical: floats above the icon leaving a gap by default; flips below the icon when it does not fit above
  const above = icon.top - tip.h - gap;
  return { left, top: above >= margin ? above : icon.bottom + gap };
}
