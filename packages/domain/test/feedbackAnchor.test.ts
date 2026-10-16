import { describe, expect, test } from "bun:test";
import { relocateCodeAnchor, type FeedbackCodeAnchor, type FeedbackExcerptLine, type RunDiffFile, type RunDiffHunk, type RunDiffLine } from "../src/index.ts";

/** A hunk from unified lines (" ", "+", "-" first), numbered from its starts. */
function hunk(oldStart: number, newStart: number, body: string): RunDiffHunk {
  let o = oldStart;
  let n = newStart;
  const lines: RunDiffLine[] = body.split("\n").map((raw) => {
    const kind = raw[0] as " " | "+" | "-";
    const text = raw.slice(1);
    if (kind === "+") return { kind, old: null, new: n++, text };
    if (kind === "-") return { kind, old: o++, new: null, text };
    return { kind: " ", old: o++, new: n++, text };
  });
  return { header: `@@ -${oldStart} +${newStart} @@`, lines };
}

function file(path: string, hunks: RunDiffHunk[], status: RunDiffFile["status"] = "M"): RunDiffFile {
  const count = (k: string) => hunks.reduce((s, h) => s + h.lines.filter((l) => l.kind === k).length, 0);
  return { path, status, hunks, additions: count("+"), deletions: count("-") };
}

/** The lines of `f` from start to end on `side`, as the backend quotes them (excerptOf). */
function excerpt(f: RunDiffFile, side: "old" | "new", start: number, end: number): FeedbackExcerptLine[] {
  const n = (l: RunDiffLine) => (side === "old" ? l.old : l.new);
  for (const h of f.hunks) {
    const first = h.lines.findIndex((l) => n(l) === start);
    const last = h.lines.findIndex((l) => n(l) === end);
    if (first >= 0 && last >= first) return h.lines.slice(first, last + 1).map((l) => ({ ...l }));
  }
  throw new Error(`no ${side} ${start}-${end} in ${f.path}`);
}

function anchorOn(f: RunDiffFile, side: "old" | "new", start: number, end: number, over: Partial<FeedbackCodeAnchor> = {}): FeedbackCodeAnchor {
  return { kind: "code", runId: "run_a", repo: "web", path: f.path, side, startLine: start, endLine: end, baseSha: "b0",
    headSha: null, diffChecksum: "c1", excerpt: excerpt(f, side, start, end), ...over };
}

const later = (files: RunDiffFile[]) => ({ runId: "run_b", checksum: "c2", files });

// The diff the comments were written on: lines 10–14 of app.ts on the new side.
const WRITTEN = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 2;
+const b = 3;
+const c = b * 2;
 export { a, b };`)]);

describe("relocateCodeAnchor", () => {
  test("on the very diff it was written on, an item is on its own lines", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    expect(relocateCodeAnchor(a, { runId: "run_a", checksum: "c1", files: [WRITTEN] }))
      .toEqual({ kind: "lines", path: "src/app.ts", side: "new", startLine: 11, endLine: 12 });
  });

  test("lines inserted above shift the item down to the same text", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const moved = file("src/app.ts", [hunk(10, 10, `+// a header
+// two lines
 const a = 1;
-const b = 2;
+const b = 3;
+const c = b * 2;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([moved]))).toEqual({ kind: "lines", path: "src/app.ts", side: "new", startLine: 13, endLine: 14 });
  });

  test("lines deleted above shift the item up", () => {
    const before = file("src/app.ts", [hunk(1, 1, ` // top
 // more
 const a = 1;
+const b = 3;
+const c = b * 2;`)]);
    const a = anchorOn(before, "new", 4, 5);
    const after = file("src/app.ts", [hunk(1, 1, `-// top
-// more
 const a = 1;
+const b = 3;
+const c = b * 2;`)]);
    expect(relocateCodeAnchor(a, later([after]))).toEqual({ kind: "lines", path: "src/app.ts", side: "new", startLine: 2, endLine: 3 });
  });

  test("an edit inside the anchored lines makes it outdated, never attached to the changed text", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const edited = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 2;
+const b = 3;
+const c = b * 3;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([edited]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "changed" });
  });

  test("anchored lines split apart by an insertion are outdated: contiguous only", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const split = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 2;
+const b = 3;
+// between
+const c = b * 2;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([split]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "changed" });
  });

  test("a whitespace-only change counts as a change", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const spaced = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 2;
+const b = 3;
+const c = b *  2;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([spaced]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "changed" });
    const indented = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 2;
