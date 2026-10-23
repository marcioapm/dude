import type { MouseEvent } from "react";

/**
 * A link's onClick that hands a plain left click to `onOpen` (the app
 * opening the URL its own way) and keeps the browser's default when
 * `onOpen` declines (returns false). A click with a modifier key, another
 * button or already handled is left to the browser, as is every click
 * when there is no `onOpen`: the element stays a real `<a href>`, so
 * middle-click, copy address and open in a new tab keep working.
 */
export function openLinkWith(onOpen: ((url: string) => boolean) | undefined, url: string): ((e: MouseEvent<HTMLAnchorElement>) => void) | undefined {
  if (!onOpen) return undefined;
  return (e) => {
    if (e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return;
    if (onOpen(url)) e.preventDefault();
  };
}
