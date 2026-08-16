import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { StateURLField } from "./state-settings-card";

describe("StateURLField", () => {
  it("hides the complete Redis URL until explicitly revealed", () => {
    const props = {
      isZh: false,
      value: "redis://user:secret@redis.internal:6379/2",
      invalid: false,
      onChange: vi.fn(),
      onToggleReveal: vi.fn(),
    };

    const hidden = renderToStaticMarkup(createElement(StateURLField, { ...props, reveal: false }));
    expect(hidden).toContain('type="password"');
    expect(hidden).toContain('aria-label="Show"');

    const revealed = renderToStaticMarkup(createElement(StateURLField, { ...props, reveal: true }));
    expect(revealed).toContain('type="text"');
    expect(revealed).toContain('aria-label="Hide"');
  });
});
