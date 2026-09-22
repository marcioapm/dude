/**
 * The git forge integration.
 *
 * Two kinds of test here. The pure functions — slug parsing, check and review
 * rollup — are where a forge's vocabulary is translated into ours, and getting
 * them wrong means a PR reads as passing when it is not. The routes are tested
 * against a stub forge rather than GitHub: the point is that we record what the
 * forge told us, not that GitHub works.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { closePool, setPool, withOrg } from "../src/db/client.ts";
import { createApiKey } from "../src/api/auth.ts";
import { slugFromUrl } from "../src/forge/github.ts";
import { branchForRun } from "../src/api/routes/pullRequests.ts";
import { startServer } from "../src/index.ts";
import * as ledger from "../src/events/ledger.ts";
import { EventTypes } from "@dude/domain";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const APP_URL = process.env.TEST_APP_DATABASE_URL ?? "postgres://dude_app:dude_app@localhost:5433/dude";

const ORG = `org_forge_${Bun.randomUUIDv7("hex").slice(-8)}`;

let owner: SQL;
/** This file's pool, so closing it cannot sever another file's. */
let app: SQL;
let server: ReturnType<typeof startServer>;
let baseUrl: string;
let userKey: string;
let runnerKey: string;
let projectId: string;
let repositoryId: string;
let workItemId: string;
let runId: string;

beforeAll(async () => {
  owner = new SQL(OWNER_URL);
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})
              ON CONFLICT (id) DO NOTHING`;

  app = new SQL(APP_URL);
  setPool(app);
  userKey = (await createApiKey({ organizationId: ORG, name: "user" })).key;
  runnerKey = (await createApiKey({ organizationId: ORG, name: "runner", kind: "runner" })).key;

  projectId = `prj_${Bun.randomUUIDv7("hex").slice(-8)}`;
  repositoryId = `repo_${Bun.randomUUIDv7("hex").slice(-8)}`;
  workItemId = `wi_${Bun.randomUUIDv7("hex").slice(-8)}`;
  runId = `run_${Bun.randomUUIDv7("hex").slice(-8)}`;

  await owner`INSERT INTO projects (id, organization_id, name, slug)
              VALUES (${projectId}, ${ORG}, 'Forge', ${`forge-${Bun.randomUUIDv7("hex").slice(-6)}`})`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
              VALUES (${repositoryId}, ${ORG}, ${projectId}, 'target',
                      'https://github.com/acme/target.git', 'main')`;
  await owner`INSERT INTO work_items (id, organization_id, project_id, title)
              VALUES (${workItemId}, ${ORG}, ${projectId}, 'Publish me')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status)
              VALUES (${runId}, ${ORG}, ${projectId}, ${workItemId}, 1, 'running')`;

  server = startServer(0);
  baseUrl = `http://localhost:${server.port}`;
});

afterAll(async () => {
  await server?.stop(true);
  await closePool(app);
  await owner`DELETE FROM organizations WHERE id = ${ORG}`;
  await owner.end();
});

function auth(key: string) {
  return { authorization: `Bearer ${key}`, "content-type": "application/json" };
}

describe("slugFromUrl", () => {
  test("reads owner/repo from every spelling a forge hands out", () => {
    // A project is configured with whichever URL its operator copied.
    expect(slugFromUrl("https://github.com/acme/target.git")).toBe("acme/target");
    expect(slugFromUrl("https://github.com/acme/target")).toBe("acme/target");
    expect(slugFromUrl("git@github.com:acme/target.git")).toBe("acme/target");
    expect(slugFromUrl("ssh://git@github.com/acme/target.git")).toBe("acme/target");
    expect(slugFromUrl("https://github.com/acme/target/")).toBe("acme/target");
  });

  test("returns null rather than a wrong guess", () => {
    // A local path is a legitimate repository URL here — the local
    // provisioner uses them — and it has no forge slug at all.
    expect(slugFromUrl("/tmp/dude-repos/target")).toBeNull();
    expect(slugFromUrl("")).toBeNull();
  });
});

describe("branchForRun", () => {
  test("namespaces machine-authored branches and separates attempts", () => {
    expect(branchForRun("wi_abc", 1)).toBe("dude/wi_abc/attempt-1");
    expect(branchForRun("wi_abc", 2)).toBe("dude/wi_abc/attempt-2");
  });
});

