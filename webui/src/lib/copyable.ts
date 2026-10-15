export function copyableTipProps(value: string | null | undefined) {
  const text = value?.trim();
  if (!text) return {};
  return {
    "data-tip": text,
    "data-tip-copy": text,
  };
}
