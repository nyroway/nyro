import { createElement } from "react";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { DataTable } from "./data-table";
import { PageHeader } from "./page-header";
import { PageLayout } from "./page-layout";
import { RowActionMenu } from "./row-action-menu";

describe("layout primitives", () => {
  it("keeps table structure visible when rows are empty", () => {
    const html = renderToStaticMarkup(
      createElement(DataTable<{ name: string }>, {
        columns: [
          {
            key: "name",
            header: "Name",
            render: (row: { name: string }) => row.name,
          },
        ],
        rows: [],
        rowKey: (row: { name: string }) => row.name,
        empty: "No providers",
      }),
    );

    expect(html).toContain("<table");
    expect(html).toContain("<th>Name</th>");
    expect(html).toContain("No providers");
  });

  it("keeps the page header outside the padded content region", () => {
    const html = renderToStaticMarkup(
      createElement(PageLayout, {
        header: createElement(PageHeader, {
          title: "Providers",
          description: "Manage upstream connections",
        }),
        children: createElement("section", null, "Provider list"),
      }),
    );

    expect(html).toMatch(
      /^<div class="content"><header class="page-header">.*<\/header><div class="content-body"><section>Provider list<\/section><\/div><\/div>$/,
    );
  });
});

describe("RowActionMenu (baseline .action-menu / .row-menu)", () => {
  const menuSource = readFileSync(resolve(__dirname, "row-action-menu.tsx"), "utf8");

  it("renders the kebab trigger on data-tip with menu semantics, no native title", () => {
    // The baseline #hoverTip fixed layer reads data-tip (falling back to aria-label); title stacks a native tooltip.
    const html = renderToStaticMarkup(
      createElement(RowActionMenu, {
        label: "更多",
        children: createElement("button", { type: "button" }, "导入模型"),
      }),
    );
    expect(html).toContain('class="action-menu"');
    expect(html).toContain('data-tip="更多"');
    expect(html).toContain('aria-haspopup="menu"');
    expect(html).toContain('class="row-menu row-menu-wide"');
    expect(html).not.toContain("title=");
  });

  it("flips the panel up when it would cross the viewport bottom (toggleRowMenu)", () => {
    // Baseline toggleRowMenu: after expanding, measure the bottom edge; if it exceeds the viewport's 12px safety margin, add .flip-up.
    expect(menuSource).toContain('panel.classList.remove("flip-up")');
    expect(menuSource).toContain('panel.getBoundingClientRect()');
    expect(menuSource).toContain("box.bottom > window.innerHeight - 12");
    expect(menuSource).toContain('panel.classList.add("flip-up")');
  });
});
