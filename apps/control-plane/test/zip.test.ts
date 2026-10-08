/**
 * The zip writer, read back by unzip: stored entries with data
 * descriptors, UTF-8 names, streamed bodies, and names that cannot escape.
 */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { crc32 } from "node:zlib";
import { safeEntryName, zipStream } from "../src/api/zip.ts";

let dir: string;
beforeAll(async () => {
  dir = await mkdtemp(join(tmpdir(), "dude-zip-"));
});
afterAll(async () => {
  await rm(dir, { recursive: true, force: true });
});

const bytes = (s: string) => new TextEncoder().encode(s);

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

test("a reader that stops reading stops the entry's source being read", async () => {
  const chunk = 64 * 1024;
  const chunks = 1024; // 64 MiB
  let pulled = 0;
  const source = new ReadableStream<Uint8Array>({
    pull(c) {
      if (pulled === chunks) return c.close();
      pulled++;
      c.enqueue(new Uint8Array(chunk).fill(pulled % 251));
    },
  });
  const reader = zipStream([{ name: "large.bin", open: async () => source }]).getReader();
  const first = await reader.read();
  expect(first.value?.length).toBe(30);
  // Long enough for a writer that ignores backpressure to drain the whole source.
  await Bun.sleep(200);
  expect(pulled).toBeLessThanOrEqual(4);

  const parts: Uint8Array[] = [first.value!];
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
  }
  expect(pulled).toBe(chunks);
  const archive = new Uint8Array(await new Blob(parts).arrayBuffer());
  // Local header, name, the data, a 16-byte descriptor, one central record and the end record.
  const name = "large.bin".length;
  expect(archive.length).toBe(30 + name + chunks * chunk + 16 + 46 + name + 22);
  let crc = 0;
  for (let i = 1; i <= chunks; i++) crc = crc32(new Uint8Array(chunk).fill(i % 251), crc);
  const descriptor = new DataView(archive.buffer, 30 + name + chunks * chunk, 16);
  expect([descriptor.getUint32(0, true), descriptor.getUint32(4, true), descriptor.getUint32(8, true)]).toEqual([0x08074b50, crc, chunks * chunk]);
  const path = join(dir, "large.zip");
  await writeFile(path, archive);
  expect(Bun.spawnSync(["unzip", "-t", path]).stdout.toString()).toContain("No errors detected");
  await rm(path);
});

test("cancelling the archive cancels the entry being read", async () => {
  let cancelled = false;
  const source = new ReadableStream<Uint8Array>({
    pull: (c) => c.enqueue(new Uint8Array(1024)),
    cancel: () => {
      cancelled = true;
    },
  });
  const reader = zipStream([{ name: "endless.bin", open: async () => source }]).getReader();
  await reader.read();
  await reader.read();
  await reader.read();
  await reader.cancel();
  expect(cancelled).toBe(true);
});

test("a name cannot climb out of where it is unzipped", () => {
  expect(safeEntryName("../../etc/passwd")).toBe("etc/passwd");
  expect(safeEntryName("/abs/x")).toBe("abs/x");
  expect(safeEntryName("a\\..\\b")).toBe("a/b");
  expect(safeEntryName("..")).toBe("file");
});
