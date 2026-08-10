import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";

import { Dialog } from "@/components/ui/dialog";
import { ResourceEditorFrame } from "./resource-editor-dialog";

describe("resource editor dialog frame", () => {
  it("keeps the title, scrolling body, and actions in separate regions", () => {
    const html = renderToStaticMarkup(
      createElement(
        Dialog,
        { open: true },
        createElement(
          ResourceEditorFrame,
          {
            title: "Edit provider",
            description: "OpenAI Production",
            onClose: () => undefined,
            footer: createElement("button", null, "Save"),
            children: createElement("label", null, "Name", createElement("input")),
          },
        ),
      ),
    );

    expect(html).toContain("v2-resource-dialog-head");
    expect(html).toContain("v2-resource-dialog-body");
    expect(html).toContain("v2-resource-dialog-foot");
    expect(html).toContain("Edit provider");
    expect(html).toContain("OpenAI Production");
    expect(html).toContain("Save");
    expect(html).toContain('aria-label="Close"');
  });

  it("omits the footer region when the editor has no actions", () => {
    const html = renderToStaticMarkup(
      createElement(
        Dialog,
        { open: true },
        createElement(
          ResourceEditorFrame,
          {
            title: "View resource",
            onClose: () => undefined,
            children: createElement("p", null, "Details"),
          },
        ),
      ),
    );

    expect(html).not.toContain("v2-resource-dialog-foot");
  });
});
