/**
 * A picture of one element, drawn from the page itself: the element and its
 * descendants (with their ::before and ::after) cloned with every computed
 * style inlined, laid out in an SVG foreignObject, drawn into a canvas and
 * outlined. No screen capture and no request: an image the page has already
 * loaded is copied through a canvas, and one the page may not read (another
 * origin without CORS) makes the picture fail rather than come out blank
 * where it was.
 */

import { OVERLAY_LIMITS } from "@dude/domain/previewOverlayProtocol";

const MAX_NODES = 2_000;
const PAD = 8;
const OUTLINE = "#7c5cff";

export class PictureError extends Error {}

/** Whether a picture is off by default: the element does not fit the window. */
export function tooBig(el: Element, win: Window): boolean {
  const r = el.getBoundingClientRect();
  return r.width > win.innerWidth || r.height > win.innerHeight || r.width < 1 || r.height < 1;
}

// One url() token, its URL in group 1, 2 or 3; a quoted one is read whole, so a data: SVG's own `url(#g)` is part of
// its URL. No two parts can take the same characters (the unquoted run has no space, quote or paren), so a failed
// match backtracks in linear time instead of trying every split of a run of spaces.
const URL_SRC = String.raw`url\((?:\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|([^\s"'()]+)))?\s*\)`;
const URL_TOKEN = new RegExp(URL_SRC, "g");

