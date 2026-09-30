/**
 * The actions a pull request offers, rendered: nothing re-runs a check
 * that could not be read, and Merge stays off with the reason.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import type { PullRequest } from "@dude/domain";
import type { ApiClient } from "../src/api/client.ts";
import { PullRequestActions } from "../src/screens/PullRequestActions.tsx";

const denied = { name: "GitHub check runs", status: "unavailable", conclusion: "", diagnostic: "check_runs_forbidden" };
const pr = (over: Partial<PullRequest>): PullRequest => ({
  id: "pr_1", taskId: "t", runId: null, repositoryId: "r", repositoryName: "web", number: 41, url: "https://github.com/a/web/pull/41",
  headBranch: "dude/t/attempt-1", baseBranch: "main", title: "Chart library", state: "open",
  checks: [], checkState: "pending", review: "approved", reviews: [], mergeable: "clean", behindBy: 0, unresolvedThreads: 0,
  display: "ci_running", createdAt: "", updatedAt: "", ...over,
});

const render = (p: PullRequest) => renderToStaticMarkup(
  <PullRequestActions client={{} as ApiClient} pr={p} defaultMethod="squash" onChanged={() => {}}>
    {({ facts, merge, note }) => <div>{facts.checks}{merge}{note}</div>}
  </PullRequestActions>,
);

describe("pull request actions", () => {
  test("unreadable check runs offer no re-run, and Merge is off saying why", () => {
    const h = render(pr({ checks: [{ name: "CodeRabbit", status: "completed", conclusion: "success" }, denied] }));
    expect(h).not.toContain('data-testid="pr-rerun"');
    expect(h).toMatch(/data-testid="pr-merge"[^>]*disabled|disabled[^>]*data-testid="pr-merge"/);
    const note = h.match(/<span data-testid="pr-blocked">([^<]*)<\/span>/)?.[1]?.replaceAll("&#x27;", "'");
    expect(note).toBe("Blocked: GitHub refused the check-runs read; check the token's Checks: Read permission and its repository/organization access (SSO, token approval)");
  });

  test("a real failure beside them can still be re-run", () => {
    const h = render(pr({ checkState: "failing", display: "ci_red", checks: [{ name: "lint", status: "completed", conclusion: "failure" }, denied] }));
    expect(h).toContain('data-testid="pr-rerun"');
  });
});
