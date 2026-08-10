import { describe, expect, it } from "vitest";

import { errorRate, rankedShare } from "./stats-view-model";

describe("statistics view model", () => {
  it("calculates a bounded error rate", () => {
    expect(errorRate(25, 100)).toBe(25);
    expect(errorRate(3, 0)).toBe(0);
    expect(errorRate(120, 100)).toBe(100);
  });

  it("ranks values and calculates shares against the selected total", () => {
    expect(rankedShare([
      { id: "small", value: 2 },
      { id: "large", value: 8 },
      { id: "zero", value: 0 },
    ], 2)).toEqual([
      { id: "large", value: 8, share: 80 },
      { id: "small", value: 2, share: 20 },
    ]);
  });
});
