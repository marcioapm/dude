import { describe, expect, test } from "bun:test";
import {
  SETTINGS_ROLES,
  SETTINGS_ROLE_DESCRIPTION,
  SETTINGS_ROLE_LABEL,
  isConductor,
  resolveMachineSize,
  runLabel,
  settingsPatchSchema,
} from "../src/index.ts";

describe("the conductor's settings", () => {
  test("it is a configured role, first, with a label and a line", () => {
    expect(SETTINGS_ROLES[0]).toBe("conductor");
    expect(SETTINGS_ROLE_LABEL.conductor).toBe("Conductor");
    expect(SETTINGS_ROLE_DESCRIPTION.conductor.length).toBeGreaterThan(10);
  });

  test("its model, effort, time limit and machine can be set, and its warm minutes", () => {
    const patch = { roles: { conductor: { model: "llm-anthropic/claude-opus-5", effort: "low", timeLimitMinutes: 30, machineSize: "msz_s" } },
      delivery: { conductorWarmMinutes: 3 } };
    expect(settingsPatchSchema.safeParse(patch).success).toBe(true);
    expect(settingsPatchSchema.safeParse({ delivery: { conductorWarmMinutes: 0 } }).success).toBe(false);
    expect(settingsPatchSchema.safeParse({ roles: { orchestrator: { model: "llm-openai/x" } } }).success).toBe(false);
  });

  test("its machine follows no other role", () => {
    const sizes = [{ id: "std", isDefault: true }, { id: "small", isDefault: false }, { id: "big", isDefault: false }];
    expect(resolveMachineSize("conductor", { organization: { implementer: { machineSize: "big" } } }, sizes))
      .toEqual({ sizeId: "std", from: "default" });
    expect(resolveMachineSize("conductor", { organization: { conductor: { machineSize: "small" } } }, sizes))
      .toEqual({ sizeId: "small", from: "organization" });
  });
});

describe("a conductor Run", () => {
  test("is the role conductor with no phase, and is named Conductor", () => {
    const run = { phase: null, role: "conductor", kind: "agent" };
    expect(isConductor(run)).toBe(true);
    expect(runLabel(run)).toBe("Conductor");
    expect(isConductor({ phase: "implement", role: "conductor" })).toBe(false);
    expect(runLabel({ phase: null, role: null })).toBe("Agent");
  });
});
