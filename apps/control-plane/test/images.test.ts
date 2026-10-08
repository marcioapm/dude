/**
 * The image library through the public API: who may change images, what a
 * save refuses (a Containerfile that won't build, an unknown or looping
 * FROM image:), drafts and Build & publish, publishing an older version
 * again with no build and the rebuild it queues for images built FROM it,
 * the default base, the picker, where an image is named (roles, a
 * project's runtime and previews) and the typed images from before the
 * library, which can only be kept or cleared.
 *
 * dude-image-builder's part (a build passing) is played here by its own
 * statements: the version's user image recorded, then image_publish(),
 * as the builder calls it.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 * The tests run in order on one database and share its state.
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { BUILDER_OFFLINE_SECONDS, promptRoleSchema } from "@dude/domain";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_images_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_img";
const OTHER = "org_img_other";
const PROJECT = "prj_img";
const LAYER = "registry.example/dude/layer@sha256:" + "1".repeat(64);

function databaseUrl(appRole = false): string {
  const url = new URL(OWNER_URL);
  if (appRole) {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${NAME}`;
  return url.toString();
}

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let adminKey: string;
let memberKey: string;
let otherKey: string;
let orchestratorServer: ReturnType<typeof Bun.serve>;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any
const body = async (res: Response): Promise<Json> => res.json();

function call(key: string, method: string, path: string, payload?: unknown) {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}`, "content-type": "application/json" },
    ...(payload === undefined ? {} : { body: JSON.stringify(payload) }),
  }));
}

function configure(layer: string | null) {
  useConfig(Config.load({ env: { ...process.env,
    DUDE_ORCHESTRATOR_URL: `http://localhost:${orchestratorServer.port}`, DUDE_ORCHESTRATOR_TOKEN: "svc",
    ...(layer ? { DUDE_LAYER_IMAGE: layer } : { DUDE_LAYER_IMAGE: "" }) } }));
}

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const migrate = Bun.spawnSync(["bun", "run", "apps/control-plane/src/db/migrate.ts"], {
    cwd: ROOT, env: { ...process.env, DATABASE_URL: databaseUrl() },
  });
  if (migrate.exitCode !== 0) throw new Error(`migrate: ${migrate.stderr.toString()}`);
  owner = new SQL(databaseUrl());
  for (const id of [ORG, OTHER]) await owner`INSERT INTO organizations (id, name, slug) VALUES (${id}, ${id === ORG ? "Acme" : id}, ${id})`;
  await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix, runtime_image, preview_settings)
    VALUES (${PROJECT}, ${ORG}, 'Dashboard', 'dashboard', 'DA', 'ghcr.io/acme/old:1', '{"image": "ghcr.io/acme/preview:1"}')`;
  app = new SQL(databaseUrl(true));
  setPool(app);
  adminKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  memberKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  orchestratorServer = Bun.serve({
    port: 0,
    fetch(req) {
      const path = new URL(req.url).pathname;
      if (path.endsWith("builtin")) return Response.json(Object.fromEntries(promptRoleSchema.options.map((r) => [r, "Built-in prompt"])));
      return Response.json({ requiredReviewers: ["correctness"], blockingSeverities: ["blocking"], maxReviewIterations: 3,
        maxAttemptsPerFinding: 2, maxPrFixIterations: 3, simplify: true, test: false, parkAfterMinutes: 10, idleNudgeMinutes: 0 });
    },
  });
  configure(LAYER);
  router = buildRouter("");
});

afterAll(async () => {
  useConfig(null);
  await orchestratorServer?.stop(true);
  if (app) await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

const BASE = "FROM debian:bookworm-slim\nRUN apt-get update && apt-get install -y git\n";
const ids: Record<string, string> = {};

/** What dude-image-builder does when a build of `versionId` passes. */
async function built(versionId: string) {
  await owner`UPDATE image_versions SET user_ref = ${"registry.example/dude/custom@sha256:" + versionId.slice(-8).padStart(64, "0")},
    built_at = now() WHERE id = ${versionId}`;
  await owner`UPDATE image_builds SET state = 'succeeded', finished_at = now() WHERE image_version_id = ${versionId} AND state = 'queued'`;
  return (await owner`SELECT * FROM image_publish(${versionId})`) as Array<{ image_name: string; version: number; version_id: string }>;
}

const image = async (id: string, key = adminKey) => body(await call(key, "GET", `/v1/images/${id}`));

