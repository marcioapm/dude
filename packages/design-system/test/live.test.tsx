/**
 * The live pieces: what a live diff flashes between updates, how it names
 * its files, and the gallery's split of pictures from documents.
 */

import { describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { LiveDiff, splitRows, type LiveDiffFile } from "../src/components/LiveDiff.tsx";
import { FileGallery, type GalleryFile } from "../src/components/FileGallery.tsx";
import { artifactKind } from "../src/components/ArtifactRow.tsx";

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

  test("with nothing changed it says so", () => {
    expect(renderToStaticMarkup(<LiveDiff base="b" files={[]} emptyMessage="Nothing yet." />)).toContain("Nothing yet.");
  });
});

describe("FileGallery", () => {
  const v = (name: string, contentType: string, version = 1) => ({ id: `${name}${version}`, name, contentType, sizeBytes: 1024, createdAt: 0, role: "implementer" as const, session: "Implement", version });
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
      expect(textIn(host, "First draft")).toBeNull();
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

describe("artifactKind", () => {
  test("knows video and pages", () => {
    expect(artifactKind("video/mp4", "x")).toBe("video");
    expect(artifactKind("application/octet-stream", "run.webm")).toBe("video");
    expect(artifactKind("text/html", "x")).toBe("html");
    expect(artifactKind("", "coverage.html")).toBe("html");
  });
});
