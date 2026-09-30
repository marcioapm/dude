/**
 * MarkdownEditor: its markup in each state (server-rendered: what is shown,
 * what is a tab, what a screen reader is told), and the edits its keys and
 * buttons make, as the pure functions the editor applies. Applying an edit
 * through the browser's undo stack needs a real browser; the web app's
 * browser suite types into it.
 */

import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { MarkdownEditor, type MarkdownEditorProps } from "../src/primitives/MarkdownEditor.tsx";
import { modKey } from "../src/util/keys.ts";
import { Input } from "../src/primitives/Input.tsx";
import { KeyHint, MarkdownCheatsheet } from "../src/primitives/Kbd.tsx";
import { continueList, countState, editorKey, formatEdit, type TextEdit } from "../src/util/markdownEdit.ts";

const noop = () => {};
const html = (props: Partial<MarkdownEditorProps> = {}) =>
  renderToStaticMarkup(<MarkdownEditor label="Goal" value="" onChange={noop} data-testid="goal" {...props} />);

/** The opening tag of the first element carrying `attr`. */
function tag(h: string, attr: string): string {
  const at = h.indexOf(attr);
  expect(at).toBeGreaterThan(-1);
  return h.slice(h.lastIndexOf("<", at), h.indexOf(">", at) + 1);
}
const tabs = (h: string) => [...h.matchAll(/<button[^>]*role="tab"[^>]*>/g)].map((m) => m[0]);
/** The source with the edit applied, and the selected text after it. */
function apply(source: string, edit: TextEdit): [string, string] {
  const next = source.slice(0, edit.from) + edit.insert + source.slice(edit.to);
  return [next, next.slice(edit.selectionStart, edit.selectionEnd)];
}

describe("MarkdownEditor markup", () => {
  test("Write and Preview are tabs controlling their panels, one Tab stop", () => {
    const h = html({ value: "Some text" });
    const [write, preview] = tabs(h);
    expect(h).toContain('role="tablist"');
    expect(h).toContain('aria-label="Goal view"');
    expect(write).toContain('aria-selected="true"');
    expect(write).toContain('tabindex="0"');
    expect(preview).toContain('aria-selected="false"');
    expect(preview).toContain('tabindex="-1"');
    const controls = /aria-controls="([^"]+)"/.exec(write!)![1]!;
    const writePanel = tag(h, `id="${controls}"`);
    expect(writePanel).toContain('role="tabpanel"');
    expect(writePanel).not.toContain("hidden");
    const previewPanel = tag(h, `id="${/aria-controls="([^"]+)"/.exec(preview!)![1]}"`);
    expect(previewPanel).toContain('role="tabpanel"');
    expect(previewPanel).toContain("hidden");
  });

  test("the textarea carries the field's anatomy: label, hint, limit, test id, spellcheck", () => {
    const h = html({ hint: "Why it matters", maxLength: 100, placeholder: "Why?" });
    const area = tag(h, "<textarea");
    const id = /id="([^"]+)"/.exec(area)![1]!;
    expect(tag(h, "<label")).toContain(`for="${id}"`);
    expect(area).toContain('data-testid="goal"');
    expect(area).toContain('maxLength="100"');
    expect(area).toContain('spellCheck="true"');
    expect(area).toContain('placeholder="Why?"');
    const described = /aria-describedby="([^"]+)"/.exec(area)![1]!;
    expect(tag(h, `id="${described.split(" ")[0]}"`)).toContain("span");
    expect(h).toContain("Why it matters");
  });

  test("an error is tied to the textarea and marks it invalid", () => {
    const h = html({ error: "Criterion 2 is over 2,000 characters" });
    const area = tag(h, "<textarea");
    expect(area).toContain('aria-invalid="true"');
    const ids = /aria-describedby="([^"]+)"/.exec(area)![1]!.split(" ");
    expect(ids.some((id) => tag(h, `id="${id}"`) && h.includes("Criterion 2 is over"))).toBe(true);
  });

  test("the formatting buttons are named, with the shortcut in their tooltips' keys", () => {
    const h = html();
    const labels = [...h.matchAll(/<button[^>]*aria-label="([^"]+)"[^>]*data-format/g)].map((m) => m[1]);
    expect(labels).toEqual(["Heading", "Bold", "Italic", "Code", "Link", "Quote", "Bulleted list", "Checklist"]);
    expect(h).toContain('role="toolbar"');
  });

  test("locked opens in Preview with Write disabled, and the source cannot change", () => {
    const h = html({ value: "# Fixed\n\nA delivery started on this.", locked: true, hint: "Delivery has started" });
    const [write, preview] = tabs(h);
    expect(write).toContain("disabled");
    expect(preview).toContain('aria-selected="true"');
    expect(tag(h, "<textarea")).toContain("disabled");
    expect(h).toContain("<h1");
    expect(h).toContain("Fixed");
    expect(h).toContain("Delivery has started");
  });

  test("disabled behaves as locked", () => {
    const h = html({ value: "x", disabled: true });
    expect(tabs(h)[0]).toContain("disabled");
    expect(tabs(h)[1]).toContain('aria-selected="true"');
  });

  test("an empty preview says so in one line", () => {
    const h = html({ defaultMode: "preview" });
    expect(h).toContain("Nothing to preview yet.");
  });

  test("preview renders through Markdown: headings and lists are elements", () => {
    const h = html({ defaultMode: "preview", value: "## What exists\n\n- one\n- two" });
    expect(h).toMatch(/<h2[^>]*>What exists<\/h2>/);
    expect(h).toMatch(/<li[^>]*>(<[^>]+>)*one/);
  });

  test("preview never renders raw HTML or a javascript: link", () => {
    const h = html({ defaultMode: "preview", value: '<img src=x onerror="alert(1)"> <script>alert(2)</script>\n\n[click](javascript:alert(3))' });
    expect(h).not.toContain("<img");
    expect(h).not.toContain("<script");
    expect(h).toContain("&lt;img");
    expect(h).not.toMatch(/href="javascript:/i);
    expect(h).toContain("click");
  });

  test("the footer says Markdown, carries the summary and a notice, and counts against the limit", () => {
    const h = html({ value: "abc", maxLength: 10_000, summary: <span>2 criteria</span>, notice: "Text outside a list item isn't saved as a criterion" });
    expect(h).toContain("Markdown");
    expect(h).toContain("2 criteria");
    expect(h).toContain("Text outside a list item");
    expect(h).toContain("3 / 10,000");
    // The summary is read with the field.
    const described = /aria-describedby="([^"]+)"/.exec(tag(h, "<textarea"))![1]!.split(" ");
    expect(described.length).toBe(1);
    expect(h.indexOf(`id="${described[0]}"`)).toBeLessThan(h.indexOf("2 criteria"));
  });

  test("the count's state follows the limit", () => {
    const state = (n: number) => /<span[^>]*data-state="(ok|near|over)"[^>]*>[\d,]+ \/ 100</.exec(html({ value: "a".repeat(n), maxLength: 100 }))?.[1];
    expect([state(80), state(95), state(101)]).toEqual(["ok", "near", "over"]);
  });
});

