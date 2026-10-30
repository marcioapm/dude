/**
 * LiveDiff on a long branch: a render of the page around it that changes
 * nothing a file shows does not redraw each file's entry or header.
 */

import { describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { LiveDiff, type FileComments } from "../src/components/LiveDiff.tsx";

const files = Array.from({ length: 40 }, (_, i) => ({ path: `src/f${i}.ts`, status: "M" as const, additions: 1, deletions: 0,
  hunks: [{ header: "@@ -1 +1 @@", lines: [{ kind: "+" as const, old: null, new: 1, text: `x${i}` }] }] }));

describe("LiveDiff's files on an unrelated render", () => {
  test("entries and headers are drawn again only when their file, mark or selection changes", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    let drawn = 0;
    // Called once per file entry drawn: what the memo saves is counted by it.
    const fileMark = () => { drawn++; return null; };
    let position: number | undefined;
    // A new function and new marks on every page render, as a page passes them.
    const commenting = () => ({ maxLines: 20, selection: null, onSelectionChange: () => {}, fileMark,
      fileComments: (p: string): FileComments | null => (p === "src/f3.ts" ? { count: 2, label: "2 comments", position } : null), onFileComments: () => {} });
    const draw = (cls: string, selected: string | null = null) =>
      act(() => root.render(<LiveDiff base="b" files={files} className={cls} commenting={commenting()} selected={selected} onSelectedChange={() => {}} />));
    try {
      draw("one");
      expect(drawn).toBe(40);
      drawn = 0;
      draw("two");
      expect(drawn).toBe(0);
      // A mark moving in its cycle redraws its file alone.
      position = 1;
      draw("three");
      expect(drawn).toBe(1);
      expect(host.querySelector('[data-testid="comment-mark"][data-where="head"]')!.textContent).toBe("1 / 2");
      expect(host.querySelector('[data-testid="comment-mark"][data-where="list"]')!.textContent).toBe("1 / 2");
      // Selecting a file redraws that entry (and the one before, none here).
      drawn = 0;
      draw("four", "src/f7.ts");
      expect(drawn).toBe(1);
      expect(host.querySelector('[data-testid="diff-file"][data-path="src/f7.ts"]')!.getAttribute("aria-pressed")).toBe("true");
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  });
});

describe("LiveDiff's renderAfterLineFor", () => {
  test("a file whose function is kept does no lookups on a rerender; renderAfterLine is never asked when both are given", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const two = [files[0]!, files[1]!];
    const lookups: Record<string, number> = {};
    const lookup = () => (path: string) => { lookups[path] = (lookups[path] ?? 0) + 1; return null; };
    let fallback = 0;
    const renderAfterLine = () => { fallback++; return null; };
    const forA = lookup();
    const draw = (cls: string, forB: (path: string) => null) => act(() => root.render(
      <LiveDiff base="b" files={two} className={cls} commenting={{ maxLines: 20, selection: null, onSelectionChange: () => {}, renderAfterLine,
        renderAfterLineFor: (p: string) => (p === "src/f0.ts" ? forA : forB) }} />));
    try {
      draw("one", lookup());
      expect(lookups["src/f0.ts"]).toBeGreaterThan(0);
      expect(lookups["src/f1.ts"]).toBeGreaterThan(0);
      for (const k of Object.keys(lookups)) delete lookups[k];
      // A's function kept, B's replaced: only B's hunk looks up its lines again.
      draw("two", lookup());
      expect(lookups["src/f0.ts"] ?? 0).toBe(0);
      expect(lookups["src/f1.ts"]).toBeGreaterThan(0);
      expect(fallback).toBe(0);
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  });
});
