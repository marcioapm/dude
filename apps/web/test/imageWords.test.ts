import { describe, expect, test } from "bun:test";
import type { ImageBuild, ImageSummary } from "@dude/domain";
import { buildStages, containerfileCompletions, draftCounts, imageState, mergeBuildLog, queuePlace, usedByWords } from "../src/imageWords.ts";

const summary = (over: Partial<ImageSummary> = {}): ImageSummary => ({
  id: "img_a", name: "node-pnpm", description: "", archivedAt: null, createdAt: "2026-09-01T00:00:00Z", createdBy: null, isDefault: false,
  published: { versionId: "v4", number: 4, builtAt: "2026-09-30T00:00:00Z", userRef: "r@sha256:1", canRunContainers: false }, pending: null, draft: null,
  parents: [], from: "image:acme-base", usedBy: [], lastChange: { at: "2026-09-30T00:00:00Z", by: null, source: "person" }, ...over,
});
const job = (over: Partial<ImageBuild> = {}): ImageBuild => ({
  id: "b", imageId: "img_a", imageName: "node-pnpm", versionId: "v5", version: 5, kind: "build", state: "queued", stage: null, layerRef: null,
  requestedBy: null, requestedAt: "2026-10-01T00:00:00Z", startedAt: null, finishedAt: null, error: null, buildSeconds: null, pushSeconds: null, canRunContainers: false, containersCheck: null, checkSeconds: null, ahead: 1, ...over,
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

describe("the build page's log after a read", () => {
  type Log = { id: string; log: string; logStart: number; logTotal: number };
  const log = (logStart: number, text: string, id = "imb_1"): Log => ({ id, log: text, logStart, logTotal: logStart + new TextEncoder().encode(text).length });
  const held = log(0, "STEP 1\nSTEP 2\n");
  test.each<[string, Log | null, Log, Log | null, string?]>([
    ["the first read is taken as it is", null, held, held],
    ["a delta from where it ends is appended", held, log(14, "STEP 3 é\n"), { id: "imb_1", log: "STEP 1\nSTEP 2\nSTEP 3 é\n", logStart: 0, logTotal: 24 }],
    ["an empty delta changes nothing in the log", held, log(14, ""), { ...held }],
    ["a delta for bytes it has (two reads overlapping) is dropped", log(0, "STEP 1\nSTEP 2\nSTEP 3\n"), log(14, "STEP 3\n"), log(0, "STEP 1\nSTEP 2\nSTEP 3\n")],
    ["an answer older than what it has is dropped", log(0, "STEP 1\nSTEP 2\nSTEP 3\n"), log(0, "STEP 1\n"), log(0, "STEP 1\nSTEP 2\nSTEP 3\n")],
    ["a cut tail, starting past its end, replaces it", held, log(100, "STEP 9\n"), log(100, "STEP 9\n")],
    ["the whole kept log, holding all it has, replaces it", held, log(0, "STEP 1\nSTEP 2\nSTEP 3\n"), log(0, "STEP 1\nSTEP 2\nSTEP 3\n")],
    ["another build's log replaces it", held, log(0, "other\n", "imb_2"), log(0, "other\n", "imb_2"), "imb_2"],
    ["a late answer for the build shown before is dropped", log(0, "other\n", "imb_2"), held, log(0, "other\n", "imb_2"), "imb_2"],
    ["a late answer for the build shown before is dropped before anything is shown", null, held, null, "imb_2"],
  ])("%s", (_name, prev, next, want, shown = "imb_1") => {
    expect(mergeBuildLog(prev, next, shown)).toEqual(want);
  });

  test("keeps only the last max bytes, never half a rune", () => {
    const got = mergeBuildLog(log(0, "aaaa"), log(4, "ébb"), "imb_1", 3);
    // "aaaaébb" is 8 bytes; the last 3 would start inside é, so 2 are kept.
    expect(got).toEqual({ id: "imb_1", log: "bb", logStart: 6, logTotal: 8 });
    expect(mergeBuildLog(null, log(0, "x".repeat(10)), "imb_1", 4)).toEqual({ id: "imb_1", log: "xxxx", logStart: 6, logTotal: 10 });
  });
});

describe("a build of a version that can run containers", () => {
  const limits = { cpus: 2, memoryMiB: 4096 };
  const base = { kind: "build" as const, error: null, canRunContainers: true };
  test("has a Check containers cell between Built and Published", () => {
    const stages = buildStages({ ...base, state: "queued", stage: null }, limits, 2);
    expect(stages.map((s) => s.label)).toEqual(["Waiting", "Building", "Check containers", "Pushed and published"]);
    expect(buildStages({ ...base, canRunContainers: false, state: "queued", stage: null }, limits, 2).map((s) => s.id)).toEqual(["wait", "build", "done"]);
  });
  test("checking is the current cell, after Built", () => {
    expect(buildStages({ ...base, state: "running", stage: "checking" }, limits, 2).map((s) => [s.label, s.state])).toEqual([
      ["Waiting", "done"], ["Built", "done"], ["Check containers", "current"], ["Pushed and published", "todo"]]);
  });
  test("passed: what it found, and published", () => {
    const detail = "podman 5.4, fuse-overlayfs, newuidmap/newgidmap with capabilities, subuid for agent";
    const stages = buildStages({ ...base, state: "succeeded", stage: null, containersCheck: { passed: true, detail } }, limits, 3);
    expect(stages.map((s) => [s.label, s.state, s.detail])).toEqual([
      ["Waiting", "done", "in the queue"], ["Built", "done", "rootless · 2 CPU · 4 GB"], ["Check containers", "done", detail],
      ["Published", "done", "every user gets it on their next Run"]]);
  });
  test("failed: the check's cell fails, the next says nothing was pushed", () => {
    const stages = buildStages({ ...base, state: "failed", stage: null, error: "Can't run containers: …",
      containersCheck: { passed: false, detail: "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap" } }, limits, 4);
    expect(stages.map((s) => [s.label, s.state, s.detail])).toEqual([
      ["Waiting", "done", "in the queue"], ["Built", "done", "rootless · 2 CPU · 4 GB"],
      ["Check containers", "failed", "Missing: podman or Docker, fuse-overlayfs, newuidmap, newgidmap"],
      ["Not published", "todo", "Not pushed · v4 is still live"]]);
  });
});