describe("images", () => {
  test("an admin adds one with a Containerfile: a draft, nothing published, nothing queued", async () => {
    const res = await call(adminKey, "POST", "/v1/images", { name: "acme-base", description: "Debian and git", containerfile: BASE, note: "First" });
    expect(res.status).toBe(201);
    const out = await body(res);
    ids.base = out.image.id;
    expect(out.image).toMatchObject({ name: "acme-base", published: null, pending: null, from: "debian:bookworm-slim", isDefault: false });
    expect(out.versions).toHaveLength(1);
    expect(out.versions[0]).toMatchObject({ number: null, state: "draft", note: "First", containerfile: BASE });
    expect(out.builds).toEqual([]);
  });

  test("a member reads, and changes nothing", async () => {
    const list = await body(await call(memberKey, "GET", "/v1/images"));
    expect(list.canEdit).toBe(false);
    expect(list.images.map((i: Json) => i.name)).toEqual(["acme-base"]);
    expect((await call(memberKey, "POST", "/v1/images", { name: "mine" })).status).toBe(403);
    expect((await call(memberKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: BASE })).status).toBe(403);
    expect((await call(memberKey, "POST", `/v1/images/${ids.base}/build`)).status).toBe(403);
  });

  test("another organization sees none of it, and cannot touch it", async () => {
    expect((await body(await call(otherKey, "GET", "/v1/images"))).images).toEqual([]);
    expect((await call(otherKey, "GET", `/v1/images/${ids.base}`)).status).toBe(404);
    // The other org's admin is its first key's person.
    expect((await call(otherKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: BASE })).status).toBe(404);
    expect((await call(otherKey, "POST", `/v1/images/${ids.base}/build`)).status).toBe(404);
  });

  test("a name is a slug, unique in the organization, and another organization may use it", async () => {
    expect((await call(adminKey, "POST", "/v1/images", { name: "Acme Base" })).status).toBe(400);
    const taken = await call(adminKey, "POST", "/v1/images", { name: "acme-base" });
    expect(taken.status).toBe(409);
    expect((await body(taken)).error.message).toBe("there is already an image named acme-base");
    expect((await call(otherKey, "POST", "/v1/images", { name: "acme-base" })).status).toBe(201);
  });

  test("a Containerfile that won't build is refused with each line's reason, and nothing is saved", async () => {
    const res = await call(adminKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: "FROM image:nope\nCOPY . /app\n" });
    expect(res.status).toBe(422);
    const err = (await body(res)).error;
    expect(err.code).toBe("invalid_containerfile");
    expect(err.details.problems.map((p: Json) => [p.line, p.message])).toEqual([
      [1, "There is no image named nope in the library"],
      [2, "An image has no build files: COPY only --from a stage or another image"],
    ]);
    expect((await image(ids.base!)).versions[0].containerfile).toBe(BASE);
  });

  test("a warning (a tag with no digest) does not stop a save", async () => {
    const res = await call(adminKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: BASE, note: "First", buildArgs: { A: "1" } });
    expect(res.status).toBe(200);
    expect((await body(res)).versions[0].buildArgs).toEqual({ A: "1" });
  });
});

