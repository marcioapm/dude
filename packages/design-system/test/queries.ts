/**
 * Finding elements as assistive technology names them, for the component
 * tests: by role and accessible name, and by label. A small subset of the
 * ARIA rules (explicit role, the implicit roles of the elements these
 * tests meet; name from aria-labelledby, aria-label, a label, or text),
 * enough that a field's lost label or a button's lost name fails a test.
 */

const IMPLICIT: Record<string, (el: Element) => boolean> = {
  button: (el) => el.tagName === "BUTTON",
  textbox: (el) =>
    el.tagName === "TEXTAREA" ||
    (el.tagName === "INPUT" && ["", "text", "email", "search", "url", "tel"].includes((el.getAttribute("type") ?? "").toLowerCase())),
  table: (el) => el.tagName === "TABLE",
  row: (el) => el.tagName === "TR",
  cell: (el) => el.tagName === "TD",
  columnheader: (el) => el.tagName === "TH",
  link: (el) => el.tagName === "A" && el.hasAttribute("href"),
  heading: (el) => /^H[1-6]$/.test(el.tagName),
};

function roleOf(el: Element, role: string): boolean {
  const explicit = el.getAttribute("role");
  if (explicit) return explicit.split(/\s+/).includes(role);
  return IMPLICIT[role]?.(el) ?? false;
}

function hidden(el: Element): boolean {
  for (let n: Element | null = el; n; n = n.parentElement) {
    if (n.getAttribute("aria-hidden") === "true" || n.hasAttribute("hidden")) return true;
  }
  return false;
}

function text(el: Element): string {
  return (el.textContent ?? "").replace(/\s+/g, " ").trim();
}

/** The element's accessible name. */
export function accessibleName(el: Element): string {
  const doc = el.ownerDocument;
  const by = el.getAttribute("aria-labelledby");
  if (by) return by.split(/\s+/).map((id) => doc.getElementById(id)).filter(Boolean).map((n) => text(n!)).join(" ");
  const label = el.getAttribute("aria-label");
  if (label) return label.trim();
  if (el.id) {
    const forIt = doc.querySelector(`label[for="${CSS.escape(el.id)}"]`);
    if (forIt) return text(forIt);
  }
  const wrapping = el.closest("label");
  if (wrapping) return text(wrapping);
  if (roleOf(el, "textbox") || (el.tagName === "INPUT")) return "";
  return text(el);
}

/** Every element in scope of this role whose name is name (exactly), not hidden from assistive technology. */
export function allByRole(scope: ParentNode, role: string, name?: string): HTMLElement[] {
  return [...scope.querySelectorAll<HTMLElement>("*")].filter(
    (el) => roleOf(el, role) && !hidden(el) && (name === undefined || accessibleName(el) === name),
  );
}

/** The one element of this role and name; throws, saying what there is, otherwise. */
export function byRole<T extends HTMLElement = HTMLElement>(scope: ParentNode, role: string, name?: string): T {
  const found = allByRole(scope, role, name);
  if (found.length !== 1) {
    const all = allByRole(scope, role).map((el) => JSON.stringify(accessibleName(el)));
    throw new Error(`${found.length} ${role} named ${JSON.stringify(name)}; the ${role}s are ${all.join(", ") || "none"}`);
  }
  return found[0] as T;
}

/** The control a label of exactly this text names (a <label for>, or aria-label). */
export function byLabel<T extends HTMLElement = HTMLElement>(scope: ParentNode, label: string): T {
  const found = [...scope.querySelectorAll<HTMLElement>("input, textarea, select, [role]")].filter(
    (el) => !hidden(el) && accessibleName(el) === label,
  );
  if (found.length !== 1) throw new Error(`${found.length} controls labelled ${JSON.stringify(label)}`);
  return found[0] as T;
}
