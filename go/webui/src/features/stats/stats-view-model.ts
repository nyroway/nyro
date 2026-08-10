export function errorRate(errors: number, requests: number): number {
  if (requests <= 0) return 0;
  return Math.max(0, Math.min(100, (errors / requests) * 100));
}

export function rankedShare<T extends { value: number }>(items: readonly T[], limit: number): Array<T & { share: number }> {
  const ranked = [...items].sort((left, right) => right.value - left.value).slice(0, limit);
  const total = ranked.reduce((sum, item) => sum + item.value, 0);
  return ranked.map((item) => ({ ...item, share: total > 0 ? (item.value / total) * 100 : 0 }));
}
