/**
 * A pull request as the API returns it: its display from dude's rollup,
 * and its check list as stored, a diagnostic entry included.
 */

import { describe, expect, test } from "bun:test";
import { pullRequestSchema } from "@dude/domain";
import { withDisplay } from "../src/api/routes/pullRequests.ts";

// A row as PR_SELECT reads it, with checks_json as the orchestrator stores it.
const row = (checkState: string, checks: unknown[]) => ({
  id: "pr_1", taskId: "t", runId: null, repositoryId: "r", repositoryName: "dude", number: 101, url: "https://github.com/a/dude/pull/101",
  headBranch: "h", baseBranch: "main", title: "First run", state: "open", checks, checkState, review: "approved", reviews: [],
  mergeable: "clean", behindBy: 0, unresolvedThreads: 0, createdAt: "", updatedAt: "",
});
const denied = { name: "GitHub check runs", status: "unavailable", conclusion: "", url: "", durationMs: 0, diagnostic: "check_runs_forbidden" };
const codeRabbit = { name: "CodeRabbit", status: "completed", conclusion: "success", url: "", durationMs: 0 };

describe("withDisplay", () => {
  test("pending from unreadable check runs stays pending beside a green subset, and keeps its diagnostic", () => {
    const out = pullRequestSchema.parse(withDisplay(row("pending", [codeRabbit, denied])));
    expect(out.display).toBe("ci_running");
    expect(out.checks.map((c) => c.diagnostic ?? null)).toEqual([null, "check_runs_forbidden"]);
  });

  test("the rollup decides over the list: a failure stays red, an empty list stays pending", () => {
    expect(withDisplay(row("failing", [codeRabbit, denied])).display).toBe("ci_red");
    expect(withDisplay(row("pending", [denied])).display).toBe("ci_running");
    expect(withDisplay(row("pending", [])).display).toBe("ci_running");
  });
});
