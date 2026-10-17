/**
 * A picture of one element, drawn from the page itself: the element and
 * its descendants cloned with every computed style inlined, laid out in
 * an SVG foreignObject, drawn into a canvas and outlined. No screen
 * capture and no request: an image the page has already loaded is copied
 * through a canvas, and one the page may not read (another origin without
 * CORS) makes the picture fail rather than come out blank where it was.
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

function inlineStyles(from: Element, to: Element, win: Window): void {
  const cs = win.getComputedStyle(from);
  let css = "";
  for (let i = 0; i < cs.length; i++) {
    const p = cs[i]!;
    let v = cs.getPropertyValue(p);
    // A background another origin serves would draw as nothing: the picture fails instead.
    if (/url\(/.test(v) && !/url\(["']?data:/.test(v)) throw new PictureError("it has a background image dude can't copy");
    css += `${p}:${v};`;
  }
  to.setAttribute("style", css);
}

function imageData(img: HTMLImageElement | HTMLCanvasElement | HTMLVideoElement, doc: Document): string {
  const c = doc.createElement("canvas");
  c.width = (img as HTMLImageElement).naturalWidth || img.width || 1;
  c.height = (img as HTMLImageElement).naturalHeight || img.height || 1;
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

async function cloneWithStyles(root: Element, win: Window): Promise<Element> {
  // Counted before any style is read: an element over the cap costs one native count, not a walk.
  if (root.getElementsByTagName("*").length + 1 > MAX_NODES) throw new PictureError("it is too large to draw");
  let nodes = 0;
  const pause = () => new Promise<void>((r) => {
    if (win.requestAnimationFrame) win.requestAnimationFrame(() => r());
    else win.setTimeout(r, 0);
  });
  const walk = async (el: Element): Promise<Element> => {
    if (++nodes > MAX_NODES) throw new PictureError("it is too large to draw");
    if (nodes % NODES_PER_YIELD === 0) await pause();
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
      for (const c of el.childNodes) {
        if (c.nodeType === 1) out.appendChild(await walk(c as Element));
        else if (c.nodeType === 3) out.appendChild(c.cloneNode());
      }
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
