/**
 * Images a person sends an agent: upload, serving, removal and the
 * sweeper, through the public API against a database of its own and a
 * stand-in S3 that keeps objects in memory.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, describe, expect, test } from "bun:test";
import { SQL } from "bun";
import { join } from "node:path";
import { closePool, setPool } from "../src/db/client.ts";
import { buildRouter } from "../src/index.ts";
import { Config, useConfig } from "../src/config.ts";
import type { Router } from "../src/api/router.ts";
import { createApiKey } from "../src/api/auth.ts";
import { SWEEP_BATCH, sweepAttachments } from "../src/sweeper.ts";
import { imageInfo } from "../src/images.ts";
import { attachmentName } from "../src/api/routes/attachments.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const ROOT = join(import.meta.dir, "../../..");
const NAME = `dude_attachments_test_${Bun.randomUUIDv7("hex").slice(-12)}`;
const ORG = "org_att";
const OTHER = "org_att_other";

function databaseUrl(user: string, name: string): string {
  const url = new URL(OWNER_URL);
  if (user === "app") {
    url.username = "dude_app";
    url.password = "dude_app";
  }
  url.pathname = `/${name}`;
  return url.toString();
}

// ---- image bytes ---------------------------------------------------------

function png(width: number, height: number, pad = 0): Uint8Array {
  const b = new Uint8Array(33 + pad);
  b.set([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0, 0, 0, 13, 0x49, 0x48, 0x44, 0x52]);
  new DataView(b.buffer).setUint32(16, width);
  new DataView(b.buffer).setUint32(20, height);
  return b;
}

function jpeg(width: number, height: number): Uint8Array {
  // SOI, an APP0 segment, then SOF0 with the size.
  const b = [0xff, 0xd8, 0xff, 0xe0, 0x00, 0x04, 0x00, 0x00, 0xff, 0xc0, 0x00, 0x11, 0x08,
    height >> 8, height & 0xff, width >> 8, width & 0xff, 3, 1, 0x22, 0, 2, 0x11, 1, 3, 0x11, 1, 0xff, 0xd9];
  return new Uint8Array(b);
}

function webp(width: number, height: number): Uint8Array {
  const b = new Uint8Array(30);
  b.set([..."RIFF"].map((c) => c.charCodeAt(0)), 0);
  b.set([..."WEBPVP8X"].map((c) => c.charCodeAt(0)), 8);
  const w = width - 1;
  const h = height - 1;
  b.set([w & 0xff, (w >> 8) & 0xff, (w >> 16) & 0xff, h & 0xff, (h >> 8) & 0xff, (h >> 16) & 0xff], 24);
  return b;
}

function gif(width: number, height: number): Uint8Array {
  const b = new Uint8Array(13);
  b.set([..."GIF89a"].map((c) => c.charCodeAt(0)));
  b.set([width & 0xff, width >> 8, height & 0xff, height >> 8], 6);
  return b;
}

// ---- stand-in S3 ---------------------------------------------------------

const objects = new Map<string, { bytes: Uint8Array; type: string | null }>();
let s3Down = false;
const s3 = Bun.serve({
  hostname: "127.0.0.1",
  port: 0,
  async fetch(req) {
    const key = decodeURIComponent(new URL(req.url).pathname).replace(/^\/[^/]+\//, "");
    if (s3Down) return new Response("<Error><Code>ServiceUnavailable</Code></Error>", { status: 503 });
    if (req.method === "PUT") {
      objects.set(key, { bytes: new Uint8Array(await req.arrayBuffer()), type: req.headers.get("content-type") });
      return new Response(null, { status: 200 });
    }
    if (req.method === "DELETE") {
      objects.delete(key);
      return new Response(null, { status: 204 });
    }
    const o = objects.get(key);
    return o ? new Response(o.bytes) : new Response("<Error><Code>NoSuchKey</Code></Error>", { status: 404 });
  },
});

const STORAGE = {
  DUDE_S3_BUCKET: "att-test",
  DUDE_S3_ENDPOINT: `http://127.0.0.1:${s3.port}`,
  DUDE_S3_ACCESS_KEY: "test",
  DUDE_S3_SECRET_KEY: "test-secret",
};

// ---- setup ---------------------------------------------------------------

let admin: SQL;
let owner: SQL;
let app: SQL;
let router: Router;
let anaKey: string;
let boKey: string;
let otherKey: string;
let taskId: string;
let otherTaskId: string;

type Json = any; // eslint-disable-line @typescript-eslint/no-explicit-any

function call(key: string, method: string, path: string, body?: string): Promise<Response> {
  return router.handle(new Request(`http://dude.test${path}`, {
    method,
    headers: { authorization: `Bearer ${key}` },
    ...(body === undefined ? {} : { body }),
  }));
}

function form(original: Uint8Array, originalType: string, delivered: Uint8Array, deliveredType: string, name = "shot.png"): FormData {
  const f = new FormData();
  f.set("original", new File([original], name, { type: originalType }));
  f.set("originalType", originalType);
  f.set("delivered", new File([delivered], name, { type: deliveredType }));
  f.set("deliveredType", deliveredType);
  return f;
}

async function upload(key: string, task: string, f: FormData): Promise<Response> {
  // Encoded as a browser sends it: the boundary in the content type.
  const encoded = new Response(f);
  return router.handle(new Request(`http://dude.test/v1/tasks/${task}/attachments`, {
    method: "POST",
    headers: { authorization: `Bearer ${key}`, "content-type": encoded.headers.get("content-type")! },
    body: await encoded.arrayBuffer(),
  }));
}

function configure(storage: boolean): void {
  useConfig(Config.load({ env: { ...process.env, ...(storage ? STORAGE : { DUDE_S3_BUCKET: "" }) } }));
  router = buildRouter("");
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
  for (const [project, org] of [["prj_att", ORG], ["prj_att_other", OTHER]] as const) {
    await owner`INSERT INTO projects (id, organization_id, name, slug, key_prefix, runtime_image)
                VALUES (${project}, ${org}, ${project}, ${project}, 'ATT', 'node:22')`;
  }
  taskId = "wi_att_1";
  otherTaskId = "wi_att_other";
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES (${taskId}, ${ORG}, 'prj_att', 1, 'Fix VAT')`;
  await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES (${otherTaskId}, ${OTHER}, 'prj_att_other', 1, 'Other')`;

  app = new SQL(databaseUrl("app", NAME));
  setPool(app);
  anaKey = (await createApiKey({ organizationId: ORG, name: "Ana" })).key;
  boKey = (await createApiKey({ organizationId: ORG, name: "Bo" })).key;
  otherKey = (await createApiKey({ organizationId: OTHER, name: "Cy" })).key;
  configure(true);
});

afterAll(async () => {
  useConfig(null);
  await s3.stop(true);
  await closePool(app);
  await owner?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

// ---- tests ---------------------------------------------------------------

describe("reading an image's header", () => {
  test("type and size come from the bytes of each of the four types", () => {
    expect(imageInfo(png(2400, 1520))).toEqual({ type: "image/png", width: 2400, height: 1520 });
    expect(imageInfo(jpeg(1200, 760))).toEqual({ type: "image/jpeg", width: 1200, height: 760 });
    expect(imageInfo(webp(900, 900))).toEqual({ type: "image/webp", width: 900, height: 900 });
    expect(imageInfo(gif(390, 844))).toEqual({ type: "image/gif", width: 390, height: 844 });
  });

  test("anything else is not an image, SVG included", () => {
    expect(imageInfo(new TextEncoder().encode("<svg xmlns='http://www.w3.org/2000/svg'/>"))).toBeNull();
    expect(imageInfo(new TextEncoder().encode("%PDF-1.7"))).toBeNull();
    expect(imageInfo(new Uint8Array([0x89, 0x50]))).toBeNull();
  });

  test("a name lux takes: no path, no control characters, the delivered type's extension", () => {
    expect(attachmentName("../etc/passwd.png", "image/webp")).toBe(".. etc passwd.webp");
    expect(attachmentName("a\u0000b\nc.JPG", "image/jpeg")).toBe("a b c.jpg");
    expect(attachmentName("", "image/png")).toBe("image.png");
    const long = attachmentName("é".repeat(300), "image/png");
    expect(new TextEncoder().encode(long).length).toBeLessThanOrEqual(255);
    expect(long.endsWith(".png")).toBe(true);
  });
});

describe("uploading", () => {
  test("stores both variants and answers what the agent will get", async () => {
    const res = await upload(anaKey, taskId, form(png(2400, 1520, 1000), "image/png", jpeg(1200, 760), "image/jpeg", "checkout-yearly.png"));
    expect(res.status).toBe(201);
    const a: Json = await res.json();
    expect(a).toMatchObject({
      name: "checkout-yearly.jpg", contentType: "image/jpeg", width: 1200, height: 760, bytes: jpeg(1200, 760).length,
      original: { contentType: "image/png", width: 2400, height: 1520, bytes: 1033 },
    });
    expect(a.id).toMatch(/^att_/);
    const [row] = await owner`SELECT object_key, original_key, sha256, uploaded_by IS NOT NULL AS by FROM attachments WHERE id = ${a.id}`;
    expect(objects.get(row.object_key)?.bytes).toEqual(jpeg(1200, 760));
    expect(objects.get(row.original_key)?.bytes.length).toBe(1033);
    expect(row.sha256).toBe(new Bun.CryptoHasher("sha256").update(jpeg(1200, 760)).digest("hex"));
    expect(row.by).toBe(true);
  });

  test("a file whose bytes are another type than it claims is refused", async () => {
    const res = await upload(anaKey, taskId, form(png(10, 10), "image/png", png(10, 10), "image/jpeg"));
    expect(res.status).toBe(400);
    expect(((await res.json()) as Json).error.message).toBe("delivered: the file says image/jpeg but is image/png");
  });

  test("bytes that are no image, or a type that is not one of the four, are refused", async () => {
    const svg = new TextEncoder().encode("<svg xmlns='http://www.w3.org/2000/svg'/>");
    const named = await upload(anaKey, taskId, form(svg, "image/png", png(1, 1), "image/png"));
    expect(named.status).toBe(400);
    expect(((await named.json()) as Json).error.message).toBe("original: the file is not a PNG, JPEG, WebP or GIF image");
    const res = await upload(anaKey, taskId, form(svg, "image/svg+xml", png(1, 1), "image/png"));
    expect(res.status).toBe(400);
    expect(((await res.json()) as Json).error.message).toBe("original: an image is PNG, JPEG, WebP or GIF, not image/svg+xml");
    const pdf = await upload(anaKey, taskId, form(new TextEncoder().encode("%PDF-1.7"), "application/pdf", png(1, 1), "image/png"));
    expect(pdf.status).toBe(400);
    expect(((await pdf.json()) as Json).error.message).toBe("original: an image is PNG, JPEG, WebP or GIF, not application/pdf");
  });

  test("an original over 10 MB, a delivered variant over 4.5 MiB or 2000 px are refused", async () => {
    const big = await upload(anaKey, taskId, form(png(4000, 3000, 10_000_000), "image/png", png(10, 10), "image/png"));
    expect(big.status).toBe(400);
    expect(((await big.json()) as Json).error.message).toBe("original: an image is at most 10 MB");

    const heavy = await upload(anaKey, taskId, form(png(10, 10), "image/png", png(1000, 1000, 4.5 * 1024 * 1024), "image/png"));
    expect(heavy.status).toBe(400);
    expect(((await heavy.json()) as Json).error.message).toBe("delivered: what the agent is sent is at most 4.5 MiB");

    const wide = await upload(anaKey, taskId, form(png(10, 10), "image/png", png(2001, 100), "image/png"));
    expect(wide.status).toBe(400);
    expect(((await wide.json()) as Json).error.message).toBe("delivered: what the agent is sent is at most 2000 px on its long side");

    // At the limits, it is taken.
    const edge = await upload(anaKey, taskId, form(png(2000, 100, 10_000_000 - 33), "image/png", png(2000, 100, Math.floor(4.5 * 1024 * 1024) - 33), "image/png"));
    expect(edge.status).toBe(201);
  });

  test("a form missing a variant is refused", async () => {
    const f = new FormData();
    f.set("original", new File([png(1, 1)], "a.png", { type: "image/png" }));
    f.set("originalType", "image/png");
    const res = await upload(anaKey, taskId, f);
    expect(res.status).toBe(400);
    expect(((await res.json()) as Json).error.message).toBe("the form has no delivered file");
  });

  test("another organization's task is not found", async () => {
    expect((await upload(anaKey, otherTaskId, form(png(1, 1), "image/png", png(1, 1), "image/png"))).status).toBe(404);
  });

  test("without a bucket, an upload answers 503 and the limits say images are off", async () => {
    configure(false);
    try {
      const res = await upload(anaKey, taskId, form(png(1, 1), "image/png", png(1, 1), "image/png"));
      expect(res.status).toBe(503);
      expect(((await res.json()) as Json).error.code).toBe("storage_unconfigured");
      const limits: Json = await (await call(anaKey, "GET", "/v1/attachment-limits")).json();
      expect(limits).toMatchObject({ enabled: false, perMessage: 6, originalBytes: 10_000_000, maxSide: 2000 });
    } finally {
      configure(true);
    }
    expect(((await (await call(anaKey, "GET", "/v1/attachment-limits")).json()) as Json).enabled).toBe(true);
  });
});

describe("serving", () => {
  let id: string;
  beforeAll(async () => {
    id = ((await (await upload(anaKey, taskId, form(png(2400, 1520), "image/png", webp(1200, 760), "image/webp", "Summary v3.png"))).json()) as Json).id;
  });

  test("streams each variant with its type, its name, and nothing a browser would sniff or run", async () => {
    const res = await call(boKey, "GET", `/v1/attachments/${id}?variant=delivered`);
    expect(res.status).toBe(200);
    expect(res.headers.get("content-type")).toBe("image/webp");
    expect(res.headers.get("x-content-type-options")).toBe("nosniff");
    expect(res.headers.get("content-security-policy")).toBe("default-src 'none'");
    expect(res.headers.get("content-disposition")).toBe(`inline; filename="Summary v3.webp"; filename*=UTF-8''Summary%20v3.webp`);
    expect([...new Uint8Array(await res.arrayBuffer())]).toEqual([...webp(1200, 760)]);

    const original = await call(boKey, "GET", `/v1/attachments/${id}?variant=original&download=1`);
    expect(original.headers.get("content-type")).toBe("image/png");
    expect(original.headers.get("content-disposition")).toStartWith(`attachment; filename="Summary v3.png"`);
    expect([...new Uint8Array(await original.arrayBuffer())]).toEqual([...png(2400, 1520)]);
  });

  test("another organization's member cannot read it", async () => {
    expect((await call(otherKey, "GET", `/v1/attachments/${id}`)).status).toBe(404);
    expect((await call(otherKey, "GET", `/v1/attachments/${id}?variant=original`)).status).toBe(404);
    expect((await call(otherKey, "DELETE", `/v1/attachments/${id}`)).status).toBe(404);
  });

  test("an unknown variant is refused", async () => {
    expect((await call(anaKey, "GET", `/v1/attachments/${id}?variant=raw`)).status).toBe(400);
  });
});

describe("removing an unsent one", () => {
  test("only its uploader may, and its objects are queued for deletion", async () => {
    const a: Json = await (await upload(anaKey, taskId, form(png(5, 5), "image/png", png(5, 5), "image/png"))).json();
    expect((await call(boKey, "DELETE", `/v1/attachments/${a.id}`)).status).toBe(403);
    const [row] = await owner`SELECT object_key, original_key FROM attachments WHERE id = ${a.id}`;
    expect((await call(anaKey, "DELETE", `/v1/attachments/${a.id}`)).status).toBe(204);
    const queued = (await owner`SELECT object_key FROM attachment_object_deletions
      WHERE object_key IN (${row.object_key}, ${row.original_key})`).map((r: { object_key: string }) => r.object_key);
    expect(queued.sort()).toEqual([row.object_key, row.original_key].sort());
  });

  test("once sent, it stays with its message", async () => {
    const a: Json = await (await upload(anaKey, taskId, form(png(5, 5), "image/png", png(5, 5), "image/png"))).json();
    await owner`UPDATE attachments SET for_prompt = true, attached_at = now() WHERE id = ${a.id}`;
    expect((await call(anaKey, "DELETE", `/v1/attachments/${a.id}`)).status).toBe(409);
  });
});

describe("the sweeper", () => {
  /** Which of `keys` are still queued for deletion. */
  const stillQueued = async (keys: string[]) =>
    ((await owner`SELECT object_key FROM attachment_object_deletions WHERE object_key IN ${owner(keys)}`) as Array<{ object_key: string }>)
      .map((r) => r.object_key).sort();

  test("removes uploads never sent after a day, keeps sent and recent ones, and deletes their objects", async () => {
    const stale: Json = await (await upload(anaKey, taskId, form(png(6, 6), "image/png", png(6, 6), "image/png"))).json();
    const fresh: Json = await (await upload(anaKey, taskId, form(png(7, 7), "image/png", png(7, 7), "image/png"))).json();
    const sent: Json = await (await upload(anaKey, taskId, form(png(8, 8), "image/png", png(8, 8), "image/png"))).json();
    await owner`UPDATE attachments SET created_at = now() - interval '25 hours' WHERE id IN (${stale.id}, ${sent.id})`;
    await owner`UPDATE attachments SET for_prompt = true, attached_at = now() WHERE id = ${sent.id}`;
    const [staleRow] = await owner`SELECT object_key, original_key FROM attachments WHERE id = ${stale.id}`;
    expect(objects.has(staleRow.object_key)).toBe(true);

    const result = await sweepAttachments();
    expect(result.expired).toBe(1);
    expect(result.failed).toBe(0);
    const left = (await owner`SELECT id FROM attachments WHERE id IN (${stale.id}, ${fresh.id}, ${sent.id})`).map((r: { id: string }) => r.id);
    expect(left.sort()).toEqual([fresh.id, sent.id].sort());
    expect(objects.has(staleRow.object_key)).toBe(false);
    expect(objects.has(staleRow.original_key)).toBe(false);
    expect(await stillQueued([staleRow.object_key, staleRow.original_key])).toEqual([]);
  });

  test("a delete storage refuses stays queued for the next pass", async () => {
    const a: Json = await (await upload(anaKey, taskId, form(png(9, 9), "image/png", png(9, 9), "image/png"))).json();
    const [row] = await owner`SELECT object_key, original_key FROM attachments WHERE id = ${a.id}`;
    const keys = [row.object_key, row.original_key].sort();
    await call(anaKey, "DELETE", `/v1/attachments/${a.id}`);
    s3Down = true;
    try {
      const result = await sweepAttachments();
      expect(result.failed).toBe(2);
      expect(await stillQueued(keys)).toEqual(keys);
    } finally {
      s3Down = false;
    }
    expect((await sweepAttachments()).deleted).toBe(2);
    expect(await stillQueued(keys)).toEqual([]);
  });

  test("a pass drains a queue longer than one batch, and stops when its time is spent", async () => {
    // An organization's worth: more than two batches of 200.
    const keys = Array.from({ length: 450 }, (_, i) => `attachments/${ORG}/bulk/${String(i).padStart(3, "0")}`);
    for (const k of keys) objects.set(k, { bytes: new Uint8Array([1]), type: null });
    await owner`INSERT INTO attachment_object_deletions ${owner(keys.map((object_key) => ({ object_key, organization_id: ORG })))}`;

    // No time at all: nothing drained, everything still queued.
    expect((await sweepAttachments(new Date(), 0)).deleted).toBe(0);
    expect((await stillQueued(keys)).length).toBe(450);

    expect((await sweepAttachments()).deleted).toBeGreaterThanOrEqual(450);
    expect(await stillQueued(keys)).toEqual([]);
    expect(keys.filter((k) => objects.has(k))).toEqual([]);
  });

  test("a pass whose whole batch storage refuses ends there, leaving everything queued", async () => {
    const keys = Array.from({ length: 450 }, (_, i) => `attachments/${ORG}/refused/${String(i).padStart(3, "0")}`).sort();
    await owner`INSERT INTO attachment_object_deletions ${owner(keys.map((object_key) => ({ object_key, organization_id: ORG })))}`;
    s3Down = true;
    try {
      const result = await sweepAttachments();
      expect(result).toEqual({ expired: 0, deleted: 0, failed: SWEEP_BATCH });
      expect(await stillQueued(keys)).toEqual(keys);
    } finally {
      s3Down = false;
    }
    expect((await sweepAttachments()).deleted).toBeGreaterThanOrEqual(450);
    expect(await stillQueued(keys)).toEqual([]);
  });
});

