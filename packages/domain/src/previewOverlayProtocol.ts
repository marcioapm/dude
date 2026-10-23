/**
 * The messages between dude's tab and the overlay a preview page loads
 * (apps/web/overlay), carried by postMessage between a window dude opened
 * and its opener. No zod here: the overlay bundles this file and nothing
 * heavier. dude validates what the overlay sends with previewOverlay.ts.
 *
 * The overlay sends: hello and navigated (the page it is on), draft (a
 * comment), prefs (its Show pins switch) and open (Open in dude). dude
 * answers: session (the page's pins, the drafts count, dude's theme and its
 * tab's window name), saved or refused (a draft's outcome), highlight (Show
 * on preview) and stop.
 */

import type { FeedbackPreviewAnchor, FeedbackStatus } from "./feedback.ts";

export const OVERLAY_PROTOCOL = 1;

export const OVERLAY_LIMITS = {
  /**
   * One message, in UTF-16 code units of its strings plus 8 per other
   * value. A picture's cap and the anchor's and body's own leave room under it.
   */
  message: Math.floor(4.5 * 1024 * 1024),
  /**
   * A picture as a data URL, in characters: 3 MiB of image, inside what
   * one agent message carries (ATTACHMENT_LIMITS.deliveredBytes 4.5 MiB,
   * messageBytes 5 MiB) with room for the item's words.
   */
  picture: 4 * 1024 * 1024,
  /** The long side of a picture, in pixels (ATTACHMENT_LIMITS.maxSide). */
  pictureSide: 2000,
  /** A pin's words and each reply's, as the overlay shows them (it shows a thread, never edits it). */
  pinBody: 600,
  replyBody: 400,
  replies: 5,
  /** A pin's author, a reply's author and a pin's when, as the overlay shows them. */
  name: 200,
  /** dude's tab's window name, which the overlay's Go to dude looks up with window.open("", name). */
  windowName: 100,
  /** Pins sent for one page. */
  pins: 200,
  /** FEEDBACK_LIMITS' own, repeated so the overlay bundles no zod (a test holds them equal). */
  body: 16_000,
  previewText: 4_000,
  previewHtml: 16_000,
} as const;

/** The anchor the overlay builds: the API's preview anchor, the picture apart. */
export type OverlayAnchor = Omit<FeedbackPreviewAnchor, "attachmentId">;

export type OverlayToDude =
  | { type: "hello"; v: 1; url: string }
  | { type: "navigated"; v: 1; url: string }
  | { type: "draft"; v: 1; nonce: string; body: string; anchor: OverlayAnchor; picture?: string }
  | { type: "prefs"; v: 1; showPins: boolean }
  | { type: "open"; v: 1; itemId: string };

/** One item on the page, as the overlay pins it: never more than it shows. */
export interface OverlayPin {
  id: string;
  /** Its number on this page, in dude's order. */
  n: number;
  status: FeedbackStatus;
  author: string;
  /** The reader's own draft. */
  mine: boolean;
  body: string;
  selector: string;
  /** "drafted 4 min ago", "submitted 1 h ago", in dude's words. */
  when: string;
  replies: Array<{ who: string; kind: "reply" | "resolve" | "reopen"; body: string }>;
}

/** dude's resolved light or dark theme, which the overlay draws in once it is connected. */
export type OverlayTheme = "light" | "dark";
export const OVERLAY_THEMES: ReadonlyArray<OverlayTheme> = ["light", "dark"];

export type DudeToOverlay =
  // `theme` is optional: an overlay or a dude from before it reads or sends none, and the overlay then follows the OS.
  // `dudeWindow` (dude's tab's window name) likewise: without it Go to dude falls back to the opener's focus().
  | { type: "session"; v: 1; task: string; url: string; pins: OverlayPin[]; drafts: number; showPins: boolean; theme?: OverlayTheme; dudeWindow?: string }
  | { type: "saved"; v: 1; nonce: string; id: string; drafts: number; picture: "attached" | "none" | "failed" }
  | { type: "refused"; v: 1; nonce?: string; reason: string }
  | { type: "highlight"; v: 1; itemId: string }
  | { type: "stop"; v: 1 };

/**
 * Whether `value` may be sent and taken as `dudeWindow`: 1 to
 * OVERLAY_LIMITS.windowName letters, digits, `-` and `_`. Names browsers
 * treat specially (`_blank`, `_self`, `_top`, `_parent`) start with `_` and
 * are refused, since `window.open("", "_blank")` would open a new tab.
 */
export function overlayWindowName(value: unknown): value is string {
  return typeof value === "string" && value.length <= OVERLAY_LIMITS.windowName && /^[A-Za-z0-9-][A-Za-z0-9_-]*$/.test(value);
}

/**
 * The page a URL names, as pins are matched to it: origin and path, the
 * query aside. The fragment is part of it when it is a hash route
 * (`#/cart`, `#!/cart`), and aside otherwise (`#top`).
 */
export function overlayPage(url: string): string | null {
  try {
    const u = new URL(url);
    return u.origin + u.pathname + (/^#!?\//.test(u.hash) ? u.hash : "");
  } catch {
    return null;
  }
}
