import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const source = readFileSync(resolve(__dirname, "providers.tsx"), "utf8");

describe("create provider health gate", () => {
  it("runs draft health streaming before creating a provider", () => {
    expect(source).toContain("streamProviderDraftHealth(input,");
    expect(source).toContain("setTestDialogMode(\"create\")");
    expect(source).toContain("setPendingCreateInput(input)");
    expect(source).toContain("setCreateHealthPassed(event.success === true)");
    expect(source).toContain("createMut.mutate(pendingCreateInput)");
  });

  it("does not auto-test the saved provider after create succeeds", () => {
    expect(source).not.toContain("await handleTest(createdProvider);");
    expect(source).toContain("onSuccess: () => {");
    expect(source).toContain("closeCreateForm();");
  });
});

describe("provider list health check", () => {
  it("uses the same streaming health pipeline as create", () => {
    expect(source).toContain("streamProviderHealth(provider.id,");
    expect(source).toContain("setTestDialogMode(\"provider\")");
    expect(source).toContain("appendHealthEvent(event)");
    expect(source).not.toContain("backend<TestResult>(\"test_provider\"");
  });
});
