import { expect, test } from "bun:test";
import { previewSettingsSchema } from "../src/servers.ts";

test("preview wake timeouts default for missing fields and accept whole minutes 1..120", () => {
  const old = previewSettingsSchema.parse({ idleTimeoutMinutes: 30 });
  expect(old.firstWakeTimeoutMinutes).toBe(15);
  expect(old.wakeTimeoutMinutes).toBe(5);
  for (const field of ["firstWakeTimeoutMinutes", "wakeTimeoutMinutes"]) {
    for (const value of [1, 120]) expect(previewSettingsSchema.parse({ [field]: value })[field as keyof typeof old]).toBe(value);
    for (const value of [0, 121, 1.5, "15", null]) expect(previewSettingsSchema.safeParse({ [field]: value }).success).toBe(false);
  }
});
