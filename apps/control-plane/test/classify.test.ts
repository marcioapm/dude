/**
 * The PR classifier: what on a pull request is worth waking an agent for.
 *
 * Every false positive here costs a fix Run; every false negative is a person's
 * request silently ignored. The tests pin both directions.
 */

import { describe, expect, test } from "bun:test";
import { classify, isActionableComment } from "../src/forge/classify.ts";
import type { PullRequestFeedback, PullRequestStatus } from "../src/forge/github.ts";

const open: PullRequestStatus = {
  number: 1,
  nodeId: null,
  url: "https://github.com/a/b/pull/1",
  state: "open",
  headSha: "abc",
  checks: "passing",
  review: "pending",
};
const prior = { state: "open", checks: "passing", review: "pending" };

function comment(body: string, author = "alice", kind: PullRequestFeedback["kind"] = "comment"): PullRequestFeedback {
  return { id: body, author, body, kind, createdAt: "2026-09-22T10:00:00Z" };
}

describe("what wakes a fixer", () => {
  test("a person asking for a change", () => {
    const signal = classify(prior, open, [comment("Please rename `foo` to `bar`.")]);
    expect(signal).toMatchObject({ kind: "actionable" });
  });

  test("a review that requested changes, however briefly worded", () => {
    // "ok" on its own would be filtered; as a changes-requested review it is
    // a request by definition.
    const signal = classify(prior, open, [comment("ok", "alice", "changes_requested")]);
    expect(signal).toMatchObject({ kind: "actionable" });
  });

  test("checks turning red", () => {
    const signal = classify(prior, { ...open, checks: "failing" }, []);
    expect(signal).toMatchObject({ kind: "actionable", feedback: [{ source: "checks" }] });
  });

  test("a line comment keeps the file it is about", () => {
    const signal = classify(prior, open, [
      { ...comment("This leaks the token", "alice", "line_comment"), path: "src/push.go" },
    ]);
    expect(signal).toMatchObject({ feedback: [{ path: "src/push.go" }] });
  });
});

describe("what does not", () => {
  test("an approval", () => {
    expect(classify(prior, { ...open, review: "approved" }, [])).toBeNull();
  });

  test("checks going green", () => {
    expect(classify({ ...prior, checks: "failing" }, open, [])).toBeNull();
  });

  test("checks that were already red", () => {
    // The last fix already saw this failure; waking again would spend the
    // loop's budget on a problem it is already working on.
    expect(classify({ ...prior, checks: "failing" }, { ...open, checks: "failing" }, [])).toBeNull();
  });

  test("courtesy comments", () => {
    for (const body of ["LGTM", "lgtm!", "Thanks", "Looks good to me.", "👍", "Ship it"]) {
      expect(isActionableComment(comment(body), [])).toBe(false);
    }
  });

  test("courtesy phrased more loosely", () => {
    // Caught in the end-to-end test: an anchored pattern let "LGTM so far,
    // thanks!" through and woke a fixer for nothing.
    for (const body of ["LGTM so far, thanks!", "Great work, thank you 🎉", "looks good for now"]) {
      expect(isActionableComment(comment(body), [])).toBe(false);
    }
  });

  test("bots", () => {
    for (const author of ["dependabot[bot]", "codecov-bot", "github-actions"]) {
      expect(isActionableComment(comment("Coverage dropped 2%", author), [])).toBe(false);
    }
  });

  test("the factory's own account", () => {
    expect(isActionableComment(comment("Addressed in abc123", "dude-factory"), ["dude-factory"])).toBe(false);
  });

  test("a request among courtesies is still a request", () => {
    for (const body of [
      "LGTM, but please rename foo",
      "Looks good. Why is this async?",
      "thanks — can you add a test",
      "Great work, fix the typo in the README",
    ]) {
      expect(isActionableComment(comment(body), [])).toBe(true);
    }
  });

  test("a polite request is still a request", () => {
    // The courtesy filter matches whole comments, not prefixes: "thanks" in a
    // sentence that asks for something must not hide the ask.
    expect(isActionableComment(comment("Thanks! Could you also add a test?"), [])).toBe(true);
  });

  test("nothing at all", () => {
    expect(classify(prior, open, [])).toBeNull();
  });
});

describe("terminal states", () => {
  test("a merge ends the loop", () => {
    expect(classify(prior, { ...open, state: "merged" }, [])).toEqual({ kind: "terminal", state: "merged" });
  });

  test("a close without merging ends it too", () => {
    expect(classify(prior, { ...open, state: "closed" }, [])).toEqual({ kind: "terminal", state: "closed" });
  });

  test("a merge seen twice is reported once", () => {
    expect(classify({ ...prior, state: "merged" }, { ...open, state: "merged" }, [])).toBeNull();
  });

  test("comments on a merged PR do not wake a fixer", () => {
    const signal = classify(prior, { ...open, state: "merged" }, [comment("Please change this")]);
    expect(signal).toMatchObject({ kind: "terminal" });
  });
});
