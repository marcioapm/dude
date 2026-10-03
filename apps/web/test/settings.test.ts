import { describe, expect, test } from "bun:test";
import type { DeliverySettings, FullDeliveryPolicy, RoleSettings } from "@dude/domain";
import { deliveryChanged, deliveryPatch, effortLabel, roleChanged, settingsPage, timeLimitLabel } from "../src/settings.ts";

const org = <T,>(value: T) => ({ value, source: "organization" as const });
const proj = <T,>(value: T) => ({ value, source: "project" as const });

const delivery: DeliverySettings = {
  requiredReviewers: org(["correctness"]),
  blockingSeverities: org(["blocking", "high"]),
  maxReviewIterations: proj(3),
  maxAttemptsPerFinding: org(2),
  maxPrFixIterations: org(3),
  simplify: org(true),
  test: org(false),
  parkAfterMinutes: org(10),
  idleNudgeMinutes: org(0),
  conductorWarmMinutes: org(5),
} as DeliverySettings;

const role = (over: Partial<RoleSettings> = {}): RoleSettings => ({
  model: org("m"),
  effort: org(null),
  timeLimitMinutes: org(null),
  machineSize: org(null),
  image: org(null),
  enabled: null,
  prompt: { organization: { versionId: null, body: "", updatedAt: null, updatedBy: null, versions: 0 } },
  ...over,
});

describe("settings helpers", () => {
  test("a delivery form's patch is only what it changed", () => {
    const form = { ...Object.fromEntries(Object.entries(delivery).map(([k, s]) => [k, s.value])), test: true, requiredReviewers: ["correctness", "security"] } as FullDeliveryPolicy;
    expect(deliveryPatch(delivery, form)).toEqual({ test: true, requiredReviewers: ["correctness", "security"] });
  });

  test("counts what a project overrides", () => {
    expect(deliveryChanged(delivery)).toBe(1);
    expect(roleChanged(role())).toBe(false);
    expect(roleChanged(role({ effort: proj("low") }))).toBe(true);
    expect(roleChanged(role({ machineSize: proj("msz_xl") }))).toBe(true);
    const withPrompt = (mode: "add" | "replace" | "inherit") =>
      role({ prompt: { organization: role().prompt.organization, project: { ...role().prompt.organization, mode } } });
    expect(roleChanged(withPrompt("inherit"))).toBe(false);
    expect(roleChanged(withPrompt("add"))).toBe(true);
  });

  test("labels read as a person says them", () => {
    expect([timeLimitLabel(null), timeLimitLabel(45), timeLimitLabel(60), timeLimitLabel(120), timeLimitLabel(90)])
      .toEqual(["No limit", "45 min", "1 hour", "2 hours", "90 min"]);
    expect([effortLabel(null), effortLabel("high")]).toEqual(["Model’s default", "High"]);
  });

  test("a place's page, or the first", () => {
    expect(settingsPage("delivery", ["general", "delivery"] as const)).toBe("delivery");
    expect(settingsPage("nope", ["general", "delivery"] as const)).toBe("general");
    expect(settingsPage(undefined, ["general", "delivery"] as const)).toBe("general");
  });
});