describe("build and publish", () => {
  test("with no dude layer configured, builds are unavailable and say why", async () => {
    configure(null);
    try {
      expect((await body(await call(adminKey, "GET", "/v1/images"))).builder).toMatchObject({ available: false, layer: null });
      const res = await call(adminKey, "POST", `/v1/images/${ids.base}/build`);
      expect(res.status).toBe(503);
      expect((await body(res)).error.code).toBe("builder_unavailable");
    } finally {
      configure(LAYER);
    }
  });

  test("Build & publish numbers the draft v1 and queues it; the published version is untouched", async () => {
    const res = await call(adminKey, "POST", `/v1/images/${ids.base}/build`);
    expect(res.status).toBe(201);
    const out = await body(res);
    expect(out.version).toBe(1);
    expect(out.image.image).toMatchObject({ published: null, pending: { number: 1, state: "queued" }, draft: null });
    const list = await body(await call(adminKey, "GET", "/v1/images"));
    // No heartbeat yet: builds are on, and the builder is offline.
    expect(list.builder).toEqual({ available: true, layer: LAYER, cpus: 1.5, memoryMiB: 1536, lastSeenAt: null, offline: true });
    expect(list.queue.map((b: Json) => [b.imageName, b.version, b.kind, b.state, b.ahead])).toEqual([["acme-base", 1, "build", "queued", 0]]);
    const [event] = await owner`SELECT payload FROM events WHERE event_type = 'image.build_queued' ORDER BY cursor DESC LIMIT 1`;
    expect(event.payload).toMatchObject({ name: "acme-base", version: 1, source: "person" });
    ids.baseV1 = out.versionId;
    ids.baseBuild1 = out.buildId;
  });

  test("with no draft there is nothing to build", async () => {
    const res = await call(adminKey, "POST", `/v1/images/${ids.base}/build`);
    expect(res.status).toBe(409);
  });

  test("the build passing publishes v1", async () => {
    expect(await built(ids.baseV1!)).toEqual([]);
    const out = await image(ids.base!);
    expect(out.image.published).toMatchObject({ number: 1, versionId: ids.baseV1 });
    expect(out.image.pending).toBeNull();
    expect(out.versions[0]).toMatchObject({ number: 1, state: "published" });
  });

  test("a child FROM image:acme-base names its parent; building a base on its child is refused as a loop", async () => {
    const res = await call(adminKey, "POST", "/v1/images", {
      name: "node-pnpm", description: "pnpm and turbo", containerfile: "FROM image:acme-base\nRUN npm i -g pnpm\n",
    });
    expect(res.status).toBe(201);
    const out = await body(res);
    ids.child = out.image.id;
    expect(out.image.parents).toEqual([{ id: ids.base, name: "acme-base" }]);
    const loop = await call(adminKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: "FROM image:node-pnpm\n" });
    expect(loop.status).toBe(422);
    expect((await body(loop)).error.message).toBe("that would build acme-base on itself: acme-base → node-pnpm → acme-base");
    const self = await call(adminKey, "PUT", `/v1/images/${ids.child}/draft`, { containerfile: "FROM image:node-pnpm\n" });
    expect((await body(self)).error.details.problems[0].message).toBe("node-pnpm can't be built FROM itself");
  });

  test("a failed build leaves the published version as it was, and the failed draft with its error", async () => {
    const queued = await body(await call(adminKey, "POST", `/v1/images/${ids.child}/build`));
    ids.childV1 = queued.versionId;
    await built(ids.childV1!);
    await call(adminKey, "PUT", `/v1/images/${ids.child}/draft`, { containerfile: "FROM image:acme-base\nRUN false\n", note: "Broken" });
    const v2 = await body(await call(adminKey, "POST", `/v1/images/${ids.child}/build`));
    await owner`UPDATE image_versions SET state = 'failed', error = 'RUN false exited 1 at step 2' WHERE id = ${v2.versionId}`;
    await owner`UPDATE image_builds SET state = 'failed', error = 'RUN false exited 1 at step 2', finished_at = now() WHERE id = ${v2.buildId}`;
    const out = await image(ids.child!);
    expect(out.image.published).toMatchObject({ number: 1, versionId: ids.childV1 });
    expect(out.image.pending).toMatchObject({ number: 2, state: "failed", error: "RUN false exited 1 at step 2" });
    const picker = await body(await call(memberKey, "GET", "/v1/images/picker"));
    expect(picker.images.find((i: Json) => i.name === "node-pnpm")).toMatchObject({ version: 1, status: { kind: "failed", version: 2 } });
  });

  test("a newer Build & publish replaces a version of the same image still waiting", async () => {
    await call(adminKey, "PUT", `/v1/images/${ids.child}/draft`, { containerfile: "FROM image:acme-base\nRUN true\n" });
    const v3 = await body(await call(adminKey, "POST", `/v1/images/${ids.child}/build`));
    await call(adminKey, "PUT", `/v1/images/${ids.child}/draft`, { containerfile: "FROM image:acme-base\nRUN true && true\n" });
    const v4 = await body(await call(adminKey, "POST", `/v1/images/${ids.child}/build`));
    const [b3] = await owner`SELECT state FROM image_builds WHERE id = ${v3.buildId}`;
    const [v] = await owner`SELECT state FROM image_versions WHERE id = ${v3.versionId}`;
    expect([b3.state, v.state]).toEqual(["cancelled", "cancelled"]);
    // v4 is published by the next steps' cascade test; finish it here.
    await built(v4.versionId);
  });

  test("publishing a base queues a rebuild of each image whose published version is built FROM it", async () => {
    await call(adminKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: BASE + "RUN true\n", note: "v2" });
    const v2 = await body(await call(adminKey, "POST", `/v1/images/${ids.base}/build`));
    ids.baseV2 = v2.versionId;
    const rebuilds = await built(v2.versionId);
    expect(rebuilds.map((r) => [r.image_name, r.version])).toEqual([["node-pnpm", 5]]);
    const child = await image(ids.child!);
    expect(child.versions[0]).toMatchObject({ number: 5, state: "queued", source: "base_rebuild", note: "Rebuild on acme-base v2", createdBy: null });
    expect(child.versions[0].containerfile).toBe("FROM image:acme-base\nRUN true && true\n");
    expect(child.versions[0].parents.map((p: Json) => p.name)).toEqual(["acme-base"]);
    ids.childV5 = child.versions[0].id;
  });

  test("Publish v1 again: at once, no build, and its child is not queued twice", async () => {
    const res = await call(adminKey, "POST", `/v1/images/${ids.base}/versions/${ids.baseV1}/publish`);
    expect(res.status).toBe(200);
    const out = await body(res);
    expect(out.image.published).toMatchObject({ number: 1, versionId: ids.baseV1 });
    expect(out.versions.map((v: Json) => [v.number, v.state])).toEqual([[2, "superseded"], [1, "published"]]);
    expect(out.builds.filter((b: Json) => b.state === "queued")).toEqual([]);
    // node-pnpm v5 still waits: it resolves acme-base when it starts, and gets v1.
    const waiting = await owner`SELECT id FROM image_versions WHERE image_id = ${ids.child} AND state = 'queued'`;
    expect(waiting.map((r: Json) => r.id)).toEqual([ids.childV5]);
    const [event] = await owner`SELECT payload FROM events WHERE event_type = 'image.published' ORDER BY cursor DESC LIMIT 1`;
    expect(event.payload).toMatchObject({ version: 1, republished: true });
  });

  test("a version that never built, or is already published, cannot be published", async () => {
    const failed = (await image(ids.child!)).versions.find((v: Json) => v.number === 2);
    expect((await call(adminKey, "POST", `/v1/images/${ids.child}/versions/${failed.id}/publish`)).status).toBe(409);
    expect((await call(adminKey, "POST", `/v1/images/${ids.base}/versions/${ids.baseV1}/publish`)).status).toBe(409);
    expect((await call(memberKey, "POST", `/v1/images/${ids.base}/versions/${ids.baseV2}/publish`)).status).toBe(403);
  });

  test("a waiting build can be cancelled; its version with it", async () => {
    const [b] = await owner`SELECT id FROM image_builds WHERE image_version_id = ${ids.childV5} AND state = 'queued'`;
    expect((await call(memberKey, "POST", `/v1/images/builds/${b.id}/cancel`)).status).toBe(403);
    expect((await call(adminKey, "POST", `/v1/images/builds/${b.id}/cancel`)).status).toBe(200);
    const [v] = await owner`SELECT state FROM image_versions WHERE id = ${ids.childV5}`;
    expect(v.state).toBe("cancelled");
  });

  test("a running build, and a Run's finish, cannot be cancelled", async () => {
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref)
      VALUES ('imb_fin_test', ${ORG}, ${ids.baseV1}, 'finish', ${LAYER})`;
    const finish = await call(adminKey, "POST", "/v1/images/builds/imb_fin_test/cancel");
    expect(finish.status).toBe(409);
    expect((await body(finish)).error.message).toBe("a Run is waiting on this: it cannot be cancelled");
    await owner`UPDATE image_builds SET state = 'running' WHERE id = 'imb_fin_test'`;
    const running = await call(adminKey, "POST", "/v1/images/builds/imb_fin_test/cancel");
    expect(running.status).toBe(409);
    expect((await body(running)).error.message).toBe("only a build still waiting can be cancelled");
    const [b] = await owner`SELECT state FROM image_builds WHERE id = 'imb_fin_test'`;
    expect(b.state).toBe("running");
    await owner`UPDATE image_builds SET state = 'succeeded' WHERE id = 'imb_fin_test'`;
  });

  test("a build's page has its log, its limits, and with ?after only what came since", async () => {
    // As the builder's flushes write it: one chunk per flush at log_total.
    const chunk = async (start: number, text: string) => {
      await owner`INSERT INTO image_build_log (organization_id, build_id, start_offset, chunk) VALUES (${ORG}, ${ids.baseBuild1}, ${start}, ${text})`;
      await owner`UPDATE image_builds SET log_total = ${start} + octet_length(${text}) WHERE id = ${ids.baseBuild1}`;
    };
    const read = async (after?: number | string) =>
      body(await call(memberKey, "GET", `/v1/images/builds/${ids.baseBuild1}${after === undefined ? "" : `?after=${after}`}`));
    await chunk(0, "STEP 1/2: FROM debian\n");
    const out = await read();
    expect(out).toMatchObject({ imageName: "acme-base", version: 1, log: "STEP 1/2: FROM debian\n", logStart: 0, logTotal: 22, note: "First" });
    expect(out.builder).toMatchObject({ cpus: 1.5, memoryMiB: 1536 });
    await chunk(22, "STEP 2/2: RUN é\n");
    expect((await read()).log).toBe("STEP 1/2: FROM debian\nSTEP 2/2: RUN é\n");
    const since = await read(22);
    expect([since.log, since.logStart, since.logTotal]).toEqual(["STEP 2/2: RUN é\n", 22, 39]);
    // Inside a chunk, at a rune's first byte: the chunk is cut there.
    const inside = await read(32);
    expect([inside.log, inside.logStart]).toEqual(["RUN é\n", 32]);
    expect((await read(39)).log).toBe("");
    // 37 is inside é's two bytes: the whole log.
    const mid = await read(37);
    expect([mid.log, mid.logStart]).toEqual(["STEP 1/2: FROM debian\nSTEP 2/2: RUN é\n", 0]);
    // The first chunk trimmed: the kept log starts at 22, so 5 is gone and the whole kept log comes.
    await owner`DELETE FROM image_build_log WHERE build_id = ${ids.baseBuild1} AND start_offset = 0`;
    const cut = await read(5);
    expect([cut.log, cut.logStart, cut.logTotal]).toEqual(["STEP 2/2: RUN é\n", 22, 39]);
    // Past the end: the whole kept log.
    expect((await read(500)).logStart).toBe(22);
    expect((await call(memberKey, "GET", `/v1/images/builds/${ids.baseBuild1}?after=x`)).status).toBe(400);
    expect((await call(otherKey, "GET", `/v1/images/builds/${ids.baseBuild1}`)).status).toBe(404);
  });


  test("the builder is offline when its heartbeat is BUILDER_OFFLINE_SECONDS old", async () => {
    await owner`INSERT INTO image_builder (seen_at) VALUES (now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS - 5}))`;
    expect((await body(await call(memberKey, "GET", "/v1/images"))).builder).toMatchObject({ offline: false });
    await owner`UPDATE image_builder SET seen_at = now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS + 5})`;
    expect((await body(await call(memberKey, "GET", "/v1/images"))).builder).toMatchObject({ offline: true });
    await owner`UPDATE image_builder SET seen_at = '2026-10-01T08:00:00Z'`;
    const list = await body(await call(memberKey, "GET", "/v1/images"));
    expect(list.builder.offline).toBe(true);
    expect(Date.parse(list.builder.lastSeenAt)).toBe(Date.parse("2026-10-01T08:00:00Z"));
  });

  test("a draft can be discarded; the published version stays", async () => {
    await call(adminKey, "PUT", `/v1/images/${ids.base}/draft`, { containerfile: BASE + "RUN echo draft\n" });
    expect((await image(ids.base!)).versions[0].state).toBe("draft");
    expect((await call(memberKey, "DELETE", `/v1/images/${ids.base}/draft`)).status).toBe(403);
    const res = await call(adminKey, "DELETE", `/v1/images/${ids.base}/draft`);
    expect(res.status).toBe(200);
    const out = await body(res);
    expect(out.versions.some((v: Json) => v.state === "draft")).toBe(false);
    expect(out.image).toMatchObject({ draft: null, published: { versionId: ids.baseV1 } });
  });
});

describe("where images are named", () => {
  test("the default base: one image, or none", async () => {
    const res = await body(await call(adminKey, "POST", `/v1/images/default/${ids.base}`));
    expect(res.defaultImageId).toBe(ids.base);
    expect(res.images.find((i: Json) => i.id === ids.base).isDefault).toBe(true);
    expect((await call(memberKey, "POST", `/v1/images/default/${ids.child}`)).status).toBe(403);
    expect((await call(adminKey, "POST", `/v1/images/default/img_nope`)).status).toBe(400);
    expect((await call(memberKey, "DELETE", "/v1/images/default")).status).toBe(403);
    const none = await body(await call(adminKey, "DELETE", "/v1/images/default"));
    expect(none.defaultImageId).toBeNull();
    expect(none.images.some((i: Json) => i.isDefault)).toBe(false);
    const [org] = await owner`SELECT default_image_id FROM organizations WHERE id = ${ORG}`;
    expect(org.default_image_id).toBeNull();
    await call(adminKey, "POST", `/v1/images/default/${ids.base}`);
  });

  test("a project's runtime image by id; a typed one only cleared", async () => {
    const typed = await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { runtimeImage: "ghcr.io/acme/new:2" });
    expect(typed.status).toBe(400);
    const res = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { runtimeImageId: ids.child }));
    expect(res).toMatchObject({ runtimeImageId: ids.child, runtimeImage: "ghcr.io/acme/old:1" });
    const cleared = await body(await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { runtimeImage: null }));
    expect(cleared).toMatchObject({ runtimeImageId: ids.child, runtimeImage: null });
    // Another organization's image is no image here.
    const [theirs] = await owner`SELECT id FROM images WHERE organization_id = ${OTHER}`;
    expect((await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { runtimeImageId: theirs.id })).status).toBe(400);
  });

  test("a project's previews by id; the typed one kept as it is, or cleared", async () => {
    const put = (previews: Json) => call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, previews);
    expect((await put({ image: "ghcr.io/acme/other:9" })).status).toBe(400);
    const kept = await body(await put({ image: "ghcr.io/acme/preview:1", imageId: ids.base }));
    expect(kept).toMatchObject({ image: "ghcr.io/acme/preview:1", imageId: ids.base });
    const cleared = await body(await put({ image: null, imageId: ids.base }));
    expect(cleared).toMatchObject({ image: null, imageId: ids.base });
  });

  test("a role's image on two layers, the fixer following the implementer", async () => {
    await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { implementer: { image: ids.base } } });
    let settings = await body(await call(adminKey, "GET", "/v1/settings/organization"));
    expect(settings.roles.implementer.image).toEqual({ value: ids.base, source: "organization" });
    expect(settings.roles.fixer.image).toEqual({ value: ids.base, source: "organization", followsImplementer: true });
    await call(adminKey, "PATCH", `/v1/projects/${PROJECT}/settings`, { roles: { qa_browser: { image: ids.child } } });
    settings = await body(await call(adminKey, "GET", `/v1/projects/${PROJECT}/settings`));
    expect(settings.roles.qa_browser.image).toEqual({ value: ids.child, source: "project", organization: null });
    expect(settings.roles.implementer.image).toEqual({ value: ids.base, source: "organization", organization: ids.base });
    expect((await call(adminKey, "PATCH", "/v1/settings/organization", { roles: { reviewer: { image: "img_nope" } } })).status).toBe(400);
  });

  test("the list says who uses each image", async () => {
    const list = await body(await call(adminKey, "GET", "/v1/images"));
    const uses = (name: string) => list.images.find((i: Json) => i.name === name).usedBy.map((u: Json) => [u.kind, u.role ?? u.image?.name ?? u.project?.name ?? null]);
    expect(uses("acme-base")).toEqual([["organization_default", null], ["role", "implementer"], ["preview", "Dashboard"], ["child", "node-pnpm"]]);
    expect(uses("node-pnpm")).toEqual([["project_role", "qa_browser"], ["runtime", "Dashboard"]]);
  });

  test("an archived image is hidden from new choices but keeps working where it is named", async () => {
    await call(adminKey, "PATCH", `/v1/images/${ids.child}`, { archived: true });
    const picker = await body(await call(memberKey, "GET", "/v1/images/picker"));
    expect(picker.images.find((i: Json) => i.name === "node-pnpm").archived).toBe(true);
    // The project already names it: saving again keeps it.
    expect((await call(adminKey, "PATCH", `/v1/projects/${PROJECT}`, { runtimeImageId: ids.child })).status).toBe(200);
    // A new reference is refused.
    const res = await call(adminKey, "PUT", `/v1/projects/${PROJECT}/preview-settings`, { imageId: ids.child });
    expect(res.status).toBe(400);
    expect((await body(res)).error.message).toBe("node-pnpm is archived: pick another image");
    await call(adminKey, "PATCH", `/v1/images/${ids.child}`, { archived: false });
  });
});

describe("a Run waiting for its image", () => {
  const runOf = async (id: string) => body(await call(memberKey, "GET", `/v1/runs/${id}`));

  test("says what it waits on while it waits, and that the builder is offline since its last heartbeat", async () => {
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('tsk_img', ${ORG}, ${PROJECT}, 1, 't')`;
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind, layer_ref)
      VALUES ('imb_wait', ${ORG}, ${ids.baseV1}, 'finish', ${"registry.example/dude/layer@sha256:" + "2".repeat(64)})`;
    // A phase Run pending, and a woken preview, asleep (paused) with no lux Run.
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, image_build_id, image_waiting_since)
      VALUES ('run_img_phase', ${ORG}, ${PROJECT}, 'tsk_img', 1, 'pending', 'agent', 'imb_wait', now()),
             ('run_img_prev', ${ORG}, ${PROJECT}, 'tsk_img', 1, 'paused', 'preview', 'imb_wait', now())`;
    await owner`UPDATE image_builder SET seen_at = now()`;
    for (const id of ["run_img_phase", "run_img_prev"]) {
      expect((await runOf(id)).preparingImage).toEqual({ buildId: "imb_wait", state: "queued", kind: "finish", imageName: "acme-base", version: 1, builderOfflineSince: null });
    }
    await owner`UPDATE image_builder SET seen_at = '2026-10-01T08:00:00Z'`;
    expect(Date.parse((await runOf("run_img_prev")).preparingImage.builderOfflineSince)).toBe(Date.parse("2026-10-01T08:00:00Z"));
    // Seen BUILDER_OFFLINE_SECONDS ago less a little: not yet offline; a little more: offline.
    await owner`UPDATE image_builder SET seen_at = now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS - 5})`;
    expect((await runOf("run_img_prev")).preparingImage.builderOfflineSince).toBeNull();
    await owner`UPDATE image_builder SET seen_at = now() - make_interval(secs => ${BUILDER_OFFLINE_SECONDS + 5})`;
    expect((await runOf("run_img_prev")).preparingImage.builderOfflineSince).not.toBeNull();
  });

  test("a builder that never reported in is offline since the Run began to wait", async () => {
    const saved = await owner`SELECT seen_at, version FROM image_builder`;
    await owner`DELETE FROM image_builder`;
    try {
      await owner`UPDATE runs SET image_waiting_since = '2026-10-01T09:30:00Z' WHERE id = 'run_img_prev'`;
      expect(Date.parse((await runOf("run_img_prev")).preparingImage.builderOfflineSince)).toBe(Date.parse("2026-10-01T09:30:00Z"));
    } finally {
      for (const r of saved) await owner`INSERT INTO image_builder (seen_at, version) VALUES (${r.seen_at}, ${r.version})`;
    }
  });

  test("a Run waiting on its image's first version says it is building, not adding the layer", async () => {
    await owner`INSERT INTO image_versions (id, organization_id, image_id, number, containerfile, state)
      VALUES ('imv_first', ${ORG}, ${ids.child}, 99, 'FROM debian', 'queued')`;
    await owner`INSERT INTO image_builds (id, organization_id, image_version_id, kind) VALUES ('imb_first', ${ORG}, 'imv_first', 'build')`;
    await owner`INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, kind, image_build_id, image_waiting_since)
      VALUES ('run_img_first', ${ORG}, ${PROJECT}, 'tsk_img', 2, 'pending', 'agent', 'imb_first', now())`;
    expect((await runOf("run_img_first")).preparingImage).toMatchObject({ buildId: "imb_first", kind: "build", imageName: "node-pnpm", version: 99 });
    await owner`UPDATE image_builds SET state = 'cancelled' WHERE id = 'imb_first'`;
    await owner`UPDATE image_versions SET state = 'cancelled' WHERE id = 'imv_first'`;
    await owner`UPDATE runs SET status = 'failed' WHERE id = 'run_img_first'`;
  });

  test("not once the job is over, or the Run has a lux Run or ended", async () => {
    await owner`UPDATE runs SET lux_run_id = 'lux_1' WHERE id = 'run_img_phase'`;
    expect((await runOf("run_img_phase")).preparingImage).toBeNull();
    await owner`UPDATE runs SET status = 'failed' WHERE id = 'run_img_prev'`;
    expect((await runOf("run_img_prev")).preparingImage).toBeNull();
    await owner`UPDATE runs SET status = 'paused' WHERE id = 'run_img_prev'`;
    await owner`UPDATE image_builds SET state = 'succeeded' WHERE id = 'imb_wait'`;
    expect((await runOf("run_img_prev")).preparingImage).toBeNull();
  });
});

