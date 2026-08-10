import { readFileSync } from "node:fs";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Brand } from "./brand";
import { DataTable } from "./data-table";
import { PageHeader } from "./page-header";
import { PageLayout } from "./page-layout";

describe("V2 primitives", () => {
  it("renders the bundled Nyro logo", () => {
    const html = renderToStaticMarkup(createElement(Brand));
    expect(html).toContain('src="/src/assets/logos/NYRO-logo.png"');

    const png = readFileSync(
      new URL("../../assets/logos/NYRO-logo.png", import.meta.url),
    );
    expect([...png.subarray(0, 8)]).toEqual([
      137, 80, 78, 71, 13, 10, 26, 10,
    ]);
  });

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
      /^<div class="v2-page-layout"><header class="v2-page-header">.*<\/header><div class="v2-page-body"><section>Provider list<\/section><\/div><\/div>$/,
    );
  });
});
