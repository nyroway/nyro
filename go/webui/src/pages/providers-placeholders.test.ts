import { describe, expect, it } from "vitest";
import { translate, type MessageKey } from "@/lib/messages";

describe("provider form placeholders", () => {
  it("uses concise Chinese and English example prefixes consistently", () => {
    const expected: Array<[MessageKey, string, string]> = [
      ["v2.providers.eGOpenaiProduction", "e.g. OpenAI Production", "如：OpenAI 生产环境"],
      ["v2.providers.eGSk", "e.g. sk-...", "如：sk-..."],
      ["v2.providers.eGHttpsApiOpenaiComV1", "e.g. https://api.openai.com/v1", "如：https://api.openai.com/v1"],
      ["v2.providers.eGHttp1270017890", "e.g. http://127.0.0.1:7890", "如：http://127.0.0.1:7890"],
      ["v2.providers.eGHttpsApiOpenaiComV1Models", "e.g. https://api.openai.com/v1/models", "如：https://api.openai.com/v1/models"],
      ["v2.providers.oneModelPerLineEGGpt4o", "One model per line, e.g. gpt-4o", "每行一个模型名，如：gpt-4o"],
    ];

    for (const [key, english, chinese] of expected) {
      expect(translate("en-US", key)).toBe(english);
      expect(translate("zh-CN", key)).toBe(chinese);
    }
  });
});
