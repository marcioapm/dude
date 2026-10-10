/**
 * The backend's pool reads and renders times in UTC on a database whose
 * default zone is not: a time json_build_object renders reaches the
 * browser as "+00:00", where it is merged with UTC times.
 *
 * Requires DATABASE_URL: a role that can create databases (the owner).
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { SQL } from "bun";
import { openPool } from "../src/db/client.ts";

const OWNER_URL = process.env.DATABASE_URL ?? "postgres://dude:dude@localhost:5433/dude";
const NAME = `dude_zone_test_${Bun.randomUUIDv7("hex").slice(-12)}`;

let admin: SQL;
let db: SQL;

beforeAll(async () => {
  admin = new SQL(OWNER_URL);
  await admin.unsafe(`CREATE DATABASE "${NAME}"`);
  await admin.unsafe(`ALTER DATABASE "${NAME}" SET timezone = 'Europe/Lisbon'`);
  const url = new URL(OWNER_URL);
  url.pathname = `/${NAME}`;
  db = openPool(url.toString());
});

afterAll(async () => {
  await db?.end();
  await admin?.unsafe(`DROP DATABASE IF EXISTS "${NAME}" WITH (FORCE)`);
  await admin?.end();
});

test("a time Postgres renders in JSON is UTC on a database in Europe/Lisbon", async () => {
  const [row] = await db`SELECT current_setting('TimeZone') AS zone,
    json_build_object('at', '2026-10-10 15:42:53.519753+00'::timestamptz) AS j`;
  expect(row.zone).toBe("UTC");
  expect(row.j.at).toBe("2026-10-10T15:42:53.519753+00:00");
});
