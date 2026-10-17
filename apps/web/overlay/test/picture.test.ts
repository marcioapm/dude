/**
 * The picture's clone in happy-dom. happy-dom ignores getComputedStyle's
 * pseudo-element argument, so each test's ::before / ::after are given as
 * the computed declarations a browser would return; the drawing itself is
 * checked in a real browser (apps/web/test/browser_preview_feedback.py).
 */

import { expect, test } from "bun:test";
import { PictureError, cloneWithStyles, contentParts, fetchedUrl } from "../picture.ts";

const Window = (globalThis.window as unknown as { constructor: new (o: object) => any }).constructor;

/** A page whose elements, by selector, have the given ::before / ::after declarations. */
function page(html: string, pseudos: Record<string, string> = {}) {
  const win = new Window({ url: "https://shop.preview.test/", width: 1280, height: 720 });
  win.document.body.innerHTML = html;
  const real = win.getComputedStyle.bind(win);
  win.getComputedStyle = (el: Element, which?: string) => {
    if (!which) return real(el);
    const decl = win.document.createElement("span").style;
    decl.cssText = "content: none";
    for (const [key, css] of Object.entries(pseudos)) {
      const [sel, pseudo] = key.split("|");
      if (pseudo === which && el.matches(sel!)) decl.cssText = css;
    }
    return decl;
  };
  return win;
}

const clone = (win: any, sel = "#it") => cloneWithStyles(win.document.querySelector(sel), win);

test("::before is the clone's first child and ::after its last, with their own styles and text", async () => {
  const win = page(`<p id="it" data-n="7">Cart</p>`, {
    "#it|::before": `content: "\\2605  " attr(data-n) " " open-quote; color: rgb(255, 0, 0); font-weight: 700`,
    "#it|::after": `content: "!"; color: rgb(0, 0, 255)`,
  });
  const out = await clone(win);
  expect(out.childNodes.length).toBe(3);
  const [before, text, after] = [...out.childNodes] as Element[];
  expect(before!.localName).toBe("span");
  expect(before!.textContent).toBe("\u2605 7 \u201c");
  expect(before!.getAttribute("style")).toContain("color:rgb(255, 0, 0);");
  expect(before!.getAttribute("style")).toContain("font-weight:700;");
  expect(before!.getAttribute("style")).not.toContain("content:");
  expect(text!.textContent).toBe("Cart");
  expect(after!.textContent).toBe("!");
  expect(after!.getAttribute("style")).toContain("color:rgb(0, 0, 255);");
});

test("an element with no pseudo content, or one browsers draw none for, gets no span", async () => {
  const win = page(`<div id="it"><b>a</b><img src="data:image/png;base64,iVBORw0KGgo="><input></div>`, {
    "b|::before": `content: normal`,
    "img|::before": `content: "x"`,
    "input|::after": `content: "x"`,
  });
  const out = await clone(win);
  expect(out.querySelectorAll("span").length).toBe(0);
  expect(out.querySelector("b")!.childNodes.length).toBe(1);
});

test("a data: image in content is drawn; an image only a request could get leaves that pseudo out", async () => {
  const win = page(`<div id="it"><b>a</b><i>b</i><u>c</u></div>`, {
    "b|::before": `content: url("data:image/png;base64,iVBORw0KGgo=") " new"`,
    "i|::before": `content: url("https://cdn.test/star.png")`,
    "u|::after": `content: "!"; background-image: url("https://cdn.test/bg.png")`,
  });
  const out = await clone(win);
  const b = out.querySelector("b")!.firstElementChild!;
  expect(b.querySelector("img")!.getAttribute("src")).toBe("data:image/png;base64,iVBORw0KGgo=");
  expect(b.textContent).toBe(" new");
  expect(out.querySelector("i")!.querySelectorAll("span").length).toBe(0);
  expect(out.querySelector("u")!.querySelectorAll("span").length).toBe(0);
  expect(out.querySelectorAll("img").length).toBe(1);
});

test("pseudo spans count against the 2,000-node cap", async () => {
  const items = "<i></i>".repeat(1_998);
  const plain = page(`<div id="it">${items}</div>`);
  expect((await clone(plain)).childElementCount).toBe(1_998);
  const marked = page(`<div id="it">${items}</div>`, { "i|::before": `content: "•"` });
  await expect(clone(marked)).rejects.toThrow(new PictureError("it is too large to draw"));
});

const DATA = "data:image/png;base64,iVBORw0KGgo=";
const REMOTE = "https://cdn.test/a.png";
const noCopy = new PictureError("it has a background image dude can't copy");

test.each([
  `background-image: url("${DATA}"), url("${REMOTE}")`,
  `background-image: url("${REMOTE}"), url("${DATA}")`,
  `background-image: url(${DATA}), url(${DATA}), url(${REMOTE})`,
  `mask-image: url("${DATA}"), url("${REMOTE}")`,
  `cursor: url("${DATA}"), url("${REMOTE}"), auto`,
  `list-style-image: url("${REMOTE}")`,
  `border-image-source: url("${REMOTE}")`,
])("a remote url() fails the picture, whichever layer it is: %s", async (style) => {
  const win = page(`<div id="it"><b style='${style}'>a</b></div>`);
  await expect(clone(win)).rejects.toThrow(noCopy);
});

test("data: url()s in every layer are drawn", async () => {
  const win = page(`<p id="it" style='background-image: url("${DATA}"), url("${DATA}")'>a</p>`);
  expect((await clone(win)).getAttribute("style")).toContain("background-image:");
});

test("a value is fetched when any url() in it is not data:, read token by token", () => {
  const svg = `url("data:image/svg+xml;utf8,<svg xmlns='http://www.w3.org/2000/svg'><rect fill='url(%23g)'/></svg>")`;
  expect(fetchedUrl(`${svg}, url("${DATA}")`)).toBe(false);
  expect(fetchedUrl(`${svg}, url("${REMOTE}")`)).toBe(true);
  expect(fetchedUrl(`url( '${DATA}' ), url(${DATA})`)).toBe(false);
  expect(fetchedUrl(`image-set(url("${REMOTE}") 1dppx)`)).toBe(true);
  expect(fetchedUrl(`url("${DATA}"), url(`)).toBe(true);
  expect(fetchedUrl(`linear-gradient(red, blue)`)).toBe(false);
});

test("a pseudo whose second background layer is remote is left out", async () => {
  const win = page(`<p id="it">a</p>`, { "#it|::before": `content: "!"; background-image: url("${DATA}"), url("${REMOTE}")` });
  expect((await clone(win)).querySelectorAll("span").length).toBe(0);
});

test("content: strings with escapes, attr(), quotes, counters left out, alt text after /", () => {
  const el = page(`<p data-n="3">x</p>`).document.querySelector("p");
  expect(contentParts(`"a \\"b\\" \\'c" 'd'`, el, "auto")).toEqual([`a "b" 'c`, "d"]);
  expect(contentParts(`attr(data-n) attr(data-missing)`, el, "auto")).toEqual(["3", ""]);
  expect(contentParts(`open-quote "q" close-quote`, el, `"«" "»"`)).toEqual(["«", "q", "»"]);
  expect(contentParts(`"n" counter(c) "." counters(c, ".") ")"`, el, "auto")).toEqual(["n", ".", ")"]);
  expect(contentParts(`"\\1F600 x" / "smile"`, el, "auto")).toEqual(["\u{1F600}x"]);
  expect(contentParts(`url(https://cdn.test/a.png)`, el, "auto")).toBeNull();
});