describe("can run containers", () => {
  const PODMAN = "FROM debian:bookworm-slim\nRUN apt-get install -y git podman fuse-overlayfs uidmap\n";

  test("is saved with the draft, and a new image starts with it off", async () => {
    const created = await body(await call(adminKey, "POST", "/v1/images", { name: "agents-podman", containerfile: PODMAN }));
    ids.podman = created.image.id;
    expect(created.versions[0]).toMatchObject({ state: "draft", canRunContainers: false });
    const on = await body(await call(adminKey, "PUT", `/v1/images/${ids.podman}/draft`, { containerfile: PODMAN, canRunContainers: true }));
    expect(on.versions[0].canRunContainers).toBe(true);
    // A save that does not name it keeps the draft's own.
    const kept = await body(await call(adminKey, "PUT", `/v1/images/${ids.podman}/draft`, { containerfile: PODMAN + "RUN true\n" }));
    expect(kept.versions[0].canRunContainers).toBe(true);
  });

  test("each numbered version keeps its own; the image, the picker and builds show the published one's", async () => {
    const v1 = await body(await call(adminKey, "POST", `/v1/images/${ids.podman}/build`));
    expect(v1.image.builds[0]).toMatchObject({ canRunContainers: true, containersCheck: null });
    await built(v1.versionId);
    let out = await image(ids.podman!);
    expect(out.image.published).toMatchObject({ number: 1, canRunContainers: true });
    let picker = await body(await call(memberKey, "GET", "/v1/images/picker"));
    expect(picker.images.find((i: Json) => i.name === "agents-podman").canRunContainers).toBe(true);
    // v2 turns it off; v1 still can.
    await call(adminKey, "PUT", `/v1/images/${ids.podman}/draft`, { containerfile: PODMAN, canRunContainers: false });
    const v2 = await body(await call(adminKey, "POST", `/v1/images/${ids.podman}/build`));
    await built(v2.versionId);
    out = await image(ids.podman!);
    expect(out.versions.map((v: Json) => [v.number, v.canRunContainers])).toEqual([[2, false], [1, true]]);
    expect(out.image.published.canRunContainers).toBe(false);
    picker = await body(await call(memberKey, "GET", "/v1/images/picker"));
    expect(picker.images.find((i: Json) => i.name === "agents-podman").canRunContainers).toBe(false);
    // Publishing v1 again brings its value back.
    await call(adminKey, "POST", `/v1/images/${ids.podman}/versions/${v1.versionId}/publish`);
    expect((await image(ids.podman!)).image.published).toMatchObject({ number: 1, canRunContainers: true });
  });

  test("a new draft starts from the published version's value", async () => {
    const draft = await body(await call(adminKey, "PUT", `/v1/images/${ids.podman}/draft`, { containerfile: PODMAN + "RUN echo 3\n" }));
    expect(draft.versions[0]).toMatchObject({ state: "draft", canRunContainers: true });
    await call(adminKey, "DELETE", `/v1/images/${ids.podman}/draft`);
  });

  test("a new image FROM image:<x> starts from x's published value; a FROM of one that cannot does not", async () => {
    const child = await body(await call(adminKey, "POST", "/v1/images", { name: "abs-preview", containerfile: "FROM image:agents-podman\nRUN true\n" }));
    expect(child.versions[0].canRunContainers).toBe(true);
    const plain = await body(await call(adminKey, "POST", "/v1/images", { name: "plain-child", containerfile: "FROM image:acme-base\n" }));
    expect(plain.versions[0].canRunContainers).toBe(false);
    // Named on create, it wins.
    const named = await body(await call(adminKey, "POST", "/v1/images", { name: "named-off", containerfile: "FROM image:agents-podman\n", canRunContainers: false }));
    expect(named.versions[0].canRunContainers).toBe(false);
  });

  test("a base rebuild keeps the child's published value", async () => {
    const child = (await body(await call(adminKey, "GET", "/v1/images"))).images.find((i: Json) => i.name === "abs-preview");
    const v1 = await body(await call(adminKey, "POST", `/v1/images/${child.id}/build`));
    await built(v1.versionId);
    await call(adminKey, "PUT", `/v1/images/${ids.podman}/draft`, { containerfile: PODMAN + "RUN echo 4\n" });
    const base = await body(await call(adminKey, "POST", `/v1/images/${ids.podman}/build`));
    const rebuilds = await built(base.versionId);
    expect(rebuilds.map((r) => r.image_name)).toEqual(["abs-preview"]);
    const [v] = await owner`SELECT can_run_containers FROM image_versions WHERE id = ${rebuilds[0]!.version_id}`;
    expect(v.can_run_containers).toBe(true);
  });
});
