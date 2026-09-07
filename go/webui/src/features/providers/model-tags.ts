const MODEL_TAG_SEPARATORS = /[\n,]/;

export function normalizeModelTags(tags: readonly string[]): string[] {
  const seen = new Set<string>();
  const normalized: string[] = [];

  for (const tag of tags) {
    const value = tag.trim();
    if (!value || seen.has(value)) continue;
    seen.add(value);
    normalized.push(value);
  }

  return normalized;
}

export function parseModelTags(value: string): string[] {
  return normalizeModelTags(value.split(MODEL_TAG_SEPARATORS));
}

export function appendModelTags(current: readonly string[], incoming: readonly string[]): string[] {
  return normalizeModelTags([...current, ...incoming]);
}

export function removeModelTagAt(current: readonly string[], index: number): string[] {
  return current.filter((_, currentIndex) => currentIndex !== index);
}
