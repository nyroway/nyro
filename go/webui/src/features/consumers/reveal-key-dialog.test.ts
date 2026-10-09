import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { afterEach, describe, expect, it, vi } from "vitest";

import { LocaleProvider } from "@/lib/i18n";
import { RevealKeyDialogBody } from "./reveal-key-dialog";

const TOKEN = "nyro-live-token-abc123";

/* §9.4 注意：raw token 仅创建响应可见。这里静态渲染对话框 body，锁定
   “密码形态遮罩 + alert-warning 仅此一次警示 + 内联复制”的呈现，
   以及 token 不以其他明文形态泄漏到 DOM。 */

function renderBody() {
  vi.stubGlobal("window", {
    localStorage: {
      getItem: () => null,
      setItem: () => undefined,
    },
  });
  return renderToStaticMarkup(createElement(
    LocaleProvider,
    null,
    createElement(RevealKeyDialogBody, { revealed: { name: "default", token: TOKEN } }),
  ));
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("RevealKeyDialogBody", () => {
  it("masks the one-time token behind a password input inside a secret-control", () => {
    const html = renderBody();

    expect(html).toContain("secret-control");
    expect(html).toContain('type="password"');
    expect(html).toContain('readOnly');
  });

  it("carries the token exactly once, as the masked input's value", () => {
    const html = renderBody();

    expect(html).toContain(`value="${TOKEN}"`);
    expect(html.split(TOKEN).length - 1).toBe(1);
  });

  it("warns that the full key is shown only once and cannot be viewed again", () => {
    const html = renderBody();

    expect(html).toContain('class="alert alert-warning"');
    expect(html).toContain("shown only once");
    expect(html).toContain("cannot be viewed again");
  });

  it("offers reveal and copy toggles on the secret control", () => {
    const html = renderBody();

    expect(html).toContain('aria-label="Show key"');
    expect(html).toContain('aria-label="Copy"');
  });
});
