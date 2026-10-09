import { describe, expect, test } from "bun:test";
import type { ModelTierUpgradeNote, ModelTierUse } from "@dude/domain";
import {
  proxyKnows,
  testResultWords,
  tierDraftOf,
  tierDraftProblems,
  tierEffortLabel,
  tierInput,
  tierMark,
  tierUsedByWords,
  tierUseName,
  tierUseWhere,
  upgradeLines,
  type TierDraft,
} from "../src/tiers.ts";

const org = (role: string, inherited = false): ModelTierUse =>
  ({ kind: "organization", role, project: null, ...(inherited ? { inherited } : {}) });
const abs = { id: "prj_abs", name: "abs", imageUrl: null };
const proj = (role: string): ModelTierUse => ({ kind: "project", role, project: abs });

describe("who uses a tier", () => {
  test("in words: agents, then projects", () => {
    expect(tierUsedByWords([org("investigator"), org("reviewer"), org("simplifier"), org("qa_browser")])).toBe("4 agents");
    expect(tierUsedByWords([org("implementer"), org("fixer", true), proj("reviewer")])).toBe("2 agents · 1 project");
    expect(tierUsedByWords([proj("reviewer"), proj("implementer")])).toBe("1 project");
    expect(tierUsedByWords([])).toBe("Nobody");
  });

  test("each use by name, and where it is set", () => {
    expect(tierUseName(org("qa_browser"))).toBe("Tester");
    expect(tierUseName(proj("reviewer"))).toBe("abs · Reviewer");
    expect(tierUseWhere(org("reviewer"), "Acme")).toBe("Acme’s setting");
    expect(tierUseWhere(proj("reviewer"), "Acme")).toBe("project override");
    expect(tierUseWhere(org("fixer", true), "Acme")).toBe("follows the implementer");
  });
});

describe("a tier's mark", () => {
  test("the seeded tiers have their own; any other the sparkle", () => {
    expect(tierMark({ name: "Thinker" })).toEqual({ icon: "brain", tone: "info" });
    expect(tierMark({ name: "coder" })).toEqual({ icon: "agent", tone: "success" });
    expect(tierMark({ name: "Fast" })).toEqual({ icon: "zap", tone: "attention" });
    expect(tierMark({ name: "gpt-5.6-sol" })).toEqual({ icon: "sparkle", tone: "neutral" });
  });
});

describe("a tier dialog's draft", () => {
  const draft = (over: Partial<TierDraft> = {}): TierDraft =>
    ({ name: "Cheap", description: "", model: "gpt-5.6-luna", effort: null, options: "", headers: "", ...over });

  test("an empty model is not set; names are trimmed", () => {
    expect(tierInput(draft({ name: " Cheap ", description: " Bulk. ", model: "  " })))
      .toEqual({ name: "Cheap", description: "Bulk.", model: null, effort: null, options: null, headers: null });
    expect(tierInput(draft({ model: " gpt-5.6-luna " })).model).toBe("gpt-5.6-luna");
  });

  test("each field's problem in the schema's words", () => {
    expect(tierDraftProblems(draft())).toEqual({});
    expect(tierDraftProblems(draft({ name: "", description: "x".repeat(81), model: "llm-openai/gpt" }))).toEqual({
      name: "A tier needs a name",
      description: "At most 80 characters",
      model: "The model as the proxy names it: no spaces or slashes, at most 200 characters",
    });
  });

  test("the effort, and the JSON fields as objects; empty is none", () => {
    const d = draft({ effort: "high", options: "{\"effort\": \"xhigh\"}", headers: " {\"X-Team\": \"dude\"} " });
    expect(tierInput(d)).toMatchObject({ effort: "high", options: { effort: "xhigh" }, headers: { "X-Team": "dude" } });
    expect(tierDraftProblems(d)).toEqual({});
    expect(tierInput(draft({ options: "  " })).options).toBeNull();
  });

  test("JSON that is not an object, or headers the schema refuses, are the field's problem", () => {
    expect(tierDraftProblems(draft({ options: "{effort: xhigh}", headers: "[1]" }))).toEqual({
      options: "A JSON object, like {\"key\": \"value\"}",
      headers: "A JSON object, like {\"key\": \"value\"}",
    });
    expect(tierDraftProblems(draft({ headers: "{\"X Team\": \"a\"}" }))).toEqual({ headers: "Header names are letters, digits and !#$%&'*+.^_`|~-" });
    expect(tierDraftProblems(draft({ headers: "{\"X-Team\": 3}" })).headers).toBeDefined();
  });

  test("a saved tier's settings as the draft shows them", () => {
    const d = tierDraftOf({ id: "t", name: "Coder", description: "", model: "claude-sonnet-5", effort: "medium",
      options: { effort: "xhigh" }, headers: null, position: 0, updatedAt: "", updatedBy: null });
    expect(d).toEqual({ name: "Coder", description: "", model: "claude-sonnet-5", effort: "medium", options: "{\n  \"effort\": \"xhigh\"\n}", headers: "" });
  });

  test("whether the proxy lists a name; unknown while its list is unread", () => {
    expect(proxyKnows("gpt-5.6-sol", ["claude-opus-5-5", "gpt-5.6-sol"])).toBe(true);
    expect(proxyKnows(" gpt-5.6-sol ", ["gpt-5.6-sol"])).toBe(true);
    expect(proxyKnows("gemini-3.8-pro", ["gpt-5.6-sol"])).toBe(false);
    expect(proxyKnows("gemini-3.8-pro", null)).toBeNull();
  });
});

describe("a tier's effort", () => {
  test("as people read it", () => {
    expect([tierEffortLabel(null), tierEffortLabel("none"), tierEffortLabel("high")]).toEqual(["Model’s default", "None", "High"]);
  });
});

describe("a test message's result", () => {
  const sent = { reasoning: { effort: "high", summary: "auto" } };
  test("its latency, or the proxy's status and words as they came, with what was sent", () => {
    expect(testResultWords({ sent, ok: true, latencyMs: 1234, status: 200, error: null }))
      .toBe("answered in 1.2 s (sent {\"reasoning\":{\"effort\":\"high\",\"summary\":\"auto\"}})");
    expect(testResultWords({ sent: {}, ok: false, latencyMs: 12, status: 404, error: "model 'x' is not served" }))
      .toBe("the proxy answered 404 — model 'x' is not served");
    expect(testResultWords({ sent: {}, ok: false, latencyMs: 30000, status: null, error: "no answer from the LLM proxy in 30s" }))
      .toBe("no answer from the LLM proxy in 30s");
  });

  test("a refusal inside a 200 stream is the proxy's words", () => {
    expect(testResultWords({ sent: { reasoning: { summary: "auto" } }, ok: false, latencyMs: 300, status: 200,
      error: "Unsupported value: 'none' is not supported with the 'gpt-6.1-sol' model." }))
      .toBe("the proxy answered 200 — Unsupported value: 'none' is not supported with the 'gpt-6.1-sol' model. (sent {\"reasoning\":{\"summary\":\"auto\"}})");
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
