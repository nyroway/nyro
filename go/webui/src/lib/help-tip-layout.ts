/* 真源 hover-tip 固定层的落点几何（纯函数，供单测直接覆盖）：
   水平＝图标上方居中、越界向内钳进视口；垂直＝默认浮在图标上方，
   上方放不下翻到图标下方。放在 lib 而非组件文件——导出组件的模块
   只能导出组件（react-refresh/only-export-components），否则热更新
   会退回整页刷新。 */
export function helpTipLayout(
  icon: { left: number; top: number; right: number; bottom: number },
  tip: { w: number; h: number },
  viewport: { w: number; h: number },
  margin = 8,
  gap = 6,
): { left: number; top: number } {
  // 水平：图标上方居中，越界向内钳进视口（弹层宽过视口时贴左安全界）
  const left = Math.min(
    Math.max((icon.left + icon.right) / 2 - tip.w / 2, margin),
    Math.max(margin, viewport.w - tip.w - margin),
  );
  // 垂直：默认浮在图标上方留 gap；上方放不下翻到图标下方
  const above = icon.top - tip.h - gap;
  return { left, top: above >= margin ? above : icon.bottom + gap };
}
