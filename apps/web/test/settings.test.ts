import { describe, expect, test } from "bun:test";
import { deliveryPolicySchema, settingsPatchSchema, type DeliverySettings, type FullDeliveryPolicy, type RoleSettings, type DeliveryPolicy } from "@dude/domain";
import { deliveryChanged, deliveryPatch, deliveryValues, roleChanged, settingsPage, timeLimitLabel } from "../src/settings.ts";

const org = <T,>(value: T) => ({ value, source: "organization" as const });
const proj = <T,>(value: T) => ({ value, source: "project" as const });

const delivery: DeliverySettings = {
  requiredReviewers: org(["correctness"]),
  blockingSeverities: org(["blocking", "high"]),
  maxReviewIterations: proj(3),
  maxAttemptsPerFinding: org(2),
  maxStillRounds: org(2),
  maxRegressionRounds: org(2),
  maxPrFixIterations: org(3),
  simplify: org(true),
  test: org(false),
  parkAfterMinutes: org(10),
  idleNudgeMinutes: org(0),
  conductorWarmMinutes: org(5),
} as DeliverySettings;

const role = (over: Partial<RoleSettings> = {}): RoleSettings => ({
  tier: org("m"),
  timeLimitMinutes: org(null),
  harness: org("opencode"),
  machineSize: org(null),
  image: org(null),
  enabled: null,
  prompt: { organization: { versionId: null, body: "", updatedAt: null, updatedBy: null, versions: 0 } },
  ...over,
});

describe("settings helpers", () => {
  for (const key of ["maxStillRounds", "maxRegressionRounds"] as const) {
    test(`${key} accepts bounds, reset and omission without inserting frozen defaults`, () => {
      for (const value of [0, 2, 20]) {
        expect(deliveryPolicySchema.parse({ [key]: value })).toEqual({ [key]: value });
        expect(settingsPatchSchema.parse({ delivery: { [key]: value } })).toEqual({ delivery: { [key]: value } });
      }
      for (const value of [-1, 21, 1.5, "2", null]) {
        expect(deliveryPolicySchema.safeParse({ [key]: value }).success).toBe(false);
        if (value !== null) expect(settingsPatchSchema.safeParse({ delivery: { [key]: value } }).success).toBe(false);
      }
      expect(settingsPatchSchema.parse({ delivery: { [key]: null } })).toEqual({ delivery: { [key]: null } });
      expect(deliveryPolicySchema.parse({})).toEqual({});
      expect(settingsPatchSchema.parse({ delivery: {} })).toEqual({ delivery: {} });
    });

    test(`${key} preserves zero as an explicit project override`, () => {
      const form = { ...deliveryValues(delivery), [key]: 0 };
      expect(deliveryPatch(delivery, form)).toEqual({ [key]: 0 });
      const saved = { ...delivery, [key]: proj(0) };
      expect(deliveryValues(saved)[key]).toBe(0);
      expect(deliveryChanged(saved)).toBe(2);
      expect(deliveryPatch(saved, form)).toEqual({});
    });
  }
  test("a delivery form's patch is only what it changed", () => {
    const form = { ...Object.fromEntries(Object.entries(delivery).map(([k, s]) => [k, s.value])), test: true, requiredReviewers: ["correctness", "security"] } as FullDeliveryPolicy;
    expect(deliveryPatch(delivery, form)).toEqual({ test: true, requiredReviewers: ["correctness", "security"] });
  });

  test("the reviewers every round runs may be none, and the non-product paths are globs", () => {
    const fine: DeliveryPolicy[] = [{ alwaysReviewers: [] }, { alwaysReviewers: ["correctness", "documentation"] }, { nonProductPaths: [] }, { nonProductPaths: [".github/**"] }];
    for (const ok of fine) {
      expect(deliveryPolicySchema.parse(ok)).toEqual(ok);
      expect(settingsPatchSchema.parse({ delivery: ok })).toEqual({ delivery: ok });
    }
    for (const bad of [{ alwaysReviewers: ["style"] }, { alwaysReviewers: "correctness" }, { nonProductPaths: [""] }, { nonProductPaths: [" "] }, { nonProductPaths: [1] }]) {
      expect(deliveryPolicySchema.safeParse(bad).success).toBe(false);
      expect(settingsPatchSchema.safeParse({ delivery: bad }).success).toBe(false);
    }
    expect(settingsPatchSchema.parse({ delivery: { alwaysReviewers: null, nonProductPaths: null } })).toEqual({ delivery: { alwaysReviewers: null, nonProductPaths: null } });
  });

  test("counts what a project overrides", () => {
    expect(deliveryChanged(delivery)).toBe(1);
    expect(roleChanged(role())).toBe(false);
    expect(roleChanged(role({ timeLimitMinutes: proj(45) }))).toBe(true);
    expect(roleChanged(role({ machineSize: proj("msz_xl") }))).toBe(true);
    expect(roleChanged(role({ harness: proj("codex") }))).toBe(true);
    const withPrompt = (mode: "add" | "replace" | "inherit") =>
      role({ prompt: { organization: role().prompt.organization, project: { ...role().prompt.organization, mode } } });
    expect(roleChanged(withPrompt("inherit"))).toBe(false);
    expect(roleChanged(withPrompt("add"))).toBe(true);
  });

  test("labels read as a person says them", () => {
    expect([timeLimitLabel(null), timeLimitLabel(45), timeLimitLabel(60), timeLimitLabel(120), timeLimitLabel(90)])
      .toEqual(["2 hours (default)", "45 min", "1 hour", "2 hours", "90 min"]);
  });

  test("a place's page, or the first", () => {
    expect(settingsPage("delivery", ["general", "delivery"] as const)).toBe("delivery");
    expect(settingsPage("nope", ["general", "delivery"] as const)).toBe("general");
    expect(settingsPage(undefined, ["general", "delivery"] as const)).toBe("general");
  });
});
