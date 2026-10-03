/**
 * For the tests that drag an image in Preview: happy-dom lays nothing out
 * and has no DataTransfer, so the rects and the drag's data are stubbed.
 */

/** A box at `top`, `height` tall and `width` wide, from the left edge. */
export function rect(top: number, height: number, width = 100): DOMRect {
  return { top, bottom: top + height, left: 0, right: width, width, height, x: 0, y: top, toJSON() {} };
}

/** Lays `els` out down the page, one every `step` px, each `height` tall. */
export function stackRects(els: Iterable<Element>, step: number, height: number): void {
  [...els].forEach((el, i) => (el.getBoundingClientRect = () => rect(i * step, height)));
}

/** One drag's shared data, and `dnd` to fire its events (dragstart, dragover, drop) at a height. */
export function imageDrag(data: Record<string, string> = {}) {
  const store = new Map(Object.entries(data));
  const dataTransfer = {
    get types() {
      return [...store.keys()];
    },
    setData: (t: string, v: string) => void store.set(t, v),
    getData: (t: string) => store.get(t) ?? "",
    effectAllowed: "",
    dropEffect: "",
  };
  return (type: string, el: Element, clientY = 0) => {
    const e = new Event(type, { bubbles: true, cancelable: true }) as Event & { dataTransfer: unknown; clientY: number };
    e.dataTransfer = dataTransfer;
    e.clientY = clientY;
    el.dispatchEvent(e);
  };
}