describe("countState", () => {
  test("ok to 90%, near past it, over past the limit, ok with no limit", () => {
    expect(countState(90, 100)).toBe("ok");
    expect(countState(91, 100)).toBe("near");
    expect(countState(100, 100)).toBe("near");
    expect(countState(101, 100)).toBe("over");
    expect(countState(1e9, undefined)).toBe("ok");
  });
});

describe("editorKey", () => {
  const k = (key: string, mods: Partial<{ ctrlKey: boolean; metaKey: boolean; shiftKey: boolean; altKey: boolean }> = {}) =>
    editorKey({ key, ctrlKey: false, metaKey: false, shiftKey: false, altKey: false, ...mods });
  test("Ctrl or ⌘ with B I K E formats", () => {
    expect(k("b", { ctrlKey: true })).toEqual({ kind: "format", format: "bold" });
    expect(k("I", { metaKey: true })).toEqual({ kind: "format", format: "italic" });
    expect(k("k", { ctrlKey: true })).toEqual({ kind: "format", format: "link" });
    expect(k("e", { metaKey: true })).toEqual({ kind: "format", format: "code" });
    expect(k("b")).toBeNull();
    expect(k("b", { ctrlKey: true, altKey: true })).toBeNull();
  });
  test("Ctrl/⌘+Shift+P toggles; Shift+Enter and Ctrl+Enter are not list Enter", () => {
    expect(k("P", { ctrlKey: true, shiftKey: true })).toEqual({ kind: "toggle" });
    expect(k("p", { metaKey: true, shiftKey: true })).toEqual({ kind: "toggle" });
    expect(k("p", { ctrlKey: true })).toBeNull();
    expect(k("Enter")).toEqual({ kind: "newline" });
    expect(k("Enter", { shiftKey: true })).toBeNull();
    expect(k("Enter", { ctrlKey: true })).toBeNull();
  });
});

describe("formatEdit wraps the selection", () => {
  test("bold, italic and code around selected words, which stay selected", () => {
    const src = "make it red";
    expect(apply(src, formatEdit(src, 8, 11, "bold"))).toEqual(["make it **red**", "red"]);
    expect(apply(src, formatEdit(src, 8, 11, "italic"))).toEqual(["make it _red_", "red"]);
    expect(apply(src, formatEdit(src, 8, 11, "code"))).toEqual(["make it `red`", "red"]);
  });
  test("with nothing selected, a placeholder is inserted and selected", () => {
    expect(apply("", formatEdit("", 0, 0, "bold"))).toEqual(["**bold text**", "bold text"]);
    expect(apply("x ", formatEdit("x ", 2, 2, "italic"))).toEqual(["x _italic text_", "italic text"]);
    expect(apply("", formatEdit("", 0, 0, "code"))).toEqual(["`code`", "code"]);
  });
  test("again takes the marks off", () => {
    const src = "make it **red**";
    expect(apply(src, formatEdit(src, 10, 13, "bold"))).toEqual(["make it red", "red"]);
  });
  test("code over several lines is a fence", () => {
    const src = "a\nb";
    expect(apply(src, formatEdit(src, 0, 3, "code"))).toEqual(["```\na\nb\n```", "a\nb"]);
  });
  test("a link selects what is left to write", () => {
    expect(apply("see docs", formatEdit("see docs", 4, 8, "link"))).toEqual(["see [docs](https://)", "https://"]);
    expect(apply("", formatEdit("", 0, 0, "link"))).toEqual(["[text](https://)", "text"]);
  });
});

