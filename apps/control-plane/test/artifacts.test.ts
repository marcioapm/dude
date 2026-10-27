/**
 * What an artifact is served as: its recorded type when that says
 * something, else its name's, else its first bytes'. And, against a
 * database of its own and a stand-in orchestrator, how a file is listed
 * with the thumbnail dude publish made of it, and how its bytes are cached.
 * Requires DATABASE_URL: a role that can create databases.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import type { Router } from "../src/api/router.ts";
import { authenticate, createApiKey } from "../src/api/auth.ts";
import { Config, useConfig } from "../src/config.ts";
import { artifactType, sniffBytes } from "../src/api/routes/artifacts.ts";

test("a recorded type that says something is kept", () => {
  expect(artifactType("image/png", "shot.bin")).toBe("image/png");
  expect(artifactType("text/markdown; charset=utf-8", "x")).toBe("text/markdown; charset=utf-8");
});

test("a generic type gives way to the name", () => {
  expect(artifactType("application/octet-stream", "demo.mp4")).toBe("video/mp4");
  expect(artifactType("text/plain", "coverage.HTML")).toBe("text/html");
  expect(artifactType("", "run.webm")).toBe("video/webm");
  expect(artifactType("text/plain", "notes.txt")).toBe("text/plain");
  expect(artifactType(null, "blob")).toBe("application/octet-stream");
});

test("the bytes say what a name does not", () => {
  const mp4 = new Uint8Array([0, 0, 0, 0x18, ...new TextEncoder().encode("ftypisom"), 0, 0]);
  expect(sniffBytes(mp4)).toBe("video/mp4");
  expect(sniffBytes(new Uint8Array([0x1a, 0x45, 0xdf, 0xa3, 1]))).toBe("video/webm");
  expect(sniffBytes(new TextEncoder().encode("\n  <!DOCTYPE html><html>"))).toBe("text/html");
  expect(sniffBytes(new TextEncoder().encode("# Notes"))).toBeNull();
});

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_artifacts_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_art";
const SHA = "a".repeat(64);
const OTHER_SHA = "b".repeat(64);

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

type Key = { key: string; id: string; personId: string };
let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let marcio: Key;
let orchestrator: ReturnType<typeof Bun.serve>;
/** The ids the stand-in orchestrator was asked for the bytes of. */
const read: string[] = [];

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl("owner", NAME) },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl("owner", NAME));
  await owner`INSERT INTO organizations (id, name, slug) VALUES (${ORG}, ${ORG}, ${ORG})`;
  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  marcio = await createApiKey({ organizationId: ORG, name: "Márcio" });

  await owner.begin(async (tx) => {
    await tx`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_art', ${ORG}, 'Art', 'art', 'ART')`;
    await tx`INSERT INTO tasks (id, organization_id, project_id, number, title, goal)
      VALUES ('wi_one', ${ORG}, 'prj_art', 1, 'One', 'g'), ('wi_two', ${ORG}, 'prj_art', 2, 'Two', 'g')`;
    await tx`INSERT INTO sessions (id, organization_id, title) VALUES ('ssn_art', ${ORG}, 'Ideas')`;
    await tx`INSERT INTO session_people (session_id, person_id, organization_id, role, accepted_at)
      VALUES ('ssn_art', ${marcio.personId}, ${ORG}, 'owner', now())`;
    await tx`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, role, kind)
      VALUES ('run_one_a', ${ORG}, 'prj_art', 'wi_one', 1, 'implementer', 'agent'),
             ('run_one_b', ${ORG}, 'prj_art', 'wi_one', 1, 'qa_browser', 'agent'),
             ('run_two', ${ORG}, 'prj_art', 'wi_two', 1, 'qa_browser', 'agent')`;
    await tx`INSERT INTO runs (id, organization_id, session_id, attempt, role, kind)
      VALUES ('run_ssn', ${ORG}, 'ssn_art', 1, 'brainstorm', 'agent')`;
    const file = (id: string, run: string, name: string, type: string, sha: string, ago: number) =>
      tx`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, size_bytes, storage_key, sha256, description, created_at)
        VALUES (${id}, ${ORG}, ${run}, 'published', ${name}, ${type}, 10, ${"k/" + id}, ${sha}, '', now() - ${ago} * interval '1 minute')`;
    // Task one: a video whose thumbnail its tester published, twice (the
    // newest is linked), from another Run of the task than the video's.
    await file("art_video", "run_one_a", "walkthrough.webm", "video/webm", SHA, 30);
    await file("art_thumb_old", "run_one_b", `.dude-thumbs/${SHA}.jpg`, "image/jpeg", "t1", 20);
    await file("art_thumb", "run_one_b", `.dude-thumbs/${SHA}.jpg`, "image/jpeg", "t2", 10);
    await file("art_notes", "run_one_a", "notes.md", "text/markdown", "c".repeat(64), 5);
    // Task two: the same bytes as task one's thumbnail names, none of its own.
    await file("art_two_video", "run_two", "walkthrough.webm", "video/webm", SHA, 30);
    // And a thumbnail of bytes task one has, which task one must not see.
    await file("art_two_thumb", "run_two", `.dude-thumbs/${OTHER_SHA}.jpg`, "image/jpeg", "t3", 1);
    await file("art_one_shot", "run_one_a", "shot.png", "image/png", OTHER_SHA, 40);
    // The session: a picture with its thumbnail, and one whose bytes only
    // task one has a thumbnail of.
    await file("art_ssn_shot", "run_ssn", "board.png", "image/png", OTHER_SHA, 10);
    await file("art_ssn_thumb", "run_ssn", `.dude-thumbs/${OTHER_SHA}.jpg`, "image/jpeg", "t4", 9);
    await file("art_ssn_video", "run_ssn", "demo.webm", "video/webm", SHA, 8);
  });

  orchestrator = Bun.serve({ port: 0, fetch(request) {
    const path = new URL(request.url).pathname;
    const id = decodeURIComponent(path.split("/")[3] ?? "");
    read.push(id);
    if (id === "art_gone") return Response.json({ error: "gone" }, { status: 410 });
    return new Response(`bytes of ${id}`, { headers: { "content-type": "application/octet-stream" } });
  } });
  useConfig(Config.load({ env: { ...process.env, DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestrator.port}`, DUDE_ORCHESTRATOR_TOKEN: "t" } }));
  router = buildRouter("", async (credential) => credential === null ? null : authenticate(credential));
});

afterAll(async () => {
  orchestrator?.stop(true);
  useConfig(null);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

function get(path: string): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, { headers: { authorization: `Bearer ${marcio.key}` } }));
}

async function listed(query: string): Promise<Array<{ id: string; name: string; thumbnailId: string | null }>> {
  const res = await get(`/v1/artifacts?${query}`);
  expect(res.status).toBe(200);
  return ((await res.json()) as { artifacts: Array<{ id: string; name: string; thumbnailId: string | null }> }).artifacts;
}

test("a file's thumbnail is the newest of its bytes in its own task, never another task's", async () => {
  const one = await listed("taskId=wi_one");
  expect(one.map((a) => [a.id, a.thumbnailId])).toEqual([
    ["art_notes", null], ["art_video", "art_thumb"], ["art_one_shot", null],
  ]);
  const two = await listed("taskId=wi_two");
  expect(two.map((a) => [a.id, a.thumbnailId])).toEqual([["art_two_video", null]]);
});

test("a session's file's thumbnail is the session's own", async () => {
  const files = await listed("sessionId=ssn_art");
  expect(files.map((a) => [a.id, a.thumbnailId])).toEqual([
    ["art_ssn_video", null], ["art_ssn_shot", "art_ssn_thumb"],
  ]);
});

test("thumbnails are neither listed nor in either zip", async () => {
  for (const query of ["taskId=wi_one", "taskId=wi_two", "sessionId=ssn_art"]) {
    expect((await listed(query)).filter((a) => a.name.startsWith(".dude-"))).toEqual([]);
  }
  for (const [path, want] of [
    ["/v1/tasks/wi_one/artifacts.zip", ["art_notes", "art_one_shot", "art_video"]],
    ["/v1/brainstorms/ssn_art/artifacts.zip", ["art_ssn_shot", "art_ssn_video"]],
  ] as const) {
    read.length = 0;
    const res = await get(path);
    expect(res.status).toBe(200);
    const zip = new TextDecoder().decode(await res.arrayBuffer());
    expect(zip).not.toContain(".dude-thumbs");
    expect([...read].sort()).toEqual([...want]);
  }
});

test("an artifact's bytes are cached for good; a refusal is not cached", async () => {
  const res = await get("/v1/artifacts/art_thumb/content");
  expect(res.status).toBe(200);
  expect(await res.text()).toBe("bytes of art_thumb");
  expect(res.headers.get("cache-control")).toBe("private, max-age=31536000, immutable");

  await owner`INSERT INTO artifacts (id, organization_id, run_id, kind, name, content_type, size_bytes, storage_key, sha256, description)
    VALUES ('art_gone', ${ORG}, 'run_one_a', 'published', 'gone.md', 'text/markdown', 1, 'k/gone', 'x', '')`;
  const gone = await get("/v1/artifacts/art_gone/content");
  expect(gone.status).toBe(410);
  expect(gone.headers.get("cache-control")).toBeNull();
  const missing = await get("/v1/artifacts/art_nobody/content");
  expect(missing.status).toBe(404);
  expect(missing.headers.get("cache-control")).toBeNull();
});