+  const b = 3;
+const c = b * 2;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([indented]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "changed" });
  });

  test("the same block found twice is outdated: exactly once, or not placed", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const twice = file("src/app.ts", [
      hunk(10, 10, ` const a = 1;
-const b = 2;
+const b = 3;
+const c = b * 2;
 export { a, b };`),
      hunk(40, 41, ` // copy
+const b = 3;
+const c = b * 2;`),
    ]);
    expect(relocateCodeAnchor(a, later([twice]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "ambiguous" });
  });

  test("found once in another hunk of the file, it moves there", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const elsewhere = file("src/app.ts", [
      hunk(10, 10, ` const a = 1;
-const b = 2;
+const z = 0;
 export { a, b };`),
      hunk(40, 40, ` // moved down
+const b = 3;
+const c = b * 2;`),
    ]);
    expect(relocateCodeAnchor(a, later([elsewhere]))).toEqual({ kind: "lines", path: "src/app.ts", side: "new", startLine: 41, endLine: 42 });
  });

  test("a renamed file is outdated, even with the same lines", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const renamed = { ...WRITTEN, status: "R" as const };
    expect(relocateCodeAnchor(a, later([renamed]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "renamed" });
  });

  test("a removed file is outdated", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const removed = file("src/app.ts", [hunk(10, 0, `-const a = 1;
-const b = 3;
-const c = b * 2;`)], "D");
    expect(relocateCodeAnchor(a, later([removed]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "removed" });
  });

  test("a file moved away (not in the diff any more) has nowhere to be placed", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const other = file("src/other.ts", [hunk(1, 1, "+x")]);
    expect(relocateCodeAnchor(a, later([other]))).toEqual({ kind: "absent" });
  });

  test("the old side matches only removed and context lines, by their old numbers", () => {
    const a = anchorOn(WRITTEN, "old", 10, 11); // " const a = 1;", "-const b = 2;"
    expect(a.excerpt.map((l) => l.kind)).toEqual([" ", "-"]);
    const shifted = file("src/app.ts", [hunk(20, 22, ` const a = 1;
-const b = 2;
+const b = 4;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([shifted]))).toEqual({ kind: "lines", path: "src/app.ts", side: "old", startLine: 20, endLine: 21 });
    // The same text on the new side is not the old side's.
    const onNew = file("src/app.ts", [hunk(20, 22, `+const a = 1;
+const b = 2;`)]);
    expect(relocateCodeAnchor(a, later([onNew]))).toEqual({ kind: "unchanged", path: "src/app.ts" });
  });

  test("the new side ignores removed lines with the same text", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const gone = file("src/app.ts", [hunk(10, 10, ` const a = 1;
-const b = 3;
-const c = b * 2;
 export { a, b };`)]);
    expect(relocateCodeAnchor(a, later([gone]))).toEqual({ kind: "outdated", path: "src/app.ts", reason: "changed" });
  });

  test("lines outside every hunk of the viewed diff are unchanged in it, not outdated and not placed", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12);
    const far = file("src/app.ts", [hunk(200, 200, ` x();
+y();`)]);
    expect(relocateCodeAnchor(a, later([far]))).toEqual({ kind: "unchanged", path: "src/app.ts" });
  });

  test("with several repositories the diff names the file under its repository", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12, { repo: "web", path: "src/app.ts" });
    const prefixed = { ...WRITTEN, path: "web/src/app.ts" };
    const moved = file("web/src/app.ts", [hunk(1, 1, `+// new
 const a = 1;
+const b = 3;
+const c = b * 2;`)]);
    expect(relocateCodeAnchor(a, later([moved]))).toEqual({ kind: "lines", path: "web/src/app.ts", side: "new", startLine: 3, endLine: 4 });
    expect(relocateCodeAnchor(a, { runId: "run_a", checksum: "c1", files: [prefixed] }).kind).toBe("lines");
  });

  test("a repository checked out under a hashed lux name is found under that name", () => {
    // lux_name('Web') = 'web-29751047' (migration 093).
    const a = anchorOn(WRITTEN, "new", 11, 12, { repo: "Web", path: "src/app.ts" });
    const hashed = file("web-29751047/src/app.ts", WRITTEN.hunks);
    expect(relocateCodeAnchor(a, later([hashed]))).toEqual({ kind: "lines", path: "web-29751047/src/app.ts", side: "new", startLine: 11, endLine: 12 });
  });

  test("with one repository, a file of the same name in a subdirectory is another file", () => {
    const readme = file("README.md", [hunk(12, 12, "+Run it with make.")]);
    const a = anchorOn(readme, "new", 12, 12, { repo: "web" });
    const docs = file("docs/README.md", [hunk(12, 12, "+Run it with make.")]);
    expect(relocateCodeAnchor(a, later([docs]))).toEqual({ kind: "absent" });
  });

  test("with two plainly named repositories, the same path in the other one is another file", () => {
    const a = anchorOn(WRITTEN, "new", 11, 12, { repo: "web", path: "src/app.ts" });
    const api = file("api/src/app.ts", WRITTEN.hunks);
    expect(relocateCodeAnchor(a, later([api]))).toEqual({ kind: "absent" });
  });
});
