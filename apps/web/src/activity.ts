/**
 * Whether the person is using this tab now, which is what `x-dude-active`
 * says on requests (api/client.ts). The rule: docs/CONTRIBUTING.md, "Online
 * and last seen".
 */

export const ACTIVE_WINDOW_MS = 5 * 60_000;

// No `scroll`: a pane following its output scrolls with nobody there.
const INPUT = ["pointerdown", "keydown", "wheel", "touchstart", "focus"] as const;

type Listening = Pick<EventTarget, "addEventListener">;

interface ShownDocument extends Listening {
  visibilityState: string;
  hasFocus(): boolean;
}

export interface Activity {
  isActive(now: number): boolean;
  /** Listens once; later calls do nothing. */
  install(win: Listening, clock?: () => number): void;
}

// Opening the page counts as input: `loadedAt` is the first.
export function createActivity(doc: ShownDocument, loadedAt: number): Activity {
  let last = loadedAt;
  let installed = false;
  return {
    isActive: (now) => doc.visibilityState === "visible" && doc.hasFocus() && now - last < ACTIVE_WINDOW_MS,
    install(win, clock = Date.now) {
      if (installed) return;
      installed = true;
      const opts = { passive: true } as const;
      const mark = () => {
        last = Math.max(last, clock());
      };
      for (const type of INPUT) win.addEventListener(type, mark, opts);
      doc.addEventListener("visibilitychange", () => {
        if (doc.visibilityState === "visible") mark();
      }, opts);
    },
  };
}

// Created when the app's code first loads, which is the page load.
const page = typeof document === "undefined" ? null : createActivity(document, Date.now());

export function isActive(now: number): boolean {
  return page?.isActive(now) ?? false;
}

export function installActivity(): void {
  page?.install(window);
}
