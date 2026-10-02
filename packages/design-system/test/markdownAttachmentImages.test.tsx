/**
 * An image a person put in their Markdown (`![alt](attachment:id)`) is
 * drawn in place through the caller's resolver; every other image stays a
 * link, never fetched.
 */

import { expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { Markdown, MarkdownImage } from "../src/components/Markdown.tsx";
import { MarkdownEditor } from "../src/primitives/MarkdownEditor.tsx";

const SOURCE = "Before ![header shot](attachment:att_h1) after.\n\n![remote](https://tracker.test/p.png)";

test("an attachment: image goes through the resolver, in its place; another image stays a link", () => {
  const asked: Array<[string, string]> = [];
  const h = renderToStaticMarkup(<Markdown source={SOURCE} attachmentImage={(id, alt) => {
    asked.push([id, alt]);
    return <MarkdownImage src={`blob:shown/${id}`} alt={alt} onOpen={() => {}} />;
  }} />);
  expect(asked).toEqual([["att_h1", "header shot"]]);
  // In the paragraph, between its words.
  expect(h).toMatch(/<p[^>]*>Before <button[^>]*data-testid="markdown-image"[^>]*><img src="blob:shown\/att_h1" alt="header shot"[^>]*\/><\/button> after\.<\/p>/);
  // The other image is a link and nothing fetches it.
  expect(h).toContain('href="https://tracker.test/p.png"');
  expect(h).not.toContain('src="https://tracker.test');
  expect(h.match(/<img /g)?.length).toBe(1);
});

test("without a resolver an attachment: image is its alt text, never a request", () => {
  const h = renderToStaticMarkup(<Markdown source={SOURCE} />);
  expect(h).toContain("Before header shot after.");
  expect(h).not.toContain("attachment:");
  expect(h).not.toContain("<img");
});

test("an attachment: URL is not a link target", () => {
  const h = renderToStaticMarkup(<Markdown source="[click](attachment:att_x)" />);
  expect(h).not.toContain("href=");
});

test("only what the parsers attach is drawn; any other spelling stays text", () => {
  const asked: Array<[string, string, string | undefined]> = [];
  const h = renderToStaticMarkup(<Markdown
    source={'![ok](attachment:att_ok "small right") ![a [b] c](attachment:att_n) ![nb](attachment:att_nb\u00a0"small") ![dash](attachment:att-x)'}
    attachmentImage={(id, alt, title) => {
      asked.push([id, alt, title]);
      return <MarkdownImage src={`blob:${id}`} alt={alt} />;
    }} />);
  expect(asked).toEqual([["att_ok", "ok", "small right"]]);
  expect(h).toContain("![a [b] c](attachment:att_n)");
  expect(h).toContain("![dash](attachment:att-x)");
  expect(h.match(/<img /g)?.length).toBe(1);
});

test("an unavailable image says so", () => {
  const h = renderToStaticMarkup(<MarkdownImage alt="gone.png" unavailable />);
  expect(h).toContain("gone.png · unavailable");
  expect(h).not.toContain("<img");
});

test("the editor's Preview draws through the same resolver", () => {
  const h = renderToStaticMarkup(<MarkdownEditor value={SOURCE} onChange={() => {}} defaultMode="preview"
    attachmentImage={(id, alt) => <MarkdownImage src={`blob:${id}`} alt={alt} />} />);
  expect(h).toContain('<img src="blob:att_h1" alt="header shot"');
});
