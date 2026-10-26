import { describe, expect, test } from "bun:test";
import {
  DEFAULT_PULL_REQUEST_CHOICE,
  PROJECT_ONLY_DELIVERY_KEYS,
  PULL_REQUEST_CHOICE,
  PULL_REQUEST_CHOICES,
  deliveryPolicySchema,
  settingsPatchSchema,
} from "../src/index.ts";

describe("before the pull request", () => {
  test("a policy takes open, draft or ask, and nothing else", () => {
    for (const v of PULL_REQUEST_CHOICES) {
      expect(deliveryPolicySchema.safeParse({ pullRequest: v }).success).toBe(true);
      expect(settingsPatchSchema.safeParse({ delivery: { pullRequest: v } }).success).toBe(true);
    }
    expect(deliveryPolicySchema.safeParse({ pullRequest: "later" }).success).toBe(false);
    expect(settingsPatchSchema.safeParse({ delivery: { pullRequest: "Open it" } }).success).toBe(false);
    // Reset is a delete.
    expect(settingsPatchSchema.safeParse({ delivery: { pullRequest: null } }).success).toBe(true);
  });

  test("open is the default, the setting is a project's only, and each value has its one line", () => {
    expect(DEFAULT_PULL_REQUEST_CHOICE).toBe("open");
    expect(PROJECT_ONLY_DELIVERY_KEYS).toContain("pullRequest");
    expect(PULL_REQUEST_CHOICE.open.label).toBe("Open it");
    expect(PULL_REQUEST_CHOICE.draft.hint).toContain("CI still runs on most repositories");
    expect(PULL_REQUEST_CHOICE.ask.label).toBe("Ask me first");
  });
});
