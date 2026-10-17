import { expect, test } from "bun:test";
import { serverCommentingSchema, type ServerCommenting } from "../src/servers.ts";

test("a preview server's commenting facts: available, or not with what is missing, and whether it needs a restart", () => {
  const good: ServerCommenting[] = [
    { commenting: { available: true }, needsRestart: false },
    { commenting: { available: true }, needsRestart: true },
    { commenting: { available: false, reason: "public_url" }, needsRestart: false },
    { commenting: { available: false, reason: "lux" }, needsRestart: false },
  ];
  for (const ok of good) expect(serverCommentingSchema.parse(ok)).toEqual(ok);
  for (const bad of [
    { commenting: { available: false }, needsRestart: false },
    { commenting: { available: false, reason: "other" }, needsRestart: false },
    { commenting: { available: true, reason: "lux" }, needsRestart: false },
    { commenting: { available: true } },
  ]) expect(serverCommentingSchema.safeParse(bad).success).toBe(false);
});
