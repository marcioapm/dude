/**
 * A task attempt's whole branch through the public API, against a database
 * of its own: the latest or a named snapshot, what is newer and how it
 * relates, the published pull request's head, and the states when there
 * is nothing to show — preparing (a capture asked for, never a GitHub call
 * here), unavailable with why, none. An earlier attempt is read-only and
 * asks for nothing; another organization's task is not found.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { newerThan } from "../src/api/routes/branchDiff.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_branch_diff_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_bd";
const OTHER = "org_bd_other";

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let ana: { key: string; personId: string };
let cy: { key: string; personId: string };

const sha = (c: string) => c.repeat(40);

const BRANCH_FILES = [{
  path: "src/vat.ts", status: "M", additions: 2, deletions: 1,
  hunks: [{ header: "@@ -10,3 +10,4 @@", lines: [
    { kind: " ", old: 10, new: 10, text: "export function vat(n) {" },
    { kind: "-", old: 11, new: null, text: "  return n * 0.2;" },
    { kind: "+", old: null, new: 11, text: "  const rate = 0.23;" },
    { kind: "+", old: null, new: 12, text: "  return n * rate;" },
    { kind: " ", old: 12, new: 13, text: "}" },
  ] }],
}, { path: "logo.png", status: "A", additions: 0, deletions: 0, hunks: [], binary: true }];

async function snapshot(id: string, attempt: number, repo: string, base: string, head: string, relation: string,
    previous: string | null, at: string, org = ORG, task = "wi_bd"): Promise<void> {
  await owner`INSERT INTO task_branch_snapshots (id, organization_id, task_id, attempt, repository_id, repository, branch_ref,
      target_ref, target_sha, merge_base_sha, head_sha, files, checksum, stats, truncated, binary_files, relation, previous_snapshot_id,
      pr_head_sha, captured_at)
    VALUES (${id}, ${org}, ${task}, ${attempt}, ${repo}, ${repo === "repo_web" ? "web" : "api"}, ${`dude/${task}/attempt-${attempt}`},
      'main', ${sha("f")}, ${base}, ${head}, ${BRANCH_FILES}::jsonb, ${"sum-" + id},
      ${{ files: 2, additions: 2, deletions: 1, truncatedFiles: 0, binaryFiles: 1, fileCapReached: false }}::jsonb,
      false, 1, ${relation}, ${previous}, ${head}, ${at}::timestamptz)`;
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT,
    env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES
    ('prj_bd', ${ORG}, 'Billing', 'billing', 'BILL'), ('prj_bd_o', ${OTHER}, 'Other', 'other', 'OTH')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title, status) VALUES
    ('wi_bd', ${ORG}, 'prj_bd', 1, 'Fix VAT', 'running'),
    ('wi_fresh', ${ORG}, 'prj_bd', 2, 'Nothing captured', 'running'),
    ('wi_norepo', ${ORG}, 'prj_bd', 3, 'No code', 'running'),
    ('wi_o', ${OTHER}, 'prj_bd_o', 1, 'Theirs', 'running')`;
  await owner`INSERT INTO repositories (id, organization_id, project_id, name, url) VALUES
    ('repo_api', ${ORG}, 'prj_bd', 'api', 'https://github.com/acme/api'),
    ('repo_web', ${ORG}, 'prj_bd', 'web', 'https://github.com/acme/web'),
    ('repo_o', ${OTHER}, 'prj_bd_o', 'api', 'https://github.com/acme/other')`;
  await owner`INSERT INTO task_repositories (task_id, repository_id, organization_id, access) VALUES
    ('wi_bd', 'repo_api', ${ORG}, 'write'), ('wi_bd', 'repo_web', ${ORG}, 'write'),
    ('wi_fresh', 'repo_api', ${ORG}, 'write'), ('wi_o', 'repo_o', ${OTHER}, 'write')`;
  await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, kind) VALUES
    ('run_bd1', ${ORG}, 'prj_bd', 'wi_bd', 1, 'completed', 'implement', 'implementer', 'agent'),
    ('run_bd2', ${ORG}, 'prj_bd', 'wi_bd', 2, 'running', 'implement', 'implementer', 'agent')`;
  // Attempt 1: one snapshot. Attempt 2 of api: initial, then main merged in, then a rebase.
  await snapshot("tbs_old", 1, "repo_api", sha("0"), sha("1"), "initial", null, "2026-10-01T10:00:00Z");
  await snapshot("tbs_a", 2, "repo_api", sha("a"), sha("b"), "initial", null, "2026-10-28T10:00:00Z");
  await snapshot("tbs_b", 2, "repo_api", sha("a"), sha("c"), "fast-forward", "tbs_a", "2026-10-28T11:00:00Z");
  await snapshot("tbs_c", 2, "repo_api", sha("d"), sha("e"), "merged-target", "tbs_b", "2026-10-28T12:00:00Z");
  await snapshot("tbs_o", 1, "repo_o", sha("a"), sha("b"), "initial", null, "2026-10-28T10:00:00Z", OTHER, "wi_o");
  await owner`INSERT INTO pull_requests (id, organization_id, project_id, task_id, repository_id, number, url, head_branch,
      base_branch, head_sha, title, last_polled_at)
    VALUES ('pr_bd', ${ORG}, 'prj_bd', 'wi_bd', 'repo_api', 41, 'https://github.com/acme/api/pull/41', 'dude/wi_bd/attempt-2',
      'main', ${sha("9")}, 'Fix VAT', '2026-10-28T12:30:00Z')`;
  // The trigger asked for a capture when the pull request opened: settled at tbs_c.
  await owner`UPDATE task_branch_capture_requests SET status = 'done', snapshot_id = 'tbs_c'`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  ana = await createApiKey({ organizationId: ORG, name: "Ana" });
  cy = await createApiKey({ organizationId: OTHER, name: "Cy" });
  router = buildRouter("");
});

afterAll(async () => {
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

async function get(key: string, path: string): Promise<{ status: number; body: Json }> {
  const res = await router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${key}` } }));
  return { status: res.status, body: await res.json() };
}

describe("GET /v1/tasks/:id/branch-diff", () => {
  test("the latest snapshot of the current attempt, its repositories and the published head", async () => {
    const { status, body } = await get(ana.key, "/v1/tasks/wi_bd/branch-diff");
    expect(status).toBe(200);
    expect(body).toMatchObject({
      taskId: "wi_bd", attempt: 2, currentAttempt: 2, readOnly: false, repository: "api",
      availability: { state: "ok" }, newer: null, capture: null,
      repositories: [{ id: "repo_api", name: "api", access: "write", snapshots: 3 }, { id: "repo_web", name: "web", access: "write", snapshots: 0 }],
      pullRequest: { number: 41, url: "https://github.com/acme/api/pull/41", state: "open", headSha: sha("9"), syncedAt: "2026-10-28T12:30:00.000Z" },
      snapshot: { id: "tbs_c", taskId: "wi_bd", attempt: 2, repositoryId: "repo_api", repository: "api", branchRef: "dude/wi_bd/attempt-2",
        targetRef: "main", targetSha: sha("f"), mergeBaseSha: sha("d"), headSha: sha("e"), checksum: "sum-tbs_c", source: "github-compare",
        capturedAt: "2026-10-28T12:00:00.000Z", relation: "merged-target", previousSnapshotId: "tbs_b", truncated: false, status: "complete",
        reason: null, prHeadSha: sha("e"),
        stats: { files: 2, additions: 2, deletions: 1, truncatedFiles: 0, binaryFiles: 1, fileCapReached: false } },
    });
    expect(body.snapshot.files[1]).toMatchObject({ path: "logo.png", binary: true });
    // The key resolves as the task's id does.
    expect((await get(ana.key, "/v1/tasks/BILL-1/branch-diff")).body.snapshot.id).toBe("tbs_c");
  });

  test("a partial snapshot keeps the file-cap status and reason", async () => {
    await owner`UPDATE task_branch_snapshots SET status = 'partial', reason = 'GitHub lists at most 300 files', truncated = true WHERE id = 'tbs_c'`;
    try {
      expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff")).body.snapshot).toMatchObject({
        status: "partial", reason: "GitHub lists at most 300 files", truncated: true,
      });
    } finally {
      await owner`UPDATE task_branch_snapshots SET status = 'complete', reason = NULL, truncated = false WHERE id = 'tbs_c'`;
    }
  });

  test("a named older snapshot says what is newer and how it relates over the span", async () => {
    const a = (await get(ana.key, "/v1/tasks/wi_bd/branch-diff?snapshot=tbs_a")).body;
    expect(a.snapshot.id).toBe("tbs_a");
    expect(a.newer).toEqual({ id: "tbs_c", relation: "merged-target", fromMergeBaseSha: sha("a"), fromHeadSha: sha("b"),
      mergeBaseSha: sha("d"), headSha: sha("e"), capturedAt: "2026-10-28T12:00:00.000Z", count: 2 });
    const b = (await get(ana.key, "/v1/tasks/wi_bd/branch-diff?snapshot=tbs_b&attempt=2")).body;
    expect(b.newer).toMatchObject({ id: "tbs_c", relation: "merged-target", count: 1 });
  });

  test("a rewrite after the one shown reads as rewritten", () => {
    const at = (h: number) => new Date(Date.UTC(2026, 9, 28, h));
    const links = [
      { id: "s1", previousSnapshotId: null, relation: "initial" as const, mergeBaseSha: "a", headSha: "b", capturedAt: at(1) },
      { id: "s2", previousSnapshotId: "s1", relation: "fast-forward" as const, mergeBaseSha: "a", headSha: "c", capturedAt: at(2) },
      { id: "s3", previousSnapshotId: "s2", relation: "rewritten" as const, mergeBaseSha: "x", headSha: "y", capturedAt: at(3) },
    ];
    expect(newerThan("s1", "s2", links)?.relation).toBe("fast-forward");
    expect(newerThan("s1", "s3", links)?.relation).toBe("rewritten");
    expect(newerThan("s3", "s3", links)).toBeNull();
    expect(newerThan("s3", "s2", links)).toMatchObject({ relation: "rewritten", count: 2 });
  });

  test("a branch reset follows the request's snapshot, not the last capture time", async () => {
    await owner`UPDATE task_branch_capture_requests SET snapshot_id = 'tbs_b' WHERE task_id = 'wi_bd' AND repository_id = 'repo_api'`;
    try {
      expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff")).body.snapshot.id).toBe("tbs_b");
      expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?snapshot=tbs_c")).body.newer).toMatchObject({
        id: "tbs_b", relation: "rewritten", count: 2,
      });
    } finally {
      await owner`UPDATE task_branch_capture_requests SET snapshot_id = 'tbs_c' WHERE task_id = 'wi_bd' AND repository_id = 'repo_api'`;
    }
  });

  test("an earlier attempt is read-only with its recorded snapshots, and asks for nothing", async () => {
    const { body } = await get(ana.key, "/v1/tasks/wi_bd/branch-diff?attempt=1");
    expect(body).toMatchObject({ attempt: 1, currentAttempt: 2, readOnly: true, availability: { state: "ok" }, snapshot: { id: "tbs_old" } });
    const web = (await get(ana.key, "/v1/tasks/wi_bd/branch-diff?attempt=1&repository=web")).body;
    expect(web).toMatchObject({ readOnly: true, snapshot: null, availability: { state: "none" } });
    const [{ n }] = await owner`SELECT count(*)::int AS n FROM task_branch_capture_requests WHERE task_id = 'wi_bd' AND attempt = 1`;
    expect(n).toBe(0);
  });

  test("nothing captured yet on the current attempt: preparing, and a capture is asked for", async () => {
    const { body } = await get(ana.key, "/v1/tasks/wi_fresh/branch-diff");
    expect(body).toMatchObject({ repository: "api", availability: { state: "preparing" }, snapshot: null, capture: { state: "pending" } });
    const rows = await owner`SELECT attempt, repository_id, status FROM task_branch_capture_requests WHERE task_id = 'wi_fresh'`;
    expect(rows).toEqual([{ attempt: 1, repository_id: "repo_api", status: "pending" }]);
  });

  test("unavailable says why; a branch not pushed yet is none; no repository is none", async () => {
    await owner`UPDATE task_branch_capture_requests SET status = 'unavailable', reason = 'GitHub refused the read (403): Resource not accessible'
      WHERE task_id = 'wi_fresh'`;
    expect((await get(ana.key, "/v1/tasks/wi_fresh/branch-diff")).body).toMatchObject({
      availability: { state: "unavailable", reason: "GitHub refused the read (403): Resource not accessible" }, snapshot: null,
      capture: { state: "unavailable" } });
    await owner`UPDATE task_branch_capture_requests SET status = 'absent', reason = NULL WHERE task_id = 'wi_fresh'`;
    expect((await get(ana.key, "/v1/tasks/wi_fresh/branch-diff")).body.availability).toEqual({ state: "none", reason: "The branch has not been pushed yet." });
    expect((await get(ana.key, "/v1/tasks/wi_norepo/branch-diff")).body).toMatchObject({ repository: null, repositories: [],
      availability: { state: "none" } });
  });

  test("a newer head being captured is told beside the snapshot shown", async () => {
    await owner`UPDATE task_branch_capture_requests SET status = 'pending' WHERE task_id = 'wi_bd' AND repository_id = 'repo_api'`;
    const { body } = await get(ana.key, "/v1/tasks/wi_bd/branch-diff");
    expect(body).toMatchObject({ availability: { state: "ok" }, snapshot: { id: "tbs_c" }, capture: { state: "pending" } });
    await owner`UPDATE task_branch_capture_requests SET status = 'done' WHERE task_id = 'wi_bd' AND repository_id = 'repo_api'`;
  });

  test("branch items are placed on the snapshot asked for: on their lines, or outdated where their lines changed", async () => {
    const author = ana.personId;
    await owner`INSERT INTO people (id, organization_id, name) VALUES ('per_bo_bd', ${ORG}, 'Bo')`;
    const anchor = (over: Record<string, unknown>) => ({ kind: "code", scope: "branch", snapshotId: "tbs_b", attempt: 2, repo: "api",
      path: "src/vat.ts", side: "new", startLine: 11, endLine: 12, baseSha: sha("a"), headSha: sha("c"), targetRef: "main",
      diffChecksum: "sum-tbs_b", excerpt: [{ kind: "+", old: null, new: 11, text: "  const rate = 0.23;" },
        { kind: "+", old: null, new: 12, text: "  return n * rate;" }], ...over });
    await owner`INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, branch_snapshot_id,
        anchor, status, batch_id, submitted_at) VALUES
      ('fbi_on', ${ORG}, 'wi_bd', 2, ${author}, 'on its lines', 'code', 'tbs_b', ${anchor({})}::jsonb, 'draft', NULL, NULL),
      ('fbi_gone', ${ORG}, 'wi_bd', 2, ${author}, 'lines changed', 'code', 'tbs_b',
        ${anchor({ excerpt: [{ kind: "+", old: null, new: 11, text: "  const rate = 0.25;" }], endLine: 11 })}::jsonb, 'draft', NULL, NULL),
      ('fbi_bo', ${ORG}, 'wi_bd', 2, 'per_bo_bd', 'private', 'code', 'tbs_b', ${anchor({})}::jsonb, 'draft', NULL, NULL)`;
    const { body } = await get(ana.key, "/v1/tasks/wi_bd/branch-diff");
    // In the order written (created_at, then id).
    expect(body.placements).toEqual([
      { itemId: "fbi_gone", snapshotId: "tbs_b", at: { kind: "outdated", path: "src/vat.ts", reason: "changed" } },
      { itemId: "fbi_on", snapshotId: "tbs_b", at: { kind: "lines", path: "src/vat.ts", side: "new", startLine: 11, endLine: 12 } },
    ]);
    // Another person's draft is never placed, nor told.
    expect(JSON.stringify(body)).not.toContain("fbi_bo");
  });

  test("another organization's task, snapshot or a bad parameter is refused", async () => {
    expect((await get(cy.key, "/v1/tasks/wi_bd/branch-diff")).status).toBe(404);
    expect((await get(ana.key, "/v1/tasks/wi_o/branch-diff")).status).toBe(404);
    expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?snapshot=tbs_o")).status).toBe(404);
    expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?repository=nope")).status).toBe(400);
    expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?attempt=0")).status).toBe(400);
    expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?attempt=9")).status).toBe(404);
    expect((await get(ana.key, "/v1/tasks/wi_bd/branch-diff?snapshot=tbs_old&attempt=2")).status).toBe(400);
    // Their own task reads as theirs.
    expect((await get(cy.key, "/v1/tasks/wi_o/branch-diff")).body.snapshot.id).toBe("tbs_o");
  });
});
