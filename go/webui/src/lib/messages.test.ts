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
    expect(translate("en-US", "nodes.currentVersion", { version: "rev-7" })).toBe(
      "Current version rev-7",
    );
    expect(translate("zh-CN", "nodes.currentVersion", { version: "rev-7" })).toBe(
      "当前版本 rev-7",
    );
  });
});
