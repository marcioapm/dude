/**
 * The live pieces: what a live diff flashes between updates, how it names
 * its files, and the gallery's split of pictures from documents.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { LiveDiff, type LiveDiffFile } from "../src/components/LiveDiff.tsx";
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
    expect(html).not.toContain("live-pill");
    expect(html).not.toContain("Follow the agent");
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
});

describe("artifactKind", () => {
  test("knows video and pages", () => {
    expect(artifactKind("video/mp4", "x")).toBe("video");
    expect(artifactKind("application/octet-stream", "run.webm")).toBe("video");
    expect(artifactKind("text/html", "x")).toBe("html");
    expect(artifactKind("", "coverage.html")).toBe("html");
  });
});
