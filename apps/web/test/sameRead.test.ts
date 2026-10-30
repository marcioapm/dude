/**
 * `sameRead`: a re-read that says the same keeps the read on screen. Each
 * term is pinned on its own: a newer snapshot with the repositories as they
 * were, a pull request head moved with its sync time as it was.
 */

import { describe, expect, test } from "bun:test";
import { branchDiffFor } from "../src/fixtures/branchDiff.ts";
import { sameRead } from "../src/branchReview.ts";

describe("sameRead", () => {
  const a = branchDiffFor("populated", 1, { files: false }, []);

  test("the same read, copied, is the same", () => {
    expect(a.pullRequest).not.toBeNull();
    expect(sameRead(a, { ...a })).toBe(true);
  });

  test("a newer snapshot alone (the repositories as they were) is not the same", () => {
    expect(a.newer).toBeNull();
    const b = { ...a, newer: { id: "tbs_fx_merged", count: 1, relation: "merged-target" as const, headSha: "h", fromHeadSha: "f",
      mergeBaseSha: "m", fromMergeBaseSha: "n", capturedAt: new Date().toISOString() } };
    expect(b.repositories).toBe(a.repositories);
    expect(sameRead(a, b as typeof a)).toBe(false);
  });

  test("the pull request's head moved alone (its sync time as it was) is not the same", () => {
    const pr = a.pullRequest!;
    const b = { ...a, pullRequest: { ...pr, headSha: "0123456789abcdef" } };
    expect(b.pullRequest.syncedAt).toBe(pr.syncedAt);
    expect(sameRead(a, b)).toBe(false);
  });
});
