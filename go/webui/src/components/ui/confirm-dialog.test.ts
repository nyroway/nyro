import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "confirm-dialog.tsx"), "utf8");
const providersSource = readFileSync(resolve(__dirname, "../../pages/providers.tsx"), "utf8");

describe("ConfirmDialog confirm button variant", () => {
  it("lets confirmClassName replace (not stack on) the default danger variant", () => {
    // Regression: the old form `button button-danger${confirmClassName ? ...}`
    // stacked danger and the passed-in class on the same button; with two
    // background classes, whichever comes later in the stylesheet wins — when
    // the import-models dialog passed button-primary the confirm button stayed
    // red. Changed to full-replacement semantics.
    expect(source).toContain('className={`button ${confirmClassName ?? "button-danger"}`}');
    expect(source).not.toMatch(/button-danger\$\{confirmClassName/);
  });

  it("keeps danger as the default for destructive confirmations", () => {
    expect(source).toContain("confirmClassName?: string");
    expect(source).toContain('?? "button-danger"');
  });

  it("renders the import-models confirm as primary blue via confirmClassName", () => {
    // User expectation: confirm import = blue, same color as the add-provider button (button-primary).
    expect(providersSource).toContain('confirmClassName="button-primary"');
  });
});
