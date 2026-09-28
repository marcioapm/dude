/**
 * The zip writer, read back by unzip: stored entries with data
 * descriptors, UTF-8 names, streamed bodies, and names that cannot escape.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { crc32, safeEntryName, zipStream } from "../src/api/zip.ts";

let dir: string;
beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "dude-zip-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

const bytes = (s: string) => new TextEncoder().encode(s);

test("crc32 is the zip one", () => {
  expect(crc32(bytes("123456789"))).toBe(0xcbf43926);
  expect(crc32(new Uint8Array())).toBe(0);
  // Continued across chunks, the same as at once.
  expect(crc32(bytes("6789"), crc32(bytes("12345")))).toBe(0xcbf43926);
});

test("an archive unzip reads back, byte for byte", async () => {
  const big = new Uint8Array(300_000).map((_, i) => (i * 31) % 256);
  const archive = await new Response(
    zipStream([
      { name: "NOTES.md", open: async () => bytes("# Notes\n") },
      { name: "screens/login.png", modified: new Date(2026, 8, 28, 10, 30, 12), open: async () => new Blob([big]).stream() },
      { name: "résumé.txt", open: async () => bytes("") },
    ]),
  ).arrayBuffer();
  const path = join(dir, "a.zip");
  await writeFile(path, new Uint8Array(archive));

  const test = Bun.spawnSync(["unzip", "-t", path]);
  expect(test.stdout.toString()).toContain("No errors detected");
  const out = join(dir, "out");
  expect(Bun.spawnSync(["unzip", "-q", path, "-d", out]).exitCode).toBe(0);
  expect(await readFile(join(out, "NOTES.md"), "utf8")).toBe("# Notes\n");
  expect(new Uint8Array(await readFile(join(out, "screens/login.png")))).toEqual(big);
  expect((await readdir(out)).sort()).toEqual(["NOTES.md", "résumé.txt", "screens"]);
  const listing = Bun.spawnSync(["unzip", "-l", path]).stdout.toString();
  expect(listing).toMatch(/(09-28-2026|2026-09-28) 10:30\s+screens\/login.png/);
});

test("an empty archive is a valid one", async () => {
  const archive = new Uint8Array(await new Response(zipStream([])).arrayBuffer());
  expect(archive.length).toBe(22);
  expect(new DataView(archive.buffer).getUint32(0, true)).toBe(0x06054b50);
});

test("an entry's bytes are read only when the archive reaches it", async () => {
  const opened: string[] = [];
  const stream = zipStream([
    { name: "a", open: async () => (opened.push("a"), bytes("a")) },
    { name: "b", open: async () => (opened.push("b"), bytes("b")) },
  ]);
  const reader = stream.getReader();
  await reader.read();
  expect(opened).toEqual(["a"]);
  await reader.cancel();
});

test("a name cannot climb out of where it is unzipped", () => {
  expect(safeEntryName("../../etc/passwd")).toBe("etc/passwd");
  expect(safeEntryName("/abs/x")).toBe("abs/x");
  expect(safeEntryName("a\\..\\b")).toBe("a/b");
  expect(safeEntryName("..")).toBe("file");
});
