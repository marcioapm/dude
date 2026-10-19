import { describe, expect, test } from "bun:test";
import { settingsPatchSchema } from "../src/index.ts";

describe("checks report rounds", () => {
  test("is a delivery setting from 0 to 20 whole rounds, reset with null", () => {
    for (const ok of [0, 2, 20, null]) {
      expect(settingsPatchSchema.safeParse({ delivery: { checksFixRounds: ok } }).success).toBe(true);
    }
    for (const bad of [-1, 21, 1.5, "2"]) {
      expect(settingsPatchSchema.safeParse({ delivery: { checksFixRounds: bad } }).success).toBe(false);
    }
  });
});
