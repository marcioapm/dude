/**
 * Whether the person is using this tab now: it is shown, it has focus, and
 * they gave it input within ACTIVE_WINDOW_MS. Requests say so with
 * `x-dude-active` (api/client.ts), and only those count as the person being
 * here (control plane, api/presence.ts). Opening the page counts as input.
 */

export const ACTIVE_WINDOW_MS = 5 * 60_000;

export interface DocumentLike {
  visibilityState: string;
  hasFocus(): boolean;
}

type Listening = Pick<EventTarget, "addEventListener">;

export interface Activity {
  markActive(now: number): void;
  isActive(now: number): boolean;
  /** Listen for input on `win` and `doc`, once; later calls do nothing. */
  install(win: Listening, doc: Listening & DocumentLike, clock?: () => number): void;
}

const INPUT = ["pointerdown", "keydown", "wheel", "touchstart", "focus"] as const;

export function createActivity(doc: DocumentLike, loadedAt: number): Activity {
  let last = loadedAt;
  let installed = false;
  const markActive = (now: number) => {
    if (now > last) last = now;
  };
  return {
    markActive,
    isActive: (now) => doc.visibilityState === "visible" && doc.hasFocus() && now - last < ACTIVE_WINDOW_MS,
    install(win, d, clock = Date.now) {
      if (installed) return;
      installed = true;
      const opts = { passive: true } as const;
      const mark = () => markActive(clock());
      for (const type of INPUT) win.addEventListener(type, mark, opts);
      // scroll does not bubble, and the app scrolls its panes, not the window.
      win.addEventListener("scroll", mark, { passive: true, capture: true });
      d.addEventListener("visibilitychange", () => {
        if (d.visibilityState === "visible") mark();
      }, opts);
    },
  };
}

// This page's: created when the app's code first loads, which is the page load.
const page = typeof document === "undefined" ? null : createActivity(document, Date.now());

export function markActive(now: number): void {
  page?.markActive(now);
}

export function isActive(now: number): boolean {
  return page?.isActive(now) ?? false;
}

export function installActivity(): void {
  page?.install(window, document);
}
