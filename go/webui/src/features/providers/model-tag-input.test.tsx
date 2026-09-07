import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { ModelTagInput } from "./model-tag-input";

describe("ModelTagInput", () => {
  it("renders models as removable tags with an accessible input", () => {
    const html = renderToStaticMarkup(createElement(ModelTagInput, {
      value: ["gpt-4o", "claude-sonnet-4-6"],
      onChange: () => undefined,
      inputLabel: "Add a manual model",
      listLabel: "Manual model list",
      placeholder: "Enter a model ID",
      helpText: "Press Enter or comma to add a model.",
      removeLabel: (model) => `Remove model ${model}`,
    }));

    expect(html).toContain("gpt-4o");
    expect(html).toContain("claude-sonnet-4-6");
    expect(html).toContain('aria-label="Manual model list"');
    expect(html).toContain('aria-label="Add a manual model"');
    expect(html).toContain('aria-label="Remove model gpt-4o"');
    expect(html).toContain('type="button"');
    expect(html).toContain('spellcheck="false"');
  });
});