describe("forge credentials", () => {
  test("never returns the secret it was given", async () => {
    const res = await fetch(`${baseUrl}/v1/forge/credential`, {
      method: "POST",
      headers: auth(userKey),
      body: JSON.stringify({ auth: "pat", secret: "ghp_supersecret" }),
    });
    expect(res.status).toBe(200);

    const body = await res.text();
    // The one property that matters: a secret written here must not come
    // back out through any response.
    expect(body).not.toContain("ghp_supersecret");
    expect(JSON.parse(body)).toMatchObject({ auth: "pat" });
  });

  test("replaces rather than accumulating", async () => {
    for (const secret of ["ghp_first", "ghp_second"]) {
      const res = await fetch(`${baseUrl}/v1/forge/credential`, {
        method: "POST",
        headers: auth(userKey),
        body: JSON.stringify({ auth: "pat", secret }),
      });
      expect(res.status).toBe(200);
    }

    const rows = await withOrg(ORG, async (scope) => {
      return (await scope.sql`
        SELECT secret FROM forge_credentials WHERE forge = 'github'`) as Array<{ secret: string }>;
    });
    expect(rows).toHaveLength(1);
    expect(rows[0]!.secret).toBe("ghp_second");
  });

  test("rejects github_app without the ids it needs", async () => {
    const res = await fetch(`${baseUrl}/v1/forge/credential`, {
      method: "POST",
      headers: auth(userKey),
      body: JSON.stringify({ auth: "github_app", secret: "-----BEGIN KEY-----" }),
    });
    expect(res.status).toBe(400);
  });
});

describe("push credential", () => {
  test("is refused to a user key", async () => {
    // Only the runner can push, because only it has the workspace. The
    // refusal is a 401 rather than a 403 throughout this API: a wrong key
    // kind should not confirm that the endpoint exists.
    const res = await fetch(`${baseUrl}/v1/runs/${runId}/push-credential`, {
      headers: auth(userKey),
    });
    expect(res.status).toBe(401);
  });

  test("is refused for a Run with no live lease", async () => {
    // The Run above has no worker and no lease: a node that lost its lease
    // must not be able to keep writing to the repository.
    const res = await fetch(`${baseUrl}/v1/runs/${runId}/push-credential`, {
      headers: auth(runnerKey),
    });
    expect(res.status).toBe(404);
  });

  test("carries the branch the control plane derived", async () => {
    const workerId = `wrk_${Bun.randomUUIDv7("hex").slice(-8)}`;
    await owner`INSERT INTO workers (id, organization_id, name, pool)
                VALUES (${workerId}, ${ORG}, ${workerId}, 'default')`;
    await owner`UPDATE runs SET worker_id = ${workerId},
                lease_expires_at = now() + interval '5 minutes' WHERE id = ${runId}`;

    const res = await fetch(`${baseUrl}/v1/runs/${runId}/push-credential`, {
      headers: auth(runnerKey),
    });
    expect(res.status).toBe(200);

    const body = (await res.json()) as { username: string; token: string; branch: string };
    // Both sides must agree on the branch without storing it, so a restart
    // cannot make them disagree.
    expect(body.branch).toBe(branchForRun(workItemId, 1));
    expect(body.username).toBe("x-access-token");
    expect(body.token).toBe("ghp_second");
  });
});

describe("pull requests", () => {
  test("lists nothing for a run that has none", async () => {
    const res = await fetch(`${baseUrl}/v1/pull-requests?runId=${runId}`, {
      headers: auth(userKey),
    });
    expect(res.status).toBe(200);
    expect((await res.json()) as { pullRequests: unknown[] }).toMatchObject({ pullRequests: [] });
  });

  test("rejects a repository from another project", async () => {
    const otherProject = `prj_${Bun.randomUUIDv7("hex").slice(-8)}`;
    const otherRepo = `repo_${Bun.randomUUIDv7("hex").slice(-8)}`;
    await owner`INSERT INTO projects (id, organization_id, name, slug)
                VALUES (${otherProject}, ${ORG}, 'Other', ${`other-${Bun.randomUUIDv7("hex").slice(-6)}`})`;
    await owner`INSERT INTO repositories (id, organization_id, project_id, name, url)
                VALUES (${otherRepo}, ${ORG}, ${otherProject}, 'elsewhere',
                        'https://github.com/acme/elsewhere.git')`;

    const res = await fetch(`${baseUrl}/v1/runs/${runId}/pull-request`, {
      method: "POST",
      headers: auth(userKey),
      body: JSON.stringify({ repositoryId: otherRepo, title: "Nope" }),
    });
    // Crossing a project boundary must not be possible even inside one org.
    expect(res.status).toBe(404);
  });

  test("reports a missing run rather than calling the forge", async () => {
    const res = await fetch(`${baseUrl}/v1/runs/run_nonexistent/pull-request`, {
      method: "POST",
      headers: auth(userKey),
      body: JSON.stringify({ repositoryId, title: "Nope" }),
    });
    expect(res.status).toBe(404);
  });
});

