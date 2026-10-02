import { describe, expect, test } from "bun:test";
import type { ModelTierUpgradeNote, ModelTierUse } from "@dude/domain";
import {
  effortsWords,
  proxyKnows,
  testResultWords,
  tierDraftProblems,
  tierInput,
  tierMark,
  tierUsedByWords,
  tierUseName,
  tierUseWhere,
  upgradeLines,
} from "../src/tiers.ts";

const org = (role: string, effort: string | null = null, inherited = false): ModelTierUse =>
  ({ kind: "organization", role, project: null, effort, ...(inherited ? { inherited } : {}) });
const abs = { id: "prj_abs", name: "abs", imageUrl: null };
const proj = (role: string, effort: string | null = null): ModelTierUse => ({ kind: "project", role, project: abs, effort });

describe("who uses a tier", () => {
  test("in words: agents, then projects", () => {
    expect(tierUsedByWords([org("investigator"), org("reviewer"), org("simplifier"), org("qa_browser")])).toBe("4 agents");
    expect(tierUsedByWords([org("implementer"), org("fixer", null, true), proj("reviewer")])).toBe("2 agents · 1 project");
    expect(tierUsedByWords([proj("reviewer"), proj("implementer")])).toBe("1 project");
    expect(tierUsedByWords([])).toBe("Nobody");
  });

  test("each use by name, and where it is set", () => {
    expect(tierUseName(org("qa_browser"))).toBe("Tester");
    expect(tierUseName(proj("reviewer"))).toBe("abs · Reviewer");
    expect(tierUseWhere(org("reviewer"), "Acme")).toBe("Acme’s setting");
    expect(tierUseWhere(proj("reviewer"), "Acme")).toBe("project override");
    expect(tierUseWhere(org("fixer", null, true), "Acme")).toBe("follows the implementer");
  });

  test("the efforts its agents use, distinct", () => {
    expect(effortsWords([org("implementer", "high"), org("fixer", "high", true)])).toBe("high");
    expect(effortsWords([org("implementer", "high"), proj("reviewer", "low"), org("simplifier")])).toBe("high and low and the model’s default");
    expect(effortsWords([])).toBeNull();
    expect(effortsWords([org("implementer"), org("fixer", null, true)])).toBeNull();
  });
});

describe("a tier's mark", () => {
  test("the seeded tiers have their own; any other the sparkle", () => {
    expect(tierMark({ name: "Thinker", position: 0 })).toEqual({ icon: "brain", tone: "info" });
    expect(tierMark({ name: "coder", position: 1 })).toEqual({ icon: "agent", tone: "success" });
    expect(tierMark({ name: "Fast", position: 2 })).toEqual({ icon: "zap", tone: "attention" });
    expect(tierMark({ name: "gpt-5.6-sol", position: 3 })).toEqual({ icon: "sparkle", tone: "neutral" });
  });
});

describe("a tier dialog's draft", () => {
  test("an empty model is not set; names are trimmed", () => {
    expect(tierInput({ name: " Cheap ", description: " Bulk. ", model: "  " })).toEqual({ name: "Cheap", description: "Bulk.", model: null });
    expect(tierInput({ name: "Cheap", description: "", model: " gpt-5.6-luna " })).toEqual({ name: "Cheap", description: "", model: "gpt-5.6-luna" });
  });

  test("each field's problem in the schema's words", () => {
    expect(tierDraftProblems({ name: "Cheap", description: "", model: "gpt-5.6-luna" })).toEqual({});
    expect(tierDraftProblems({ name: "", description: "x".repeat(81), model: "llm-openai/gpt" })).toEqual({
      name: "A tier needs a name",
      description: "At most 80 characters",
      model: "The model as the proxy names it: no spaces or slashes, at most 200 characters",
    });
  });

  test("whether the proxy lists a name; unknown while its list is unread", () => {
    expect(proxyKnows("gpt-5.6-sol", ["claude-opus-5-5", "gpt-5.6-sol"])).toBe(true);
    expect(proxyKnows(" gpt-5.6-sol ", ["gpt-5.6-sol"])).toBe(true);
    expect(proxyKnows("gemini-3.8-pro", ["gpt-5.6-sol"])).toBe(false);
    expect(proxyKnows("gemini-3.8-pro", null)).toBeNull();
  });
});

describe("a test message's result", () => {
  test("its latency, or the proxy's status and words as they came", () => {
    expect(testResultWords({ efforts: ["high"], sent: "high", ok: true, latencyMs: 1234, status: 200, error: null })).toBe("answered (effort high) in 1.2 s");
    expect(testResultWords({ efforts: [null], sent: null, ok: true, latencyMs: 800, status: 200, error: null })).toBe("answered with no effort in 0.8 s");
    expect(testResultWords({ efforts: ["low"], sent: "low", ok: false, latencyMs: 12, status: 404, error: "model 'x' is not served" }))
      .toBe("(effort low): the proxy answered 404 — model 'x' is not served");
    expect(testResultWords({ efforts: [null], sent: null, ok: false, latencyMs: 30000, status: null, error: "no answer from the LLM proxy in 30s" }))
      .toBe("with no effort: no answer from the LLM proxy in 30s");
  });

  test("one request for every effort that goes out alike; a Claude model's says it went without one", () => {
    expect(testResultWords({ efforts: ["high", "max"], sent: "high", ok: true, latencyMs: 1200, status: 200, error: null }))
      .toBe("answered (efforts high, max) in 1.2 s");
    expect(testResultWords({ efforts: ["high", null], sent: null, ok: true, latencyMs: 900, status: 200, error: null }))
      .toBe("answered (efforts high, none; sent without an effort, as the agent sends it) in 0.9 s");
  });
});

describe("the upgrade's notes", () => {
  const note = (over: Partial<ModelTierUpgradeNote>): ModelTierUpgradeNote =>
    ({ id: 1, role: "reviewer", project: null, oldModel: "llm-anthropic/claude-fable-5-1", tierId: "t", tierName: "Thinker", newTier: false, modelChanged: false, ...over });

  test("the organisation's roles group by the model they named and the tier they ask for; a project's stand alone", () => {
    const lines = upgradeLines([
      note({ id: 1, role: "investigator" }),
      note({ id: 2, role: "reviewer" }),
      note({ id: 3, role: "simplifier", oldModel: "llm-anthropic/claude-sonnet-5-5", modelChanged: true }),
      note({ id: 4, role: "qa_browser", oldModel: "llm-anthropic/claude-sonnet-5-5", modelChanged: true }),
      note({ id: 5, role: "reviewer", project: abs, oldModel: "llm-openai/gpt-5.6-sol", tierName: "gpt-5.6-sol", newTier: true }),
    ]);
    expect(lines.map((l) => [l.roles, l.oldModel, l.who, l.tierName, l.newTier, l.modelChanged])).toEqual([
      [["investigator", "reviewer"], "llm-anthropic/claude-fable-5-1", null, "Thinker", false, false],
      [["simplifier", "qa_browser"], "llm-anthropic/claude-sonnet-5-5", null, "Thinker", false, true],
      [["reviewer"], "llm-openai/gpt-5.6-sol", "abs · Reviewer", "gpt-5.6-sol", true, false],
    ]);
  });
});
