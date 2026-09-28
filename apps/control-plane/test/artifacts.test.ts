/**
 * What an artifact is served as: its recorded type when that says
 * something, else its name's, else its first bytes'.
 */

import { expect, test } from "bun:test";
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
