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
    await db`INSERT INTO organizations (id, name, slug) VALUES ('org_a','A','a'), ('org_b','B','b')`;
    await db`INSERT INTO people (id, organization_id, email, name, last_seen_at, removed_at) VALUES
      ('per_a','org_a','A@EXAMPLE.TEST','Older',now()-interval '1 day',NULL),
      ('per_b','org_b','a@example.test','Latest',now(),NULL),
      ('per_gone','org_b','gone@example.test','Gone',NULL,now()),
      ('per_null','org_a',NULL,'Key only',NULL,NULL)`;
    await db.unsafe(await readFile("migrations/113_accounts.sql", "utf8"));
    expect((await db`SELECT email::text, name FROM accounts ORDER BY email`).map((r: Record<string, unknown>) => ({ ...r })))
      .toEqual([{ email: "a@example.test", name: "Latest" }, { email: "gone@example.test", name: "Gone" }]);
    const rows = await db`SELECT id, account_id FROM people ORDER BY id`;
    expect(rows.map((r: { id: string }) => r.id)).toEqual(["per_a", "per_b", "per_gone", "per_null"]);
    expect(rows[0].account_id).toBe(rows[1].account_id); expect(rows[3].account_id).toBeNull();
    await db`UPDATE people SET removed_at = now() WHERE id = 'per_b'`;
    await db`UPDATE accounts SET name = 'Changed' WHERE email = 'a@example.test'`;
    expect((await db`SELECT name FROM people WHERE id = 'per_a'`)[0].name).toBe("Changed");
    expect((await db`SELECT name FROM people WHERE id = 'per_b'`)[0].name).toBe("Latest");
  } finally {
    await db.end(); await admin.unsafe(`DROP DATABASE ${name} WITH (FORCE)`); await admin.end();
  }
}, 30000);
