import { describe, expect, it } from "vitest";

import { appendModelTags, normalizeModelTags, parseModelTags, removeModelTagAt } from "./model-tags";

describe("model tags", () => {
  it("splits comma and line separated values while preserving model identifiers", () => {
    expect(parseModelTags(" gpt-4o,claude-sonnet-4-6\ngemini-2.5-pro\r\nopenrouter/free ")).toEqual([
      "gpt-4o",
      "claude-sonnet-4-6",
      "gemini-2.5-pro",
      "openrouter/free",
    ]);
  });

  it("removes empty values and exact duplicates without changing first-seen order", () => {
    expect(normalizeModelTags([" gpt-4o ", "", "gpt-4o", "GPT-4O", "  "])).toEqual([
      "gpt-4o",
      "GPT-4O",
    ]);
  });

  it("adds pasted models without duplicating or reordering existing tags", () => {
    expect(appendModelTags(["gpt-4o", "claude-sonnet-4-6"], parseModelTags("gpt-4o,gemini-2.5-pro"))).toEqual([
      "gpt-4o",
      "claude-sonnet-4-6",
      "gemini-2.5-pro",
    ]);
  });

  it("removes a tag at the requested index", () => {
    expect(removeModelTagAt(["gpt-4o", "claude-sonnet-4-6", "gemini-2.5-pro"], 1)).toEqual([
      "gpt-4o",
      "gemini-2.5-pro",
    ]);
  });
});
