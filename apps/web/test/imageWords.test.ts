import { describe, expect, test } from "bun:test";
import type { ImageBuild, ImageSummary } from "@dude/domain";
import { buildStages, containerfileCompletions, draftCounts, imageState, queuePlace, usedByWords } from "../src/imageWords.ts";

const summary = (over: Partial<ImageSummary> = {}): ImageSummary => ({
  id: "img_a", name: "node-pnpm", description: "", archivedAt: null, createdAt: "2026-09-01T00:00:00Z", createdBy: null, isDefault: false,
  published: { versionId: "v4", number: 4, builtAt: "2026-09-30T00:00:00Z", userRef: "r@sha256:1" }, pending: null, draft: null,
  parents: [], from: "image:acme-base", usedBy: [], lastChange: { at: "2026-09-30T00:00:00Z", by: null, source: "person" }, ...over,
});
const job = (over: Partial<ImageBuild> = {}): ImageBuild => ({
  id: "b", imageId: "img_a", imageName: "node-pnpm", versionId: "v5", version: 5, kind: "build", state: "queued", stage: null, layerRef: null,
  requestedBy: null, requestedAt: "2026-10-01T00:00:00Z", startedAt: null, finishedAt: null, error: null, buildSeconds: null, pushSeconds: null, ahead: 1, ...over,
});
const ago = () => "2h ago";

describe("an image's state in its row", () => {
  test("published, with when", () => {
    expect(imageState(summary(), [], ago)).toEqual({ kind: "published", words: "Published · 2h ago" });
  });
  test("a newer version building, and for how long", () => {
    const s = summary({ pending: { versionId: "v5", number: 5, state: "building", error: null } });
    expect(imageState(s, [job({ state: "running", startedAt: "x" })], ago)).toEqual({ kind: "building", words: "Building v5 · 2h" });
  });
  test("waiting, with its place in the builder's line", () => {
    const s = summary({ pending: { versionId: "v5", number: 5, state: "queued", error: null } });
    expect(imageState(s, [job({ ahead: 1 })], ago)).toEqual({ kind: "waiting", words: "Waiting · 2nd" });
    expect(imageState(s, [job({ ahead: 0 })], ago).words).toBe("Waiting · next");
  });
  test("a rebuild waiting on its base says so", () => {
    const s = summary({ parents: [{ id: "img_base", name: "acme-base" }], pending: { versionId: "v5", number: 5, state: "queued", error: null } });
    expect(imageState(s, [job({ ahead: 2 }), job({ id: "c", imageId: "img_base", state: "running" })], ago)).toEqual({ kind: "waiting", words: "Rebuilds after acme-base" });
  });
  test("failed, and what is still live", () => {
    const s = summary({ pending: { versionId: "v5", number: 5, state: "failed", error: "x" } });
    expect(imageState(s, [], ago)).toEqual({ kind: "failed", words: "v5 failed · v4 still live" });
    expect(imageState({ ...s, published: null }, [], ago).words).toBe("v5 failed");
  });
  test("nothing published: a draft, or nothing", () => {
    expect(imageState(summary({ published: null, draft: { versionId: "d", updatedAt: "x", updatedBy: null } }), [], ago).kind).toBe("draft");
    expect(imageState(summary({ published: null }), [], ago)).toEqual({ kind: "none", words: "Nothing published" });
  });
});

test("queue places read as a person counts", () => {
  expect([null, 0, 1, 2, 10, 20].map(queuePlace)).toEqual(["", "next", "2nd", "3rd", "11th", "21st"]);
});

test("who uses an image, in a few words", () => {
  const project = { id: "p", name: "Dashboard" };
  expect(usedByWords([
    { kind: "organization_default" },
    { kind: "role", role: "qa_browser" },
    { kind: "runtime", project },
    { kind: "preview", project },
    { kind: "child", image: { id: "c", name: "storybook" } },
  ], "Acme")).toBe("Default; Tester (Acme); Dashboard · runtime, previews; storybook FROM it");
  expect(usedByWords([], "Acme")).toBe("Nobody yet");
});

describe("a build's stages", () => {
  const limits = { cpus: 1.5, memoryMiB: 1536 };
  test("waiting, building, failed: the failure's sentence in the stage, and what stays live", () => {
    expect(buildStages({ state: "queued", stage: null, kind: "build", error: null }, limits, 4).map((s) => s.state)).toEqual(["current", "todo", "todo"]);
    const running = buildStages({ state: "running", stage: "building", kind: "build", error: null }, limits, 4);
    expect(running.map((s) => s.state)).toEqual(["done", "current", "todo"]);
    expect(running[1]!.detail).toBe("rootless · 1.5 CPU · 1.5 GB");
    const failed = buildStages({ state: "failed", stage: null, kind: "build", error: "ran out of memory (1.5 GB) at step 3" }, limits, 4);
    expect(failed.map((s) => [s.state, s.detail])).toEqual([["done", "in the queue"], ["failed", "ran out of memory (1.5 GB) at step 3"], ["todo", "v4 is still live"]]);
  });
  test("a finish says it adds the dude layer", () => {
    expect(buildStages({ state: "running", stage: "finishing", kind: "finish", error: null }, limits, 1)[1]!.label).toBe("Adding the dude layer");
  });
});

describe("Containerfile completion", () => {
  const images = [{ name: "acme-base", version: 7, isDefault: true }, { name: "node-pnpm", version: 4, isDefault: false }];
  test("after FROM, the library's images first, then common bases", () => {
    const got = containerfileCompletions({ before: "FROM image:", word: "image:" }, images, "Acme", "node-pnpm")!;
    expect(got.map((c) => [c.label, c.detail])).toEqual([["image:acme-base", "Acme’s default base · v7"]]);
    const bases = containerfileCompletions({ before: "FROM deb", word: "deb" }, images, "Acme")!;
    expect(bases.map((c) => c.label)).toEqual(["debian:bookworm-slim", "debian:trixie-slim"]);
  });
  test("at a line's start, the instructions", () => {
    expect(containerfileCompletions({ before: "WO", word: "WO" }, images, "Acme")!.map((c) => c.label)).toEqual(["WORKDIR"]);
  });
  test("elsewhere, nothing", () => {
    expect(containerfileCompletions({ before: "RUN apt", word: "apt" }, images, "Acme")).toBeNull();
  });
});

test("a draft's lines added and removed against what it was made from", () => {
  expect(draftCounts("FROM a\nRUN x\n", "FROM a\nRUN y\nENV Z=1\n")).toEqual({ add: 2, del: 1 });
});
