import { describe, expect, it } from "vitest";
import { messageCatalogs, resolveInitialLocale, translate } from "./messages";

describe("message catalogs", () => {
  it("keeps Chinese coverage aligned with the canonical English catalog", () => {
    expect(Object.keys(messageCatalogs["zh-CN"]).sort()).toEqual(
      Object.keys(messageCatalogs["en-US"]).sort(),
    );
  });

  it("defaults to English unless the user saved a supported locale", () => {
    expect(resolveInitialLocale(null)).toBe("en-US");
    expect(resolveInitialLocale("fr-FR")).toBe("en-US");
    expect(resolveInitialLocale("zh-CN")).toBe("zh-CN");
  });

  it("interpolates named values without changing untranslated enum values", () => {
    expect(translate("en-US", "nodes.uptimeHint", { host: "web-1" })).toBe(
      "Longest connection: web-1",
    );
    expect(translate("zh-CN", "nodes.uptimeHint", { host: "web-1" })).toBe(
      "最长连接：web-1",
    );
  });
});
