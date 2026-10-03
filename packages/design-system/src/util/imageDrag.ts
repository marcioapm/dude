/**
 * An image dragged within the page (Preview's reposition drag). Chrome offers
 * the dragged <img> as a file too, so its `types` include "Files": the drop
 * zones use this to tell it from a file dragged in from the desktop.
 */

/** The dataTransfer type a repositioned image carries: `{ key, n }` as JSON. */
export const IMAGE_DRAG_TYPE = "application/x-dude-image";

let active = false;

const end = () => {
  active = false;
  window.removeEventListener("dragend", end, true);
  window.removeEventListener("drop", endAfterDrop, true);
};
// The drop's own handlers still run with the drag marked; a drop that moves
// the image unmounts its source, whose dragend then never reaches the window.
const endAfterDrop = () => void setTimeout(end, 0);

/** Marks an in-page image drag, from its dragstart until it ends or drops. */
export function startImageDrag(): void {
  active = true;
  window.addEventListener("dragend", end, true);
  window.addEventListener("drop", endAfterDrop, true);
}

export const endImageDrag = end;

/** Whether `types` or the page's own drag say this drag is an image moving, not a file. */
export function isImageMove(types: ReadonlyArray<string> | DOMStringList | undefined): boolean {
  return active || Array.from(types ?? []).includes(IMAGE_DRAG_TYPE);
}
