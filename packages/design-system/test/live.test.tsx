/**
 * The live pieces: what a live diff flashes between updates, how it names
 * its files, and the gallery's split of pictures from documents.
 */

import { describe, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { LiveDiff, splitRows, type LiveDiffFile } from "../src/components/LiveDiff.tsx";
import { galleryOrder, type GalleryFile } from "../src/components/FileGallery.tsx";
import { artifactKind } from "../src/components/ArtifactRow.tsx";
import { cssModuleAsWritten } from "./cssModule.ts";

// The gallery's class names as written, so a card's classes can be read.
mock.module(...cssModuleAsWritten(new URL("../src/components/FileGallery.module.css", import.meta.url)));
const { FileGallery } = await import("../src/components/FileGallery.tsx");

const file = (path: string, status: LiveDiffFile["status"], lines: Array<["+" | "-" | " ", string]>): LiveDiffFile => ({
  path, status,
  additions: lines.filter(([k]) => k === "+").length,
  deletions: lines.filter(([k]) => k === "-").length,
  hunks: [{ header: "@@ -1 +1 @@", lines: lines.map(([kind, text], i) => ({ kind, old: kind === "+" ? null : i + 1, new: kind === "-" ? null : i + 1, text })) }],
});

describe("LiveDiff", () => {
  test("names each file with its status as a letter and a word, and sums the counts", () => {
    const html = renderToStaticMarkup(
      <LiveDiff base="0fff44b9a1c2" live files={[file("src/a.ts", "M", [["-", "old"], ["+", "new"]]), file("NEW.md", "A", [["+", "hi"]])]} />,
    );
    expect(html).toContain("Since <span");
    expect(html).toContain("0fff44b<");
    expect(html).toContain("2 files");
    expect(html).toContain("+2");
    expect(html).toContain('aria-label="added"');
    expect(html).toContain('aria-label="modified"');
    expect(html).toContain("new file");
    // Signs as well as tints: a diff reads without colour.
    expect(html).toContain(">−<");
    expect(html).toContain('role="switch"');
    expect(html).toContain('aria-checked="true"');
  });

  test("nothing flashes on first sight", () => {
    const html = renderToStaticMarkup(<LiveDiff base="b" files={[file("a", "A", [["+", "x"]])]} />);
    expect(html).not.toContain("data-fresh");
  });

  test("an ended diff has no live pill and no follow toggle", () => {
    const html = renderToStaticMarkup(<LiveDiff base="b" files={[file("a", "A", [["+", "x"]])]} />);
    expect(html).not.toContain('role="switch"');
    expect(html).not.toContain("Follow the agent");
  });

  test("split pairs each removed run with the added run after it", () => {
    const l = (kind: "+" | "-" | " ", text: string) => ({ kind, old: null, new: null, text });
    const rows = splitRows([l(" ", "a"), l("-", "b"), l("-", "c"), l("+", "B"), l(" ", "d"), l("+", "e")]);
    expect(rows.map((r) => [r.left?.text ?? null, r.right?.text ?? null])).toEqual([["a", "a"], ["b", "B"], ["c", null], ["d", "d"], [null, "e"]]);
  });

  test("offers Unified/Split, and the viewer only when it can open one", () => {
    const files = [file("a.ts", "M", [["+", "x"]]), file("gone.ts", "D", [["-", "y"]])];
    expect(renderToStaticMarkup(<LiveDiff base="b" files={files} />)).not.toContain("Open in the viewer");
    const html = renderToStaticMarkup(<LiveDiff base="b" files={files} onOpenFile={() => {}} defaultView="split" />);
    expect(html.match(/Open in the viewer/g)?.length).toBe(2); // aria-label and title, once: not on the deleted file
    expect(html).toContain(">Unified<");
    expect(html).toContain("split-row");
  });

  test("the page's controls share its toolbar, and the last change heads the files", () => {
    const html = renderToStaticMarkup(
      <LiveDiff base="b" live files={[file("a.ts", "M", [["+", "x"]])]} leading={<i data-x="lead" />}
        lastChange={{ tool: "Write", path: "a.ts" }} />,
    );
    const head = html.slice(0, html.indexOf('aria-label="Changed files"'));
    expect(head.indexOf('data-x="lead"')).toBeLessThan(head.indexOf("Since"));
    expect(html.indexOf('data-testid="last-change"')).toBeGreaterThan(html.indexOf('aria-label="Changed files"'));
  });

  test("an empty diff still says what the agent did last", () => {
    expect(renderToStaticMarkup(<LiveDiff base="b" live files={[]} lastChange={{ tool: "Write", path: "a.ts" }} />)).toContain("a.ts");
  });

  test("with nothing changed there is no summary, only what it says", () => {
    const html = renderToStaticMarkup(<LiveDiff base="b" files={[]} emptyMessage="Nothing yet." />);
    expect(html).not.toContain("0 files");
    expect(html).not.toContain("Unified");
  });

  test("follows the newest change only while the diff is on screen", async () => {
    // happy-dom lays nothing out: give every box the same place, and count the follows.
    const place = HTMLElement.prototype.getBoundingClientRect;
    const scroll = HTMLElement.prototype.scrollTo;
    let at = { top: 100, bottom: 400 };
    const follows: ScrollToOptions[] = [];
    HTMLElement.prototype.getBoundingClientRect = () => ({ ...at, left: 0, right: 800, width: 800, height: at.bottom - at.top, x: 0, y: at.top, toJSON() {} }) as DOMRect;
    HTMLElement.prototype.scrollTo = function (o?: ScrollToOptions | number) { if (typeof o === "object") follows.push(o); } as typeof HTMLElement.prototype.scrollTo;
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    let n = 0;
    const next = () => [file("src/a.ts", "M", [["-", "old"], ...Array.from({ length: ++n }, (_, i): ["+", string] => ["+", `line ${i}`])])];
    try {
      await act(async () => root.render(<LiveDiff base="0fff44b9a1c2" live files={next()} />));
      await act(async () => root.render(<LiveDiff base="0fff44b9a1c2" live files={next()} />));
      expect(follows).toHaveLength(1);
      expect(follows[0]!.behavior).toBe("smooth");
      // Below the fold, and above it: a new change flashes, but nothing scrolls.
      for (const off of [{ top: window.innerHeight + 10, bottom: window.innerHeight + 300 }, { top: -300, bottom: -1 }]) {
        at = off;
        await act(async () => root.render(<LiveDiff base="0fff44b9a1c2" live files={next()} />));
        expect(host.querySelectorAll("[data-fresh]").length).toBeGreaterThan(0);
        expect(follows).toHaveLength(1);
      }
      // Back on screen, and partly on screen across either edge: it follows each time.
      const h = window.innerHeight;
      for (const [i, on] of [{ top: 100, bottom: 400 }, { top: -100, bottom: 300 }, { top: h - 100, bottom: h + 500 }].entries()) {
        at = on;
        await act(async () => root.render(<LiveDiff base="0fff44b9a1c2" live files={next()} />));
        expect(follows).toHaveLength(2 + i);
      }
    } finally {
      await act(async () => root.unmount());
      host.remove();
      HTMLElement.prototype.getBoundingClientRect = place;
      HTMLElement.prototype.scrollTo = scroll;
    }
  });

  test("with nothing changed it says so", () => {
    expect(renderToStaticMarkup(<LiveDiff base="b" files={[]} emptyMessage="Nothing yet." />)).toContain("Nothing yet.");
  });

});

describe("FileGallery", () => {
  const v = (name: string, contentType: string, version = 1) => ({ id: `${name}${version}`, name, contentType, sizeBytes: 1024, createdAt: 0, role: "implementer" as const, session: "Implement", version });
  const contentTypes: Record<string, string> = { md: "text/markdown", png: "image/png", webm: "video/webm" };
  const named = (...names: string[]): GalleryFile[] => names.map((n) => ({ name: n, versions: [v(n, contentTypes[n.split(".").pop()!]!)] }));
  const files: GalleryFile[] = [
    { name: "shot.png", versions: [v("shot.png", "image/png", 2), v("shot.png", "image/png", 1)] },
    { name: "demo.webm", versions: [v("demo.webm", "video/webm")] },
    { name: "NOTES.md", versions: [v("NOTES.md", "text/markdown")] },
  ];

  test("pictures in the gallery, documents in the list, a file saved again is one file with versions", () => {
    const html = renderToStaticMarkup(<FileGallery files={files} onOpen={() => {}} onDownload={() => {}} onDownloadAll={() => {}} />);
    expect(html.match(/data-testid="file-card"/g)?.length).toBe(2);
    expect(html.match(/data-testid="file-row"/g)?.length).toBe(1);
    expect(html).toContain(">v2<");
    expect(html).toContain("Download all");
    expect(html).toContain("3 KB");
  });

  test("pictures, then documents, each by name as a person counts: 2 before 10, the case ignored", () => {
    const order = galleryOrder(named("b.md", "10-done.png", "02-cleared.png", "A.md", "2-saved.png", "walkthrough.png")).map((f) => f.name);
    expect(order).toEqual(["02-cleared.png", "2-saved.png", "10-done.png", "walkthrough.png", "A.md", "b.md"]);
  });

  test("the gallery shows its cards and rows in that order, whatever order it was given", () => {
    const given = named("NOTES.md", "02-cleared.png", "01-saved.png", "walkthrough.webm", "acceptance.md");
    const doc = new DOMParser().parseFromString(renderToStaticMarkup(<FileGallery files={given} onOpen={() => {}} onDownload={() => {}} />), "text/html");
    const names = (id: string) => [...doc.querySelectorAll(`[data-testid="${id}"]`)].map((e) => e.getAttribute("data-name"));
    expect(names("file-card")).toEqual(["01-saved.png", "02-cleared.png", "walkthrough.webm"]);
    expect(names("file-row")).toEqual(["acceptance.md", "NOTES.md"]);
  });

  test("a video with a picture shows it under a play glyph, still a button; without one, the dark card", () => {
    const html = (thumbnail?: string) => renderToStaticMarkup(<FileGallery files={[files[1]!]} thumbnail={() => thumbnail} onOpen={() => {}} onDownload={() => {}} />);
    const card = new DOMParser().parseFromString(html("blob:poster"), "text/html").querySelector("[data-testid='file-card']")!;
    expect(card.tagName).toBe("BUTTON");
    const picture = card.firstElementChild!;
    expect(picture.querySelector("img")?.getAttribute("src")).toBe("blob:poster");
    expect(picture.querySelectorAll("svg").length).toBe(1);
    expect(picture.classList.contains("picture")).toBe(true);
    expect(picture.classList.contains("dark")).toBe(false);
    const bare = new DOMParser().parseFromString(html(), "text/html").querySelector("[data-testid='file-card']")!.firstElementChild!;
    expect(bare.querySelector("img")).toBeNull();
    expect(bare.querySelectorAll("svg").length).toBe(1);
    expect(bare.classList.contains("dark")).toBe(true);
  });

  test("a file's description is a line under its name, in a card and a row, the latest version's; none, no line", async () => {
    const described: GalleryFile[] = [
      { name: "shot.png", versions: [{ ...v("shot.png", "image/png"), description: "The signed-in page" }] },
      { name: "NOTES.md", versions: [
        { ...v("NOTES.md", "text/markdown", 2), description: "With numbers" },
        { ...v("NOTES.md", "text/markdown", 1), description: "First draft" },
      ] },
      { name: "plain.md", versions: [v("plain.md", "text/markdown")] },
    ];
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    try {
      await act(async () => root.render(<FileGallery files={described} onOpen={() => {}} onDownload={() => {}} />));
      const card = host.querySelector("[data-testid='file-card'][data-name='shot.png']")!;
      const row = (name: string) => host.querySelector(`[data-testid='file-row'][data-name='${name}']`)!;
      const description = (scope: Element) => [...scope.querySelectorAll("[data-testid='file-description']")]
        .map((d) => [d.textContent, d.getAttribute("title")]);
      expect(textIn(card, "The signed-in page")).not.toBeNull();
      expect(description(card)).toEqual([["The signed-in page", "The signed-in page"]]);
      expect(textIn(row("NOTES.md"), "With numbers")).not.toBeNull();
      expect(description(row("NOTES.md"))).toEqual([["With numbers", "With numbers"]]);
      expect(textsIn(host, "First draft")).toBe(0);
      expect(description(row("plain.md"))).toEqual([]);
    } finally {
      await act(async () => root.unmount());
      host.remove();
    }
  });
});

/** The element within `scope` whose own text is exactly `text`, as getByText finds it. */
function textIn(scope: Element, text: string): Element | null {
  return [...scope.querySelectorAll("*")].find((e) => e.children.length === 0 && e.textContent === text) ?? null;
}

/** How many elements within `scope` have exactly `text` as their own: absence is 0. */
function textsIn(scope: Element, text: string): number {
  return [...scope.querySelectorAll("*")].filter((e) => e.children.length === 0 && e.textContent === text).length;
}

describe("artifactKind", () => {
  test("knows video and pages", () => {
    expect(artifactKind("video/mp4", "x")).toBe("video");
    expect(artifactKind("application/octet-stream", "run.webm")).toBe("video");
    expect(artifactKind("text/html", "x")).toBe("html");
    expect(artifactKind("", "coverage.html")).toBe("html");
  });

});

describe("LiveDiff: comment marks and the page's scope", () => {
  const files = [file("src/a.ts", "M", [["-", "old"], ["+", "new"]]), file("src/b.ts", "A", [["+", "hi"]])];
  const marks = (path: string) => (path === "src/a.ts" ? { count: 3, label: "2 submitted by Ana Silva · 1 your draft" }
    : path === "docs/c.md" ? { count: 1, label: "1 outdated", position: 1 } : null);
  const commenting = (onFileComments = (_: string) => {}) => ({ maxLines: 20, selection: null, onSelectionChange: () => {}, fileComments: marks, onFileComments });

  test("scopeLabel replaces the Since summary; null leaves it out", () => {
    expect(renderToStaticMarkup(<LiveDiff base="b" files={files} scopeLabel={<i data-x="scope" />} />)).toContain('data-x="scope"');
    const none = renderToStaticMarkup(<LiveDiff base="b" files={files} scopeLabel={null} />);
    expect(none).not.toContain("Since");
    expect(none).not.toContain("2 files");
    expect(renderToStaticMarkup(<LiveDiff base="b" files={files} />)).toContain("Since");
  });

  test("a marked file carries its mark on the header and beside — not inside — its list entry", () => {
    const host = document.createElement("div");
    document.body.append(host);
    const root = createRoot(host);
    const shown: string[] = [];
    try {
      act(() => root.render(<LiveDiff base="b" files={files} commenting={commenting((p) => shown.push(p))} />));
      const all = [...host.querySelectorAll<HTMLButtonElement>('[data-testid="comment-mark"]')];
      expect(all.map((m) => `${m.dataset["where"]}:${m.dataset["path"]}`).sort()).toEqual(["head:src/a.ts", "list:src/a.ts"]);
      for (const m of all) {
        expect(m.closest('[data-testid="diff-file"]')).toBeNull();
        expect(m.getAttribute("aria-label")).toBe("3 comments on src/a.ts — show next");
        expect(m.textContent).toBe("3");
      }
      // No button holds a button.
      expect(host.querySelectorAll("button button").length).toBe(0);
      all[0]!.click();
      expect(shown).toEqual(["src/a.ts"]);
      // An unmarked file stays one button.
      expect(host.querySelector('[data-testid="diff-file"][data-path="src/b.ts"]')!.parentElement!.tagName).toBe("NAV");
    } finally {
      act(() => root.unmount());
      host.remove();
    }
  });

  test("while cycling the mark reads k / N and names the comment", () => {
    const html = renderToStaticMarkup(<LiveDiff base="b" files={files} commenting={{ ...commenting(), fileComments: (p: string) => (p === "src/a.ts" ? { count: 3, label: "x", position: 2 } : null) }} />);
    expect(html).toContain("Comment 2 of 3 on src/a.ts — show next");
    expect(html).toContain("2 / 3");
  });

  test("commented files the page's filter hides are listed with their marks", () => {
    const html = renderToStaticMarkup(<LiveDiff base="b" files={files} filteredOut={["docs/c.md"]} commenting={commenting()} />);
    expect(html).toContain("With comments, hidden by the filter");
    expect(html).toContain('data-testid="filtered-out-file" data-path="docs/c.md"');
    expect(html).toContain("Comment 1 of 1 on docs/c.md — show next");
    expect(renderToStaticMarkup(<LiveDiff base="b" files={files} commenting={commenting()} />)).not.toContain("hidden by the filter");
    // Even zero matches must leave hidden commented paths reachable.
    const noMatches = renderToStaticMarkup(<LiveDiff base="b" files={[]} filteredOut={["docs/c.md"]} commenting={commenting()} />);
    expect(noMatches).toContain("With comments, hidden by the filter");
    expect(noMatches).toContain("Comment 1 of 1 on docs/c.md — show next");
  });

});