describe("formatEdit prefixes lines", () => {
  test("heading, quote, bullet and checklist on the caret's line; the caret keeps its place in the words", () => {
    const src = "one\ntwo";
    expect(apply(src, formatEdit(src, 5, 5, "heading"))[0]).toBe("one\n## two");
    expect(formatEdit(src, 5, 5, "heading").selectionStart).toBe(8);
    expect(apply(src, formatEdit(src, 5, 5, "quote"))[0]).toBe("one\n> two");
    expect(apply(src, formatEdit(src, 0, 0, "bullet"))[0]).toBe("- one\ntwo");
    expect(apply(src, formatEdit(src, 0, 0, "checklist"))[0]).toBe("- [ ] one\ntwo");
  });
  test("every selected line, and off again when all have it", () => {
    const src = "one\ntwo\nthree";
    const [on] = apply(src, formatEdit(src, 0, 7, "bullet"));
    expect(on).toBe("- one\n- two\nthree");
    expect(apply(on, formatEdit(on, 0, 11, "bullet"))[0]).toBe(src);
  });
  test("a selection ending at the start of a line leaves that line alone", () => {
    const src = "one\ntwo";
    expect(apply(src, formatEdit(src, 0, 4, "bullet"))[0]).toBe("- one\ntwo");
  });
});

describe("continueList", () => {
  const enter = (src: string, caret = src.length) => {
    const edit = continueList(src, caret);
    return edit ? apply(src, edit)[0] + `|${edit.selectionStart}` : null;
  };
  test("a bullet continues with the same bullet", () => {
    expect(enter("- one")).toBe("- one\n- |8");
    expect(enter("* one")).toBe("* one\n* |8");
    expect(enter("+ one")).toBe("+ one\n+ |8");
  });
  test("a number increments, keeping its delimiter", () => {
    expect(enter("1. one")).toBe("1. one\n2. |10");
    expect(enter("9) nine")).toBe("9) nine\n10) |12");
  });
  test("a task continues as an open task, even after a done one", () => {
    expect(enter("- [ ] one")).toBe("- [ ] one\n- [ ] |16");
    expect(enter("- [x] done")).toBe("- [x] done\n- [ ] |17");
  });
  test("indentation is kept", () => {
    expect(enter("- one\n  - sub")).toBe("- one\n  - sub\n  - |18");
  });
  test("Enter on an empty item ends the list", () => {
    expect(enter("- one\n- ")).toBe("- one\n|6");
    expect(enter("- one\n- [ ] ")).toBe("- one\n|6");
    expect(enter("1. one\n2. ")).toBe("1. one\n|7");
  });
  test("mid-item, the rest of the line moves to the new item", () => {
    expect(enter("- one two", 5)).toBe("- one\n-  two|8");
  });
  test("not a list line: Enter is Enter", () => {
    expect(continueList("plain", 5)).toBeNull();
    expect(continueList("-not a list", 11)).toBeNull();
    expect(continueList("## Heading", 10)).toBeNull();
  });
});

describe("the pieces around a document being written", () => {
  test("a title-sized Input keeps the field's anatomy and says what the label needs", () => {
    const h = renderToStaticMarkup(<Input size="title" label="Title" labelNote="required" defaultValue="x" data-testid="t" />);
    const label = tag(h, "<label");
    const id = /for="([^"]+)"/.exec(label)![1]!;
    expect(tag(h, "<input")).toContain(`id="${id}"`);
    expect(h).toMatch(/<label[^>]*>Title<span[^>]*> · required<\/span><\/label>/);
  });

  test("the cheatsheet lists the editor's shortcuts as key caps", () => {
    const h = renderToStaticMarkup(<MarkdownCheatsheet />);
    for (const source of ["**bold**", "_italic_", "[text](url)", "`code`", "- [ ] item", "## Heading"]) expect(h).toContain(source);
    expect([...h.matchAll(/<kbd[^>]*>([^<]+)<\/kbd>/g)].map((m) => m[1])).toEqual([modKey(), "B", modKey(), "I", modKey(), "K", modKey(), "E"]);
  });

  test("a key hint is its keys and what they do", () => {
    const h = renderToStaticMarkup(<KeyHint keys={["mod", "Enter"]}>create</KeyHint>);
    expect(h.replace(/<[^>]+>/g, "")).toBe(`${modKey()}Entercreate`);
  });
});
