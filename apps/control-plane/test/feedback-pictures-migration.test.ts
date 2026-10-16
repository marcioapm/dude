/**
 * Migration 133: a feedback item's picture is kept from the sweeper. Applies
 * the migrations before 133 to a database of its own, with a draft and a
 * submitted item naming pictures as production would have them, then 133,
 * twice: nothing of the data changes, and the sweeper may read an item's
 * anchor and nothing else of it.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { listMigrationFiles } from "../src/db/migrate.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_feedback_pictures_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

let admin: SQL;
let db: SQL;
let before: unknown[];

const items = () => db`SELECT id, status, anchor, body FROM feedback_items ORDER BY id`;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  db = new SQL(url.toString());
  const files = await listMigrationFiles();
  await db`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text NOT NULL, checksum text NOT NULL,
           applied_at timestamptz NOT NULL DEFAULT now())`;
  for (const f of files.filter((f) => f.version < "133")) await db.unsafe(await f.contents());

  await db`INSERT INTO organizations (id, name, slug) VALUES ('org_p', 'P', 'p')`;
  await db`INSERT INTO people (id, organization_id, name) VALUES ('per_ana', 'org_p', 'Ana')`;
  await db`INSERT INTO projects (id, organization_id, name, slug, key_prefix) VALUES ('prj_p', 'org_p', 'P', 'p', 'P')`;
  await db`INSERT INTO tasks (id, organization_id, project_id, number, title) VALUES ('wi_p', 'org_p', 'prj_p', 1, 'T')`;
  for (const id of ["att_draft", "att_sent"]) {
    await db`INSERT INTO attachments (id, organization_id, task_id, uploaded_by, name, content_type, width, height, bytes, sha256,
      object_key, original_content_type, original_width, original_height, original_bytes, original_key)
      VALUES (${id}, 'org_p', 'wi_p', 'per_ana', 'a.png', 'image/png', 1, 1, 1, 'x', ${`k/${id}`}, 'image/png', 1, 1, 1, ${`o/${id}`})`;
  }
  await db`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
    VALUES ('fbt_p', 'org_p', 'wi_p', 1, 'per_ana', 'k')`;
  await db`INSERT INTO feedback_items (id, organization_id, task_id, attempt, author_person_id, body, anchor_kind, anchor) VALUES
    ('fbi_draft', 'org_p', 'wi_p', 1, 'per_ana', 'secret words', 'preview', '{"kind":"preview","url":"https://w.test","attachmentId":"att_draft"}'),
    ('fbi_general', 'org_p', 'wi_p', 1, 'per_ana', 'general', 'general', '{"kind":"general"}')`;
  await db`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at)
    VALUES ('fbi_sent', 'org_p', 'wi_p', 1, 'fbt_p', 'per_ana', 'sent', 'preview',
            '{"kind":"preview","url":"https://w.test","attachmentId":"att_sent"}', 'open', now())`;
  before = await items();

  const m = files.find((f) => f.version === "133")!;
  await db.unsafe(await m.contents());
  await db.unsafe(await m.contents());
}, 120_000);

afterAll(async () => {
  await db?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("applied twice, it changes no item and no upload", async () => {
  expect(await items()).toEqual(before);
  expect((await db`SELECT count(*)::int AS n FROM attachments`)[0].n).toBe(2);
});

test("as the sweeper, an item's anchor is readable and its words are not", async () => {
  const anchors = await db.begin(async (tx) => {
    await tx`SET LOCAL ROLE dude_sweeper`;
    return tx`SELECT anchor->>'attachmentId' AS id FROM feedback_items WHERE anchor->>'attachmentId' IS NOT NULL ORDER BY 1`;
  });
  expect(anchors.map((r: { id: string }) => r.id)).toEqual(["att_draft", "att_sent"]);
  const words = db.begin(async (tx) => {
    await tx`SET LOCAL ROLE dude_sweeper`;
    return tx`SELECT body FROM feedback_items`;
  });
  await expect(words).rejects.toThrow("permission denied");
});
