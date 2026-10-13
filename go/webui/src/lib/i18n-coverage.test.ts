import { readdirSync, readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { messageCatalogs, type MessageKey } from "./messages";

/* §12 bilingual acceptance: bare-key rendering checks for t().
   The type system already guarantees keys exist (MessageKey + zero casts); this test adds two safety nets:
   ① the catalogs themselves: values in both language packs are non-empty, and no key was written in as its own value;
   ② call sites: every literal key in the source (t()/localizedMessage()/translate())
      really exists in both language packs — guarding against "added the key but only to one language" slips. */

const enKeys = Object.keys(messageCatalogs["en-US"]) as MessageKey[];
const zhKeys = Object.keys(messageCatalogs["zh-CN"]) as MessageKey[];
const enKeySet = new Set(enKeys);
const zhKeySet = new Set(zhKeys);

function collectSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = resolve(dir, entry.name);
    if (entry.isDirectory()) out.push(...collectSourceFiles(path));
    else if (/\.(ts|tsx)$/.test(entry.name) && !/\.test\.(ts|tsx)$/.test(entry.name)) {
      out.push(path);
    }
  }
  return out;
}

describe("message catalog sanity", () => {
  it("keeps zh-CN aligned with the canonical en-US catalog", () => {
    expect(zhKeys.sort()).toEqual(enKeys.sort());
  });

  it("never ships an empty value or a raw key as the value", () => {
    const offenders: string[] = [];
    for (const [locale, catalog] of Object.entries(messageCatalogs)) {
      for (const [key, value] of Object.entries(catalog)) {
        if (typeof value !== "string" || value.trim() === "") offenders.push(`${locale}:${key} 空`);
        if (value === key) offenders.push(`${locale}:${key} 值=键`);
      }
    }
    expect(offenders).toEqual([]);
  });
});

describe("literal key call sites (both locales)", () => {
  const sources = collectSourceFiles(resolve(__dirname, ".."))
    .map((path) => ({ path, text: readFileSync(path, "utf8") }));

  const literalPatterns = [
    /\bt\(\s*"([a-z][a-zA-Z0-9]*(?:\.[a-zA-Z0-9]+)+)"/g,
    /\blocalizedMessage\(\s*[a-zA-Z][a-zA-Z0-9]*\s*,\s*"([a-z][a-zA-Z0-9]*(?:\.[a-zA-Z0-9]+)+)"/g,
    /\btranslate\(\s*"[a-zA-Z-]+"\s*,\s*"([a-z][a-zA-Z0-9]*(?:\.[a-zA-Z0-9]+)+)"/g,
  ];

  const usedKeys = new Set<string>();
  for (const { text } of sources) {
    for (const pattern of literalPatterns) {
      for (const match of text.matchAll(pattern)) usedKeys.add(match[1]);
    }
  }

  it("finds a meaningful number of literal call sites", () => {
    // Guard against the scan regex itself going stale: the call-site count must be in the right ballpark (10 pages + the shell + the command palette).
    expect(usedKeys.size).toBeGreaterThan(200);
  });

  it("has every literal key in both catalogs", () => {
    const missing = [...usedKeys]
      .filter((key) => !enKeySet.has(key as MessageKey) || !zhKeySet.has(key as MessageKey));
    expect(missing).toEqual([]);
  });
});
