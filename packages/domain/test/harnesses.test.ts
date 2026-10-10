import { describe, expect, test } from "bun:test";
import { harnessMisfit, harnessSchema, modelProvider, resolveHarness } from "../src/harnesses.ts";
import { agentModelConfigSchema, storedAgentModelsSchema } from "../src/hierarchy.ts";
import { settingsPatchSchema } from "../src/settings.ts";

describe("a role's harness", () => {
  test("is project, then organisation, then — for the fixer — the implementer's, else OpenCode", () => {
    const organization = { implementer: { harness: "codex" }, reviewer: { harness: "claude-code" } } as never;
    const project = { reviewer: { harness: "opencode" } } as never;
    expect(resolveHarness("reviewer", { project, organization })).toEqual({ harness: "opencode", from: "project" });
    expect(resolveHarness("reviewer", { organization })).toEqual({ harness: "claude-code", from: "organization" });
    expect(resolveHarness("fixer", { project, organization })).toEqual({ harness: "codex", from: "implementer" });
    expect(resolveHarness("simplifier", { project, organization })).toEqual({ harness: "opencode", from: "default" });
    // A stored value that names no harness is passed over.
    expect(resolveHarness("reviewer", { project: { reviewer: { harness: "aider" } } as never, organization })).toEqual({ harness: "claude-code", from: "organization" });
  });

  test("is one of three, in a role's settings and in a settings change", () => {
    expect(harnessSchema.options).toEqual(["opencode", "claude-code", "codex"]);
    expect(agentModelConfigSchema.safeParse({ harness: "claude-code" }).success).toBe(true);
    // A stored one that names no harness (an older or unknown value) reads as unset, the rest kept.
    expect(storedAgentModelsSchema.parse({ reviewer: { harness: "aider", tier: "mtr_1" } })).toEqual({ reviewer: { harness: undefined, tier: "mtr_1" } });
    for (const harness of ["aider", 42]) expect(agentModelConfigSchema.safeParse({ harness }).success).toBe(false);
    expect(settingsPatchSchema.safeParse({ roles: { implementer: { harness: "codex" } } }).success).toBe(true);
    expect(settingsPatchSchema.safeParse({ roles: { implementer: { harness: null } } }).success).toBe(true);
    expect(settingsPatchSchema.safeParse({ roles: { implementer: { harness: "Codex" } } }).success).toBe(false);
  });

  test("fits a model by its provider: Claude Code Anthropic's, Codex OpenAI's, OpenCode both", () => {
    expect(modelProvider("claude-sonnet-5")).toBe("anthropic");
    expect(modelProvider("gpt-6-sol")).toBe("openai");
    expect(modelProvider("my-claude")).toBe("openai");
    expect(harnessMisfit("claude-code", "claude-sonnet-5")).toBeNull();
    expect(harnessMisfit("claude-code", "gpt-6-sol")).toContain("Claude Code runs only Anthropic models");
    expect(harnessMisfit("codex", "gpt-6-sol")).toBeNull();
    expect(harnessMisfit("codex", "claude-sonnet-5")).toContain("Codex runs only OpenAI models");
    expect(harnessMisfit("opencode", "claude-sonnet-5")).toBeNull();
    expect(harnessMisfit("opencode", "gpt-6-sol")).toBeNull();
    // Nothing to check: no model yet, or the scripted agent, which plays any.
    expect(harnessMisfit("codex", null)).toBeNull();
    expect(harnessMisfit("codex", "fake/scripted")).toBeNull();
  });
});