describe("deleting a task", () => {
  test("takes its attachments with it, and their objects are deleted", async () => {
    await owner`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_att_gone', ${ORG}, 'prj_att', 2, 'Gone')`;
    const kept: Json = await (await upload(anaKey, taskId, form(png(3, 3), "image/png", png(3, 3), "image/png"))).json();
    const ids: string[] = [];
    for (let i = 0; i < 2; i++) {
      ids.push(((await (await upload(anaKey, "wi_att_gone", form(png(4, 4), "image/png", png(4, 4), "image/png"))).json()) as Json).id);
    }
    await owner`UPDATE attachments SET for_prompt = true, attached_at = now() WHERE id = ${ids[0]!}`;
    const keys = (await owner`SELECT object_key, original_key FROM attachments WHERE task_id = 'wi_att_gone'`)
      .flatMap((r: { object_key: string; original_key: string }) => [r.object_key, r.original_key]);
    expect(keys.length).toBe(4);
    for (const k of keys) expect(objects.has(k)).toBe(true);

    await owner`DELETE FROM tasks WHERE id = 'wi_att_gone'`;
    expect((await owner`SELECT count(*)::int AS n FROM attachments WHERE task_id = 'wi_att_gone'`)[0].n).toBe(0);
    // Every key of the task's, and only the task's.
    const queued = (await owner`SELECT object_key FROM attachment_object_deletions WHERE object_key LIKE ${`attachments/${ORG}/wi_att_gone/%`}`)
      .map((r: { object_key: string }) => r.object_key);
    expect(queued.sort()).toEqual([...keys].sort());

    await sweepAttachments();
    for (const k of keys) expect(objects.has(k)).toBe(false);
    // Another task's are untouched.
    expect((await call(anaKey, "GET", `/v1/attachments/${kept.id}`)).status).toBe(200);
  });
});