// A value naming any url() whose bytes are not in the page's reach (drawing it would need a request), or a url( not read as a token.
export const fetchedUrl = (v: string) => /url\(/i.test(v)
  && ([...v.matchAll(URL_TOKEN)].some((m) => !(m[1] ?? m[2] ?? m[3] ?? "").trimStart().startsWith("data:"))
    || /url\(/i.test(v.replace(URL_TOKEN, "")));

/** A computed style as an inline one; null when a property names a url() dude can't copy. */
function styleText(cs: CSSStyleDeclaration, skip?: string): string | null {
  let css = "";
  for (let i = 0; i < cs.length; i++) {
    const p = cs[i]!;
    if (p === skip) continue;
    const v = cs.getPropertyValue(p);
    if (fetchedUrl(v)) return null;
    css += `${p}:${v};`;
  }
  return css;
}

function inlineStyles(from: Element, to: Element, win: Window): void {
  const css = styleText(win.getComputedStyle(from));
  // A background another origin serves would draw as nothing: the picture fails instead.
  if (css === null) throw new PictureError("it has a background image dude can't copy");
  to.setAttribute("style", css);
}

const unescapeCss = (s: string) => s.replace(/\\([0-9a-fA-F]{1,6})\s?|\\(.)/g, (_, hex: string | undefined, ch: string | undefined) =>
  hex ? String.fromCodePoint(parseInt(hex, 16)) : ch!);

// A content value's tokens: string (1, 2), url() (3, its URL in 4-6), attr() (7), counter, [no-]quote (8, 9), alt-text slash (10).
const CONTENT_TOKEN = new RegExp(String.raw`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|(${URL_SRC})|attr\(\s*([\w-]+)[^)]*\)|counters?\((?:[^)"']|"[^"]*"|'[^']*')*\)|(no-)?(open|close)-quote|(\/)`, "g");

type ContentPart = string | { img: string };

/**
 * What a computed `content` shows, as text and data-URL images; null when
 * it names an image only a request could get. Counters are left out: no
 * API gives their value. What follows a top-level `/` is alt text.
 */
export function contentParts(content: string, el: Element, quotes: string): ContentPart[] | null {
  const q = quotes === "auto" ? ["\u201c", "\u201d"] : [...quotes.matchAll(/"((?:[^"\\]|\\.)*)"/g)].map((m) => unescapeCss(m[1]!));
  const parts: ContentPart[] = [];
  // Quote nesting within this one value; depth an ancestor's quotes left open is not known here.
  let depth = 0;
  for (const m of content.matchAll(CONTENT_TOKEN)) {
    if (m[10]) break;
    if (m[1] !== undefined || m[2] !== undefined) parts.push(unescapeCss(m[1] ?? m[2]!));
    else if (m[3] !== undefined) {
      // Chrome serialises a url() quoted, escaping an inner `"` as `\"`.
      const u = unescapeCss(m[4] ?? m[5] ?? m[6] ?? "").trim();
      if (!u.startsWith("data:")) return null;
      parts.push({ img: u });
    } else if (m[7]) parts.push(el.getAttribute(m[7]) ?? "");
    else if (m[9]) {
      const open = m[9] === "open";
      if (!open) depth = Math.max(0, depth - 1);
      const pair = Math.max(0, Math.min(depth, (q.length >> 1) - 1));
      if (!m[8]) parts.push(q[2 * pair + (open ? 0 : 1)] ?? "");
      if (open) depth++;
    }
  }
  return parts;
}

// Elements browsers draw no ::before / ::after for; canvas, video and frames never get this far.
const NO_PSEUDO = new Set(["img", "input", "textarea", "select", "br", "hr", "wbr", "audio", "embed", "object"]);

/** The element's ::before or ::after as a styled span, or null when it has none dude can draw. */
function pseudoSpan(el: Element, which: "::before" | "::after", win: Window, count: () => void): Element | null {
  const cs = win.getComputedStyle(el, which);
  const content = cs.getPropertyValue("content");
  // fetchedUrl, not only contentParts: contentParts stops at the first top-level `/` it reads, and a `/` inside
  // a function, as in `oklch(0.5 0.1 30 / 0.5)`, ends it before a remote url() later in the value.
  if (!content || content === "none" || content === "normal" || fetchedUrl(content)) return null;
  const parts = contentParts(content, el, cs.getPropertyValue("quotes") || "auto");
  // `content` is dropped from the span's style: on an element, a url() there would replace it.
  const css = parts && styleText(cs, "content");
  if (parts === null || css === null) return null;
  count();
  const span = win.document.createElement("span");
  span.setAttribute("style", css);
  for (const p of parts) {
    if (typeof p === "string") span.append(p);
    else {
      count();
      const img = win.document.createElement("img");
      img.setAttribute("src", p.img);
      span.append(img);
    }
  }
  return span;
}

function imageData(img: HTMLImageElement | HTMLCanvasElement | HTMLVideoElement, doc: Document): string {
  const c = doc.createElement("canvas");
  // A video's width and height are its attributes (0 when unset): its frame's size is videoWidth/videoHeight.
  const v = img as HTMLVideoElement;
  c.width = v.videoWidth || (img as HTMLImageElement).naturalWidth || img.width || 1;
  c.height = v.videoHeight || (img as HTMLImageElement).naturalHeight || img.height || 1;
  const ctx = c.getContext("2d");
  if (!ctx) throw new PictureError("this browser can't draw it");
  ctx.drawImage(img, 0, 0);
  try {
    return c.toDataURL("image/png");
  } catch {
    throw new PictureError("it shows an image from another site");
  }
}

// Nodes cloned between yields to the page, so a large element doesn't hold the main thread for its whole walk.
const NODES_PER_YIELD = 200;

/** The element and its descendants with styles inlined; ::before and ::after spans count against MAX_NODES. */
export async function cloneWithStyles(root: Element, win: Window): Promise<Element> {
  // Counted before any style is read: an element over the cap costs one native count, not a walk.
  if (root.getElementsByTagName("*").length + 1 > MAX_NODES) throw new PictureError("it is too large to draw");
  let nodes = 0, nextPause = NODES_PER_YIELD;
  const count = () => {
    if (++nodes > MAX_NODES) throw new PictureError("it is too large to draw");
  };
  const pause = () => new Promise<void>((r) => {
    if (win.requestAnimationFrame) win.requestAnimationFrame(() => r());
    else win.setTimeout(r, 0);
  });
  const walk = async (el: Element): Promise<Element> => {
    count();
    if (nodes >= nextPause) {
      nextPause = nodes + NODES_PER_YIELD;
      await pause();
    }
    const name = el.localName;
    let out: Element;
    if (name === "canvas" || name === "video") {
      out = win.document.createElement("img");
      out.setAttribute("src", imageData(el as HTMLCanvasElement, win.document));
    } else if (name === "iframe" || name === "frame") {
      throw new PictureError("it holds a frame");
    } else {
      out = el.cloneNode(false) as Element;
      for (const a of [...out.attributes]) if (/^on/i.test(a.name) || a.name === "srcset") out.removeAttribute(a.name);
      if (name === "img") {
        const src = (el as HTMLImageElement).currentSrc || (el as HTMLImageElement).src;
        if (src && !src.startsWith("data:")) out.setAttribute("src", imageData(el as HTMLImageElement, win.document));
      }
      if (name === "input" || name === "textarea") out.setAttribute("value", (el as HTMLInputElement).value);
      const pseudo = !NO_PSEUDO.has(name);
      const before = pseudo && pseudoSpan(el, "::before", win, count);
      if (before) out.appendChild(before);
      for (const c of el.childNodes) {
        if (c.nodeType === 1) out.appendChild(await walk(c as Element));
        else if (c.nodeType === 3) out.appendChild(c.cloneNode());
      }
      const after = pseudo && pseudoSpan(el, "::after", win, count);
      if (after) out.appendChild(after);
    }
    inlineStyles(el, out, win);
    return out;
  };
  return walk(root);
}

function load(src: string, win: Window): Promise<HTMLImageElement> {
  return new Promise((resolve, reject) => {
    const img = win.document.createElement("img");
    const t = win.setTimeout(() => reject(new PictureError("it took too long to draw")), 4_000);
    img.onload = () => { win.clearTimeout(t); resolve(img); };
    img.onerror = () => { win.clearTimeout(t); reject(new PictureError("the browser couldn't draw it")); };
    img.src = src;
  });
}

/**
 * The element as a PNG (or, past the cap, JPEG) data URL, outlined, at
 * most OVERLAY_LIMITS.pictureSide pixels on its long side. Throws a
 * PictureError saying why when it cannot be drawn.
 */
export async function drawPicture(el: Element, win: Window): Promise<string> {
  const r = el.getBoundingClientRect();
  const w = Math.ceil(r.width), h = Math.ceil(r.height);
  if (w < 1 || h < 1) throw new PictureError("it has no size");
  const clone = await cloneWithStyles(el, win);
  clone.setAttribute("style", `${clone.getAttribute("style")}margin:0;position:static;transform:none;`);
  const xhtml = new XMLSerializer().serializeToString(clone);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="${w}" height="${h}"><foreignObject x="0" y="0" width="100%" height="100%">`
    + `<div xmlns="http://www.w3.org/1999/xhtml" style="width:${w}px;height:${h}px">${xhtml}</div></foreignObject></svg>`;
  const img = await load(`data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`, win);
  const scale = Math.min(win.devicePixelRatio || 1, 2, OVERLAY_LIMITS.pictureSide / (Math.max(w, h) + 2 * PAD));
  const canvas = win.document.createElement("canvas");
  canvas.width = Math.max(1, Math.round((w + 2 * PAD) * scale));
  canvas.height = Math.max(1, Math.round((h + 2 * PAD) * scale));
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new PictureError("this browser can't draw it");
  ctx.scale(scale, scale);
  ctx.fillStyle = "#ffffff";
  ctx.fillRect(0, 0, w + 2 * PAD, h + 2 * PAD);
  ctx.drawImage(img, PAD, PAD, w, h);
  ctx.strokeStyle = OUTLINE;
  ctx.lineWidth = 3;
  ctx.strokeRect(PAD - 3, PAD - 3, w + 6, h + 6);
  let url: string;
  try {
    url = canvas.toDataURL("image/png");
    if (url.length > OVERLAY_LIMITS.picture) url = canvas.toDataURL("image/jpeg", 0.85);
  } catch {
    throw new PictureError("the page doesn't let it be copied");
  }
  if (url.length > OVERLAY_LIMITS.picture) throw new PictureError("it is too large to send");
  if (!/^data:image\/(png|jpeg);base64,/.test(url)) throw new PictureError("this browser can't encode it");
  return url;
}
