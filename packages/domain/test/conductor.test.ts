import { describe, expect, test } from "bun:test";
import {
  SETTINGS_ROLES,
  SETTINGS_ROLE_DESCRIPTION,
  SETTINGS_ROLE_LABEL,
  isConductor,
  resolveMachineSize,
  ROLE_EFFORT_REMOVED,
  runLabel,
  settingsPatchSchema,
} from "../src/index.ts";

describe("the conductor's settings", () => {
  test("it is a configured role, first, with a label and a line", () => {
    expect(SETTINGS_ROLES[0]).toBe("conductor");
    expect(SETTINGS_ROLE_LABEL.conductor).toBe("Conductor");
    expect(SETTINGS_ROLE_DESCRIPTION.conductor.length).toBeGreaterThan(10);
  });

  test("its tier, time limit and machine can be set, and its warm minutes; an effort is the tier's", () => {
    const patch = { roles: { conductor: { tier: "mtr_thinker", timeLimitMinutes: 30, machineSize: "msz_s" } },
      delivery: { conductorWarmMinutes: 3 } };
    expect(settingsPatchSchema.safeParse(patch).success).toBe(true);
    const effort = settingsPatchSchema.safeParse({ roles: { conductor: { effort: "low" } } });
    expect(effort.success).toBe(false);
    expect(effort.error?.issues[0]?.message).toBe(ROLE_EFFORT_REMOVED);
    expect(settingsPatchSchema.safeParse({ delivery: { conductorWarmMinutes: 0 } }).success).toBe(false);
    expect(settingsPatchSchema.safeParse({ roles: { orchestrator: { tier: "mtr_thinker" } } }).success).toBe(false);
  });

  test("its edit limit is a delivery setting: whole lines and files, at least one", () => {
    expect(settingsPatchSchema.safeParse({ delivery: { conductorEditLines: 60, conductorEditFiles: 3 } }).success).toBe(true);
    expect(settingsPatchSchema.safeParse({ delivery: { conductorEditLines: null, conductorEditFiles: null } }).success).toBe(true);
    for (const bad of [{ conductorEditLines: 0 }, { conductorEditFiles: 0 }, { conductorEditLines: 1.5 }, { conductorEditFiles: "3" }]) {
      expect(settingsPatchSchema.safeParse({ delivery: bad }).success).toBe(false);
    }
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
