import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import * as DialogPrimitive from "@radix-ui/react-dialog";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

import { ResourceEditorDrawerFrame, ResourceEditorFrame } from "./resource-editor-dialog";

/* Two popup states: edit/add forms = right-side drawer (baseline #addProviderDrawer/#createDrawer),
   probe/one-time tokens = centered modal over-drawer (baseline #probeModal). */

describe("resource editor dialog frame (modal)", () => {
  it("keeps the title, scrolling body, and actions in separate regions", () => {
    const html = renderToStaticMarkup(
      createElement(
        DialogPrimitive.Root,
        { open: true },
        createElement(
          ResourceEditorFrame,
          {
            title: "Test provider",
            description: "OpenAI Production",
            onClose: () => undefined,
            footer: createElement("button", null, "Close"),
            children: createElement("label", null, "Name", createElement("input")),
          },
        ),
      ),
    );

    expect(html).toContain("modal-header");
    expect(html).toContain("modal-body");
    expect(html).toContain("modal-footer");
    expect(html).toContain("Test provider");
    expect(html).toContain("OpenAI Production");
    expect(html).toContain("Close");
    expect(html).toContain('aria-label="Close"');
  });

  it("omits the footer region when the editor has no actions", () => {
    const html = renderToStaticMarkup(
      createElement(
        DialogPrimitive.Root,
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

    expect(html).not.toContain("modal-footer");
  });
});

describe("resource editor drawer frame (edit/create)", () => {
  it("renders the drawer skeleton with the actions right-aligned", () => {
    const html = renderToStaticMarkup(
      createElement(
        DialogPrimitive.Root,
        { open: true },
        createElement(
          ResourceEditorDrawerFrame,
          {
            title: "Add provider",
            description: "Configure connection credentials",
            onClose: () => undefined,
            footer: createElement(
              "div",
              null,
              createElement("button", { className: "button button-secondary" }, "Cancel"),
              createElement("button", { className: "button button-primary" }, "Save"),
            ),
            children: createElement("label", null, "Base URL", createElement("input")),
          },
        ),
      ),
    );

    expect(html).toContain("drawer-header");
    expect(html).toContain("drawer-header-copy");
    expect(html).toContain("drawer-title");
    expect(html).toContain("drawer-body");
    expect(html).toContain("drawer-footer");
    expect(html).toContain("drawer-footer-end");
    expect(html).toContain("Add provider");
    expect(html).toContain("Configure connection credentials");
    expect(html).toContain("Save");
    expect(html).toContain('aria-label="Close"');
    // The drawer skeleton must not contain modal vocabulary
    expect(html).not.toContain("modal-header");
    expect(html).not.toContain("modal-body");
    expect(html).not.toContain("modal-footer");
  });

  it("omits the footer region when the drawer has no actions", () => {
    const html = renderToStaticMarkup(
      createElement(
        DialogPrimitive.Root,
        { open: true },
        createElement(
          ResourceEditorDrawerFrame,
          {
            title: "Add resource",
            onClose: () => undefined,
            children: createElement("p", null, "Form"),
          },
        ),
      ),
    );

    expect(html).not.toContain("drawer-footer");
  });
});

describe("resource editor drawer dialog (source)", () => {
  const source = readFileSync(resolve(__dirname, "resource-editor-dialog.tsx"), "utf8");

  it("mounts the editor form as a right-side drawer with the wide variant", () => {
    expect(source).toContain('DialogPrimitive.Overlay className="drawer-mask"');
    expect(source).toContain('`drawer drawer-wide ${className}`');
    expect(source).not.toContain("modal-form-wide");
  });

  it("keeps the probe/result modal stackable over the drawer", () => {
    expect(source).toContain('DialogPrimitive.Overlay className="modal-mask over-drawer"');
    expect(source).toContain('`modal modal-form ${className}`');
  });
});
