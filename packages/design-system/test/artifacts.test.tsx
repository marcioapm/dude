import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ArtifactGroup, ArtifactRow, artifactKind } from "../src/components/ArtifactRow.tsx";
import { ArtifactPreview, prettyJson } from "../src/components/ArtifactPreview.tsx";

describe("artifactKind", () => {
  test("content type decides", () => {
    expect(artifactKind("text/markdown", "x")).toBe("markdown");
    expect(artifactKind("application/json", "x")).toBe("json");
    expect(artifactKind("application/problem+json", "x")).toBe("json");
    expect(artifactKind("image/png", "x")).toBe("image");
    expect(artifactKind("text/plain; charset=utf-8", "x")).toBe("text");
    expect(artifactKind("application/pdf", "report.pdf")).toBe("other");
  });
  test("a generic or missing type falls back to the extension", () => {
    expect(artifactKind("application/octet-stream", "README.md")).toBe("markdown");
    expect(artifactKind(undefined, "screens/login.PNG")).toBe("image");
    expect(artifactKind(null, "results.json")).toBe("json");
    expect(artifactKind("", "notes.txt")).toBe("text");
    expect(artifactKind("", "bundle.tar.gz")).toBe("other");
  });
});

describe("prettyJson", () => {
  test("pretty-prints valid JSON", () => {
    expect(prettyJson('{"a":1,"b":[1,2]}')).toBe('{\n  "a": 1,\n  "b": [\n    1,\n    2\n  ]\n}');
  });
  test("returns invalid JSON untouched", () => {
    expect(prettyJson('{"a": ')).toBe('{"a": ');
  });
});

const row = (extra: Partial<Parameters<typeof ArtifactRow>[0]> = {}) =>
  renderToStaticMarkup(
    <ul>
      <ArtifactRow name="design.md" contentType="text/markdown" sizeBytes={1284} producer={{ role: "implementer", phase: "write" }} publishedAt={Date.now() - 3_600_000} {...extra} />
    </ul>,
  );

describe("ArtifactRow", () => {
  test("without a preview there is no disclosure button", () => {
    const html = row({ download: <a href="/x" download>Download</a> });
    expect(html).not.toContain("aria-expanded");
    expect(html).toContain('href="/x"');
    expect(html).toContain("1.3 KB");
    expect(html).toContain("design.md");
  });
  test("with a preview the head is a button with aria-expanded, and the download is its sibling", () => {
    const html = row({ preview: <div>body</div>, download: <a href="/x" download>Download</a>, defaultExpanded: true });
    expect(html).toMatch(/<button[^>]*aria-expanded="true"/);
    expect(html).not.toMatch(/<button[^>]*>(?:(?!<\/button>).)*<a /s);
    expect(html).toContain(">body<");
  });
  test("collapsed rows render no body and no aria-controls", () => {
    const html = row({ preview: <div>body</div> });
    expect(html).toMatch(/aria-expanded="false"/);
    expect(html).not.toContain("aria-controls");
    expect(html).not.toContain(">body<");
  });
  test("change is a neutral badge; producer names the role and phase", () => {
    const html = row({ change: "updated" });
    expect(html).toContain(">Updated<");
    expect(html).toContain("Implementer · write");
  });
});

describe("ArtifactGroup", () => {
  test("renders nothing when empty and not asked", () => {
    expect(renderToStaticMarkup(<ArtifactGroup artifacts={[]} renderRow={() => null} />)).toBe("");
  });
  test("renders the empty state when asked", () => {
    const html = renderToStaticMarkup(<ArtifactGroup artifacts={[]} renderRow={() => null} empty="Nothing published." />);
    expect(html).toContain("No artifacts yet");
    expect(html).toContain("Nothing published.");
  });
  test("header carries the count", () => {
    const html = renderToStaticMarkup(<ArtifactGroup artifacts={[{ id: "a" }, { id: "b" }]} renderRow={(a) => <li key={a.id}>{a.id}</li>} />);
    expect(html).toContain(">2<");
    expect(html).toContain("Artifacts");
  });
});

describe("ArtifactPreview", () => {
  test("markdown renders as a document", () => {
    const html = renderToStaticMarkup(<ArtifactPreview contentType="text/markdown" name="d.md" text="# Hi" />);
    expect(html).toContain("<h1");
  });
  test("JSON is pretty-printed in a pre; invalid JSON stays as typed", () => {
    expect(renderToStaticMarkup(<ArtifactPreview contentType="application/json" name="r.json" text='{"a":1}' />)).toContain('{\n  &quot;a&quot;: 1\n}');
    expect(renderToStaticMarkup(<ArtifactPreview contentType="application/json" name="r.json" text='{"a": ' />)).toContain("{&quot;a&quot;: ");
  });
  test("images are an img with the name as alt", () => {
    const html = renderToStaticMarkup(<ArtifactPreview contentType="image/png" name="login.png" url="/blob/1" />);
    expect(html).toMatch(/<img[^>]*src="\/blob\/1"[^>]*alt="login.png"/);
  });
  test("unknown types say so and show the download", () => {
    const html = renderToStaticMarkup(<ArtifactPreview contentType="application/pdf" name="r.pdf" download={<a href="/r.pdf">get</a>} />);
    expect(html).toContain("No preview for application/pdf.");
    expect(html).toContain('href="/r.pdf"');
  });
  test("loading and error states", () => {
    expect(renderToStaticMarkup(<ArtifactPreview contentType="text/plain" name="a" loading />)).toContain('aria-busy="true"');
    expect(renderToStaticMarkup(<ArtifactPreview contentType="text/plain" name="a" error="404" />)).toContain('role="alert"');
  });
  test("a file too large to show offers its download instead", () => {
    const html = renderToStaticMarkup(
      <ArtifactPreview contentType="text/plain" name="huge.log" tooLarge download={<a href="#x">dl</a>} text="never shown" />,
    );
    expect(html).toContain("Too large to show here.");
    expect(html).toContain("dl");
    expect(html).not.toContain("never shown");
  });
});
