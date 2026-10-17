/**
 * What the overlay reads of a page's element: a selector that finds it
 * and nothing else, a component when the app says, its text, HTML and
 * box. Only what the page itself shows; nothing leaves until a save.
 */

import { OVERLAY_LIMITS, type OverlayAnchor } from "@dude/domain/previewOverlayProtocol";

const esc = (s: string) => (typeof CSS !== "undefined" && CSS.escape ? CSS.escape(s) : s.replace(/[^\w-]/g, (c) => `\\${c}`));

/** Whether `selector` finds exactly one element in `doc`, and which; an invalid selector finds none. */
export function resolveOne(doc: Document, selector: string): Element | null {
  if (!selector) return null;
  try {
    const all = doc.querySelectorAll(selector);
    return all.length === 1 ? all[0]! : null;
  } catch {
    return null;
  }
}

// Class names a build hashes or a framework toggles say nothing that lasts.
const UNSTABLE_CLASS = /^(css-|sc-|jsx-|_)|[0-9a-f]{5,}|^(hover|focus|active|open|selected|is-|has-)/i;

function step(el: Element): string {
  const tag = el.localName;
  const classes = [...el.classList].filter((c) => !UNSTABLE_CLASS.test(c)).slice(0, 2).map((c) => `.${esc(c)}`).join("");
  const parent = el.parentElement;
  let s = tag + classes;
  if (parent) {
    const same = [...parent.children].filter((c) => c.localName === tag && (classes === "" || c.matches(s)));
    if (same.length > 1) s += `:nth-of-type(${[...parent.children].filter((c) => c.localName === tag).indexOf(el) + 1})`;
  }
  return s;
}

/**
 * A selector that finds `el` alone in its document: a unique id or test id
 * when it has one, else steps up its ancestors (a unique id stops the walk)
 * until the path is unique.
 */
export function selectorFor(el: Element): string {
  const doc = el.ownerDocument;
  const own = uniqueOwn(el);
  if (own) return own;
  const steps: string[] = [];
  for (let at: Element | null = el; at && at !== doc.documentElement; at = at.parentElement) {
    const anchor = at !== el ? uniqueOwn(at) : null;
    if (anchor) {
      steps.unshift(anchor);
      break;
    }
    steps.unshift(step(at));
    const sel = steps.join(" > ");
    if (resolveOne(doc, sel) === el) return sel;
  }
  const sel = steps.join(" > ");
  return resolveOne(doc, sel) === el ? sel : fullPath(el);
}

function uniqueOwn(el: Element): string | null {
  const doc = el.ownerDocument;
  const tries: string[] = [];
  if (el.id && !/^\d|[0-9a-f]{8,}/i.test(el.id)) tries.push(`#${esc(el.id)}`);
  for (const a of ["data-testid", "data-test", "data-cy"]) {
    const v = el.getAttribute(a);
    if (v) tries.push(`${el.localName}[${a}="${v.replace(/["\\]/g, "\\$&")}"]`);
  }
  return tries.find((t) => resolveOne(doc, t) === el) ?? null;
}

function fullPath(el: Element): string {
  const steps: string[] = [];
  for (let at: Element | null = el; at && at.parentElement; at = at.parentElement) {
    steps.unshift(`${at.localName}:nth-child(${[...at.parentElement.children].indexOf(at) + 1})`);
  }
  return [el.ownerDocument.documentElement.localName, ...steps].join(" > ");
}

export interface Component {
  name?: string | undefined;
  file?: string | undefined;
  line?: number | undefined;
}

/**
 * The component that drew `el`, when the app says: a `data-component`
 * (and `data-source="file:line"`) on it or an ancestor, else React's own
 * fiber in a development build (its name; the source where React keeps it).
 */
export function componentOf(el: Element): Component | undefined {
  const marked = el.closest("[data-component]");
  if (marked) {
    const out: Component = { name: marked.getAttribute("data-component")!.slice(0, 200) };
    const src = /^(.+):(\d+)$/.exec(marked.getAttribute("data-source") ?? "");
    if (src) Object.assign(out, { file: src[1]!.slice(0, 1_000), line: Number(src[2]) });
    return out;
  }
  const key = Object.keys(el).find((k) => k.startsWith("__reactFiber$"));
  type Fiber = { type?: unknown; return?: Fiber | null; _debugSource?: { fileName?: string; lineNumber?: number } };
  for (let f = key ? (el as unknown as Record<string, Fiber>)[key] : undefined, n = 0; f && n < 50; f = f.return ?? undefined, n++) {
    const t = f.type as { displayName?: string; name?: string } | string | undefined;
    const name = typeof t === "function" || (typeof t === "object" && t) ? (t as { displayName?: string; name?: string }).displayName ?? (t as { name?: string }).name : undefined;
    if (name && /^[A-Z]/.test(name)) {
      const out: Component = { name: name.slice(0, 200) };
      if (f._debugSource?.fileName) Object.assign(out, { file: f._debugSource.fileName.slice(0, 1_000), line: f._debugSource.lineNumber });
      return out;
    }
  }
  return undefined;
}

/** "<GreetButton> button.primary" or "form#greet › button": what the hover label calls it. */
export function labelFor(selector: string, component?: Component): string {
  const steps = selector.split(/\s*>\s*/);
  return component?.name ? `<${component.name}> ${steps.at(-1)}` : steps.slice(-2).join(" › ");
}

const oneLine = (s: string) => s.replace(/\s+/g, " ").trim();

/** The anchor a comment on `el` is saved with, as the API's preview anchor takes it. */
export function anchorFor(el: Element, win: Window): OverlayAnchor {
  const selector = selectorFor(el);
  const r = el.getBoundingClientRect();
  const text = oneLine((el as HTMLElement).innerText ?? el.textContent ?? "").slice(0, OVERLAY_LIMITS.previewText);
  const html = el.outerHTML.slice(0, OVERLAY_LIMITS.previewHtml);
  const component = componentOf(el);
  const anchor: OverlayAnchor = {
    kind: "preview",
    url: win.location.href.slice(0, 2_048),
    viewport: { width: Math.max(1, Math.round(win.innerWidth)), height: Math.max(1, Math.round(win.innerHeight)) },
    selector: selector.slice(0, 2_000),
    rect: { x: Math.round(r.left), y: Math.round(r.top), width: Math.max(0, Math.round(r.width)), height: Math.max(0, Math.round(r.height)) },
  };
  if (text) anchor.text = text;
  if (html) anchor.html = html;
  if (component) anchor.component = component;
  return anchor;
}

/** The origin of a frame the overlay cannot read into, or null for any other element (a same-origin frame is read). */
export function foreignFrame(el: Element, win: Window): string | null {
  if (el.localName !== "iframe" && el.localName !== "frame") return null;
  let readable = false;
  try {
    readable = Boolean((el as HTMLIFrameElement).contentDocument);
  } catch {
    readable = false;
  }
  if (readable) return null;
  try {
    return new URL((el as HTMLIFrameElement).src, win.location.href).origin;
  } catch {
    return "another site";
  }
}
