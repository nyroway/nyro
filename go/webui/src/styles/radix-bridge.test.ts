import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

/* Radix renders Overlay and Content as sibling nodes, while in the baseline HTML the modal is
   a child of modal-mask — the baseline's flex centering and stacking context both come from that parent-child structure.
   A sibling-form popup must carry its own positioning and stacking level (radix-bridge.css), or it falls into the document flow
   and gets covered by the mask. Static render tests can't see layout; the 2026-09 live browser acceptance caught
   this once — locked down here. */
const bridge = readFileSync(resolve(__dirname, "radix-bridge.css"), "utf8");

describe("radix sibling overlays (live-verified)", () => {
  it("centers every Radix-mounted modal over its mask", () => {
    expect(bridge).toContain(".modal-mask[data-state] ~ .modal");
    expect(bridge).toMatch(/\.modal-mask\[data-state\] ~ \.modal\s*\{[^}]*position:\s*fixed/s);
    expect(bridge).toMatch(/\.modal-mask\[data-state\] ~ \.modal\s*\{[^}]*transform:\s*translate\(-50%, -50%\)/s);
  });

  it("stacks the over-drawer variant above drawers", () => {
    expect(bridge).toMatch(/\.modal-mask\.over-drawer\[data-state\] ~ \.modal\s*\{[^}]*z-index:\s*40/s);
  });

  it("keeps centering inside the entry/exit animations", () => {
    // Without the centering transform in the keyframes, the popup jumps to the wrong position mid-animation
    expect(bridge).toMatch(/@keyframes modal-in-centered\s*\{[^@]*translate\(-50%, calc\(-50% \+ 8px\)\)/s);
  });

  it("no longer uses the never-matching descendant selectors", () => {
    // Overlay/Content are siblings: the descendant selector .modal-mask .modal can never match
    expect(bridge).not.toMatch(/\.modal-mask\[data-state[^\]]*\]\s+\.modal\b/);
  });
});
