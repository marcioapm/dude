/**
 * Focus return for layers that open over the page (a dialog, the narrow
 * sidebar's drawer): remember what had focus when the layer opened, and
 * put it back when the layer closes — so Escape leaves the keyboard where
 * it was, not on `<body>`.
 */

/** What has focus now, to hand back to `returnFocus` later. */
export function focusedElement(doc: Document | undefined = typeof document === "undefined" ? undefined : document): Element | null {
  const el = doc?.activeElement ?? null;
  return el && el !== doc?.body ? el : null;
}

/**
 * Focus `target` again if it is still on the page. False when there is
 * nothing to return to (it was removed, or nothing had focus), so the
 * caller can fall back.
 */
export function returnFocus(target: Element | null | undefined): boolean {
  if (!target || !target.isConnected) return false;
  const focusable = target as Partial<HTMLElement>;
  if (typeof focusable.focus !== "function") return false;
  focusable.focus({ preventScroll: true });
  return true;
}

/**
 * A Radix `onCloseAutoFocus`: the caller's handler first; unless it
 * prevented the default, focus goes back to `opener()` and Radix's own
 * fallback (its Trigger, or `<body>`) is skipped.
 */
export function closeAutoFocus(opener: () => Element | null, handler?: ((e: Event) => void) | undefined): (e: Event) => void {
  return (e) => {
    handler?.(e);
    if (!e.defaultPrevented && returnFocus(opener())) e.preventDefault();
  };
}
