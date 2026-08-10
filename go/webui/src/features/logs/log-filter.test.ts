import { describe, expect, it } from "vitest";

import { applyLogStatusFilter, logStatusFilterValue } from "./log-filter";

describe("log status filters", () => {
  it("maps query bounds to the matching control value", () => {
    expect(logStatusFilterValue({ status_min: 200, status_max: 299 })).toBe("ok");
    expect(logStatusFilterValue({ status_min: 400 })).toBe("error");
    expect(logStatusFilterValue({})).toBe("all");
  });

  it("updates only status bounds", () => {
    expect(applyLogStatusFilter({ consumer_id: "consumer-a", status_min: 400 }, "ok")).toEqual({
      consumer_id: "consumer-a",
      status_min: 200,
      status_max: 299,
    });
    expect(applyLogStatusFilter({ route_id: "route-a", status_min: 400 }, "all")).toEqual({
      route_id: "route-a",
      status_min: undefined,
      status_max: undefined,
    });
  });
});
