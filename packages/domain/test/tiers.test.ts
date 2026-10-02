import { describe, expect, test } from "bun:test";
import {
  isTierModel,
  modelTierInputSchema,
  replaceTier,
  resolveTier,
  testModelSchema,
  TIER_MODEL_MESSAGE,
} from "../src/tiers.ts";

const tier = (over: Record<string, unknown> = {}) => modelTierInputSchema.safeParse({ name: "Coder", model: "claude-opus-5-5", ...over });
const errorOf = (r: ReturnType<typeof tier>) => (r.success ? null : r.error.issues[0]!.message);

describe("a tier's model", () => {
  test("is the proxy's name for it, as given", () => {
    for (const m of ["claude-opus-5-5", "gpt-5.6-sol", "gemini-3.8-pro", "new-model:latest", "x".repeat(200)]) {
      expect(isTierModel(m)).toBe(true);
    }
  });

  test("has no provider prefix, space or slash, and is 1 to 200 characters", () => {
    for (const m of ["llm-anthropic/claude-opus-5-5", "a b", " gpt", "gpt\n", "", "x".repeat(201), "fake/unknown"]) {
      expect(isTierModel(m)).toBe(false);
    }
    expect(errorOf(tier({ model: "llm-openai/gpt-5.6-sol" }))).toBe(TIER_MODEL_MESSAGE);
  });

  test("the scripted agent's test models stay accepted", () => {
    for (const m of ["fake/scripted", "fake/hang", "fake/tools", "fake/request", "fake/wait", "fake/live", "fake/ask"]) {
      expect(isTierModel(m)).toBe(true);
    }
  });

  test("may be unset", () => {
    const r = tier({ model: null });
    expect(r.success && r.data.model).toBeNull();
    expect(modelTierInputSchema.parse({ name: "Fast" })).toEqual({ name: "Fast", description: "", model: null });
  });
});

describe("a tier's name and description", () => {
  test("a name is 1 to 24 characters, trimmed", () => {
    expect(tier({ name: " Coder " }).success && tier({ name: " Coder " }).data?.name).toBe("Coder");
    expect(errorOf(tier({ name: "  " }))).toBe("A tier needs a name");
    expect(tier({ name: "x".repeat(24) }).success).toBe(true);
    expect(errorOf(tier({ name: "x".repeat(25) }))).toBe("At most 24 characters");
  });

  test("a description is at most 80 characters", () => {
    expect(tier({ description: "x".repeat(80) }).success).toBe(true);
    expect(errorOf(tier({ description: "x".repeat(81) }))).toBe("At most 80 characters");
  });

  test("nothing else is taken", () => {
    expect(tier({ position: 3 }).success).toBe(false);
  });
});

describe("a test message", () => {
  test("names a model and the efforts to try, none by default", () => {
    expect(testModelSchema.parse({ model: "gpt-5.6-sol" })).toEqual({ model: "gpt-5.6-sol", efforts: [] });
    expect(testModelSchema.safeParse({ model: "gpt-5.6-sol", efforts: ["extreme"] }).success).toBe(false);
  });
});

describe("resolving a role's tier", () => {
  const tiers = [{ id: "thinker" }, { id: "coder" }, { id: "fast" }];

  test("the project's, then the organization's", () => {
    expect(resolveTier("reviewer", { project: { reviewer: { tier: "fast" } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: "fast", from: "project" });
    expect(resolveTier("reviewer", { project: { reviewer: { effort: "low" } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: "thinker", from: "organization" });
  });

  test("the fixer follows the implementer over both layers, unless it names its own", () => {
    const org = { implementer: { tier: "coder" } };
    expect(resolveTier("fixer", { organization: org }, tiers)).toEqual({ tierId: "coder", from: "implementer" });
    expect(resolveTier("fixer", { project: { implementer: { tier: "fast" } }, organization: org }, tiers))
      .toEqual({ tierId: "fast", from: "implementer" });
    expect(resolveTier("fixer", { project: { implementer: { tier: "fast" } }, organization: { ...org, fixer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: "thinker", from: "organization" });
  });

  test("an id that names no tier is passed over; none at all is null", () => {
    expect(resolveTier("reviewer", { project: { reviewer: { tier: "gone" } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: "thinker", from: "organization" });
    expect(resolveTier("reviewer", { organization: {} }, tiers)).toEqual({ tierId: null, from: null });
  });
});

describe("moving what named a tier", () => {
  test("every role naming it, its other fields kept", () => {
    const models = { reviewer: { tier: "fast", effort: "low" as const }, implementer: { tier: "coder" } };
    expect(replaceTier(models, "fast", "thinker")).toEqual({ reviewer: { tier: "thinker", effort: "low" }, implementer: { tier: "coder" } });
  });

  test("nothing naming it is the same object", () => {
    const models = { implementer: { tier: "coder" } };
    expect(replaceTier(models, "fast", "thinker")).toBe(models);
  });
});
