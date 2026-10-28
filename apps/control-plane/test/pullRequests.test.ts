/**
 * A pull request as the API returns it: its display from dude's rollup,
 * and its check list as stored, a diagnostic entry included.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { pullRequestSchema } from "@dude/domain";
import { registerPullRequestRoutes, withDisplay } from "../src/api/routes/pullRequests.ts";
import { createApiKey, type Principal } from "../src/api/auth.ts";
import { Router } from "../src/api/router.ts";
import { closePool, setPool } from "../src/db/client.ts";

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

/**
 * baseSyncStatus as the list reads it, against DATABASE_URL's database
 * (migrated): a pending request is queued only while the task's latest
 * delivery is live and tracks its pull request.
 */
describe("baseSyncStatus", () => {
  const org = `org_bss_${Bun.randomUUIDv7("hex").slice(-12)}`;
  const project = `${org}_p`;
  const task = `${org}_t`;
  let owner: SQL;
  let app: SQL;
  let router: Router;

  beforeAll(async () => {
    owner = new SQL(process.env.DATABASE_URL!);
    const url = new URL(process.env.DATABASE_URL!);
    url.username = "dude_app";
    url.password = "dude_app";
    app = new SQL(url.toString());
    setPool(app);
    await owner`INSERT INTO organizations (id, name, slug) VALUES (${org}, ${org}, ${org})`;
    const made = await createApiKey({ organizationId: org, name: "Owner" });
    const principal: Principal = { credentialKind: "api_key", apiKeyId: made.id, personId: made.personId,
      organizationId: org, kind: "user", role: "admin", name: "Owner" };
    router = new Router(async (credential) => (credential === "key" ? principal : null));
    registerPullRequestRoutes(router);
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES (${project}, ${org}, 'P', ${project}, 'P')`;
    await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES (${`${org}_r`}, ${org}, ${project}, 'web', 'https://github.com/acme/web.git')`;
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES (${task}, ${org}, ${project}, 1, 'T')`;
    for (const [id, n] of [[`${org}_pr1`, 1], [`${org}_pr2`, 2], [`${org}_pr3`, 3]] as const) {
      await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch, base_branch, head_sha, title)
        VALUES (${id}, ${org}, ${project}, ${task}, ${`${org}_r`}, ${n}, ${`https://x/${n}`}, ${`b${n}`}, 'main', 'h', 'T')`;
      await owner`INSERT INTO base_sync_requests (organization_id, task_id, pull_request_id, base, base_sha)
        VALUES (${org}, ${task}, ${id}, 'main', 'b4se')`;
    }
    // The third's request was integrated: complete whatever tracks it.
    await owner`UPDATE base_sync_requests SET completed_at = now() WHERE pull_request_id = ${`${org}_pr3`}`;
    // An earlier delivery tracked the second; the latest tracks the first.
    await owner`INSERT INTO workflow_runs (id, organization_id, workflow_type, idempotency_key, status, step, task_id, state, created_at)
      VALUES (${`${org}_wf0`}, ${org}, 'task.delivery', ${`delivery:${task}:0`}, 'waiting', 'awaitPullRequest', ${task},
        ${{ pullRequestIds: [`${org}_pr2`] }}::jsonb, now() - interval '1 hour'),
      (${`${org}_wf1`}, ${org}, 'task.delivery', ${`delivery:${task}:1`}, 'waiting', 'awaitPullRequest', ${task},
        ${{ pullRequestIds: [`${org}_pr1`, `${org}_pr3`] }}::jsonb, now())`;
  });

  afterAll(async () => {
    await closePool(app);
    await owner`DELETE FROM organizations WHERE id = ${org}`;
    await owner.end();
  });

  const statuses = async () => {
    const res = await router.handle(new Request(`http://dude.test/v1/pull-requests?taskId=${task}`, { headers: { authorization: "key" } }));
    const body = (await res.json()) as { pullRequests: Array<{ number: number; baseSyncStatus: string | null }> };
    return Object.fromEntries(body.pullRequests.map((p) => [p.number, p.baseSyncStatus]));
  };

  test("queued on the pull request the live delivery tracks, none on one it no longer does", async () => {
    expect(await statuses()).toEqual({ 1: "queued", 2: null, 3: "complete" });
  });

  test("none once the delivery is no longer live", async () => {
    await owner`UPDATE workflow_runs SET status = 'completed' WHERE id = ${`${org}_wf1`}`;
    expect(await statuses()).toEqual({ 1: null, 2: null, 3: "complete" });
  });
});