describe("findings reported by a re-review", () => {
  /*
   * What lets the review → fix loop converge. A finding from the first
   * review is closed by a later review of the same kind that does not raise
   * it again — but only once a fixer has actually attempted it, and only
   * within the same category.
   */
  let reviewer: string;

  beforeAll(async () => {
    const workerId = `wrk_${Bun.randomUUIDv7("hex").slice(-8)}`;
    await owner`INSERT INTO workers (id, organization_id, name, pool)
                VALUES (${workerId}, ${ORG}, ${workerId}, 'default')`;
    reviewer = workerId;
  });

  async function reviewRun(category: string): Promise<string> {
    const id = `run_${Bun.randomUUIDv7("hex").slice(-8)}`;
    await owner`INSERT INTO runs (id, organization_id, project_id, work_item_id, attempt, status, phase, worker_id)
                VALUES (${id}, ${ORG}, ${projectId}, ${workItemId}, 1, 'running', 'review', ${reviewer})`;
    // Through the real ledger rather than a hand-written insert, so the
    // test cannot drift from the schema the code actually reads.
    await ledger.append({
      eventType: EventTypes.RunCreated,
      organizationId: ORG,
      projectId,
      workItemId,
      runId: id,
      sessionId: null,
      workflowRunId: null,
      actor: { type: "system", id: "test" },
      source: "control-plane",
      correlationId: null,
      causationId: null,
      payload: { phase: "review", category },
    });
    return id;
  }

  async function report(runId: string, findings: unknown[]) {
    const res = await fetch(`${baseUrl}/v1/runs/${runId}/findings`, {
      method: "POST",
      headers: auth(runnerKey),
      body: JSON.stringify({ findings }),
    });
    expect(res.status).toBe(201);
  }

  async function statusOf(title: string): Promise<string> {
    const [row] = (await owner`
      SELECT status FROM review_findings WHERE work_item_id = ${workItemId} AND title = ${title}
      ORDER BY created_at DESC LIMIT 1`) as Array<{ status: string }>;
    return row!.status;
  }

  const blocking = (title: string, category = "correctness") => ({
    severity: "blocking",
    category,
    title,
  });

  test("a clean re-review resolves a finding the fixer attempted", async () => {
    await report(await reviewRun("correctness"), [blocking("Attempted and fixed")]);
    await owner`UPDATE review_findings SET fix_attempts = 1 WHERE title = 'Attempted and fixed'`;

    await report(await reviewRun("correctness"), []);

    expect(await statusOf("Attempted and fixed")).toBe("resolved");
  });

  test("a finding no fixer has touched stays open", async () => {
    // Not raised again is not the same as fixed: a flaky reviewer omits
    // things all the time, and nothing has tried to fix this one.
    await report(await reviewRun("correctness"), [blocking("Never attempted")]);
    await report(await reviewRun("correctness"), []);

    expect(await statusOf("Never attempted")).toBe("open");
  });

  test("a re-review of another category does not resolve it", async () => {
    // A security reviewer saying nothing is not evidence that a correctness
    // problem went away.
    await report(await reviewRun("correctness"), [blocking("Wrong reviewer")]);
    await owner`UPDATE review_findings SET fix_attempts = 1 WHERE title = 'Wrong reviewer'`;

    await report(await reviewRun("security"), []);

    expect(await statusOf("Wrong reviewer")).toBe("open");
  });

  test("a problem the re-review raises again reappears as open", async () => {
    await report(await reviewRun("correctness"), [blocking("Survived the fix")]);
    await owner`UPDATE review_findings SET fix_attempts = 1 WHERE title = 'Survived the fix'`;

    await report(await reviewRun("correctness"), [blocking("Survived the fix")]);

    // The old row closes and the new one is open, so the loop still sees a
    // blocking finding and does not stop.
    const rows = (await owner`
      SELECT status FROM review_findings WHERE title = 'Survived the fix'
      ORDER BY created_at`) as Array<{ status: string }>;
    expect(rows.map((r) => r.status)).toEqual(["resolved", "open"]);
  });
});
