import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { DashboardWorkspace } from "./dashboard-workspace";

describe("dashboard workspace", () => {
  it("keeps all four operational modules in one ordered grid boundary", () => {
    const html = renderToStaticMarkup(
      createElement(
        DashboardWorkspace,
        null,
        createElement("article", { className: "v2-dashboard-trend" }, "Request trend"),
        createElement("aside", { className: "v2-health-panel" }, "Upstream health"),
        createElement("article", { className: "v2-route-performance" }, "Model performance"),
        createElement("aside", { className: "v2-activity-panel" }, "Runtime activity"),
      ),
    );

    expect(html).toMatch(/^<section class="v2-dashboard-workspace">/);
    expect(html.indexOf("Request trend")).toBeLessThan(html.indexOf("Upstream health"));
    expect(html.indexOf("Upstream health")).toBeLessThan(html.indexOf("Model performance"));
    expect(html.indexOf("Model performance")).toBeLessThan(html.indexOf("Runtime activity"));
    expect(html).toMatch(/Runtime activity<\/aside><\/section>$/);
  });
});
