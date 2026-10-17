/**
 * The script lux adds to a branch preview's pages:
 * `<script src="<dude>/feedback/overlay.js" data-dude-origin="<dude>" async>`.
 * dude's origin comes from that tag alone. Built by `bun run build:overlay`
 * into public/feedback/overlay.js, which the web app's build serves.
 */

import { Overlay } from "./overlay.ts";

type Marked = Window & { __dudeOverlay?: boolean };

(function boot() {
  const win = window as Marked;
  if (win.__dudeOverlay || window.top !== window) return;
  const script = document.currentScript as HTMLScriptElement | null;
  const origin = script?.dataset["dudeOrigin"] ?? "";
  // An origin as the browser writes one, or nothing: postMessage's target must be exact.
  let dudeOrigin: string;
  try {
    dudeOrigin = new URL(origin).origin;
  } catch {
    return;
  }
  if (dudeOrigin !== origin || dudeOrigin === "null") return;
  win.__dudeOverlay = true;
  const go = () => new Overlay(window, { dudeOrigin }).start();
  if (document.body) go();
  else document.addEventListener("DOMContentLoaded", go, { once: true });
})();
