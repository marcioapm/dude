import { expect, test } from "bun:test";
import { SQL } from "bun";
import { readFile } from "node:fs/promises";
import { listMigrationFiles } from "../src/db/migrate.ts";

test("accounts backfill preserves ids, shares emails, leaves email-less people, and copies only active profiles", async () => {
  const admin = new SQL(process.env.DATABASE_URL!);
  const name = `accounts_backfill_${crypto.randomUUID().replaceAll("-", "")}`;
  await admin.unsafe(`CREATE DATABASE ${name}`);
  const url = new URL(process.env.DATABASE_URL!); url.pathname = `/${name}`;
  const db = new SQL(url.toString());
  try {
    await db`CREATE TABLE schema_migrations (version text PRIMARY KEY, name text, checksum text)`;
    for (const file of await listMigrationFiles()) {
      if (file.version === "113") break;
      await db.unsafe(await file.contents());
    }
    await db`INSERT INTO organizations (id, name, slug) VALUES ('org_a','A','a'), ('org_b','B','b'), ('org_c','C','c')`;
    await db`INSERT INTO people (id, organization_id, email, name, photo_url, photo_key, photo_token, last_seen_at, removed_at) VALUES
      ('per_a','org_a','A@EXAMPLE.TEST','Older','https://img.example/older.png','older-key','older-token',now()-interval '1 day',NULL),
      ('per_b','org_b','a@example.test','Latest','https://img.example/latest.png','latest-key','latest-token',now(),NULL),
      ('per_snapshot','org_c','A@example.test','Snapshot','https://img.example/snapshot.png','snapshot-key','snapshot-token',now(),now()),
      ('per_gone','org_b','gone@example.test','Gone',NULL,NULL,NULL,NULL,now()),
      ('per_null','org_a',NULL,'Key only',NULL,NULL,NULL,NULL,NULL)`;
    const snapshot = (await db`SELECT name, email::text, photo_url, photo_key, photo_token FROM people WHERE id='per_snapshot'`)[0];
    await db.unsafe(await readFile("migrations/113_accounts.sql", "utf8"));
    expect((await db`SELECT email::text, name FROM accounts ORDER BY email`).map((r: Record<string, unknown>) => ({ ...r })))
      .toEqual([{ email: "a@example.test", name: "Latest" }, { email: "gone@example.test", name: "Gone" }]);
    const rows = await db`SELECT id, account_id FROM people ORDER BY id`;
    expect(rows.map((r: { id: string }) => r.id)).toEqual(["per_a", "per_b", "per_gone", "per_null", "per_snapshot"]);
    expect(rows[0].account_id).toBe(rows[1].account_id); expect(rows[3].account_id).toBeNull();
    expect(rows[4].account_id).toBe(rows[0].account_id);
    const canonical = (await db`SELECT name, email::text, photo_url, photo_key, photo_token FROM accounts WHERE email='a@example.test'`)[0];
    expect(canonical).toEqual({ name: "Latest", email: "a@example.test", photo_url: "https://img.example/latest.png",
      photo_key: "latest-key", photo_token: "latest-token" });
    for (const id of ["per_a", "per_b"]) {
      expect((await db`SELECT name, email::text, photo_url, photo_key, photo_token FROM people WHERE id=${id}`)[0]).toEqual(canonical);
    }
    expect((await db`SELECT name, email::text, photo_url, photo_key, photo_token FROM people WHERE id='per_snapshot'`)[0]).toEqual(snapshot);
    await db`UPDATE people SET removed_at = now() WHERE id = 'per_b'`;
    await db`UPDATE accounts SET name = 'Changed' WHERE email = 'a@example.test'`;
    expect((await db`SELECT name FROM people WHERE id = 'per_a'`)[0].name).toBe("Changed");
    expect((await db`SELECT name FROM people WHERE id = 'per_b'`)[0].name).toBe("Latest");
  } finally {
    await db.end(); await admin.unsafe(`DROP DATABASE ${name} WITH (FORCE)`); await admin.end();
  }
}, 30000);
