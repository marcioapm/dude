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
    for (const m of ["fake/scripted", "fake/hang", "fake/tools", "fake/request", "fake/wait", "fake/live", "fake/ask", "fake/lookup"]) {
      expect(isTierModel(m)).toBe(true);
    }
  });

  test("may be unset", () => {
    const r = tier({ model: null });
    expect(r.success && r.data.model).toBeNull();
    expect(modelTierInputSchema.parse({ name: "Fast" })).toEqual({ name: "Fast", description: "", model: null, effort: null, options: null, headers: null });
  });
});

describe("a tier's effort, options and headers", () => {
  test("an effort is none, low, medium, high or max; null is the model's default", () => {
    for (const e of ["none", "low", "medium", "high", "max", null]) expect(tier({ effort: e }).success).toBe(true);
    for (const e of ["xhigh", "minimal", "", "High"]) expect(tier({ effort: e }).success).toBe(false);
  });

  test("options are a JSON object of at most 4 KB as Postgres renders it", () => {
    expect(tier({ options: { effort: "xhigh", thinking: { display: "omitted" } } }).success).toBe(true);
    for (const o of [[], "x", 3]) expect(tier({ options: o }).success).toBe(false);
    // {"k": "…"}: 9 bytes around the value as jsonb text, 2 fewer than JSON.stringify's.
    expect(tier({ options: { k: "x".repeat(4096 - 9) } }).success).toBe(true);
    expect(errorOf(tier({ options: { k: "x".repeat(4096 - 8) } }))).toBe("At most 4096 bytes as JSON");
  });

  test("headers are token names with one-line string values, at most 4 KB", () => {
    expect(tier({ headers: { "X-Team": "dude", "anthropic-beta": "context-1m" } }).success).toBe(true);
    expect(errorOf(tier({ headers: { "X Team": "dude" } }))).toBe("Header names are letters, digits and !#$%&'*+.^_`|~-");
    expect(errorOf(tier({ headers: { "X-Team": "a\r\nInjected: 1" } }))).toBe("A header's value is one line");
    expect(errorOf(tier({ headers: { "X-Team": "a\u007fb" } }))).toBe("A header's value holds no DEL character");
    expect(tier({ headers: { "X-Team": 3 } }).success).toBe(false);
    expect(errorOf(tier({ headers: { h: "x".repeat(4096) } }))).toBe("At most 4096 bytes as JSON");
  });

  test("no string in options or headers, key or value, holds a NUL, which jsonb cannot store", () => {
    const nul = "No NUL characters (\\u0000)";
    expect(errorOf(tier({ options: { a: "\u0000" } }))).toBe(nul);
    expect(errorOf(tier({ options: { deep: [{ "k\u0000": 1 }] } }))).toBe(nul);
    expect(errorOf(tier({ headers: { "X-Team": "a\u0000b" } }))).toBe(nul);
    const test = testModelSchema.safeParse({ model: "gpt-6-sol", options: { a: "\u0000" } });
    expect(test.success ? null : test.error.issues[0]!.message).toBe(nul);
  });

  test("a backslash followed by u0000 is six characters, not a NUL, and is kept", () => {
    expect(tier({ options: { stop: "\\u0000" } }).success).toBe(true);
    expect(tier({ options: { "\\u0000": 1 } }).success).toBe(true);
    expect(tier({ headers: { "X-A": "\\u0000" } }).success).toBe(true);
    expect(testModelSchema.safeParse({ model: "gpt-6-sol", options: { re: "\\u0000" } }).success).toBe(true);
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
  test("names a model and the tier's settings as the dialog has them", () => {
    expect(testModelSchema.parse({ model: "gpt-5.6-sol" })).toEqual({ model: "gpt-5.6-sol", effort: null, options: null, headers: null });
    expect(testModelSchema.parse({ model: "gpt-6-sol", effort: "none" }).effort).toBe("none");
    expect(testModelSchema.safeParse({ model: "llm-openai/gpt-5.6-sol" }).success).toBe(false);
    expect(testModelSchema.safeParse({ model: "gpt-5.6-sol", tierId: "mtr_x" }).success).toBe(false);
  });
});

describe("resolving a role's tier", () => {
  const tiers = [{ id: "thinker" }, { id: "coder" }, { id: "fast" }];

  test("the project's, then the organization's", () => {
    expect(resolveTier("reviewer", { project: { reviewer: { tier: "fast" } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: "fast", from: "project" });
    expect(resolveTier("reviewer", { project: { reviewer: { timeLimitMinutes: 45 } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
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

  test("the first layer naming one decides; an id that names no tier is none; none at all is null", () => {
    expect(resolveTier("reviewer", { project: { reviewer: { tier: "gone" } }, organization: { reviewer: { tier: "thinker" } } }, tiers))
      .toEqual({ tierId: null, from: "project" });
    expect(resolveTier("reviewer", { organization: {} }, tiers)).toEqual({ tierId: null, from: null });
  });
});

describe("moving what named a tier", () => {
  test("every role naming it, its other fields kept", () => {
    const models = { reviewer: { tier: "fast", timeLimitMinutes: 45 }, implementer: { tier: "coder" } };
    expect(replaceTier(models, "fast", "thinker")).toEqual({ reviewer: { tier: "thinker", timeLimitMinutes: 45 }, implementer: { tier: "coder" } });
  });

  test("nothing naming it is the same object", () => {
    const models = { implementer: { tier: "coder" } };
    expect(replaceTier(models, "fast", "thinker")).toBe(models);
  });
});
