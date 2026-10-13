/**
 * A browser's globals for the tests that mount components, preloaded
 * (bunfig.toml) so they exist before React or Radix load: Radix picks its
 * layout effect by whether `document` exists when its module is evaluated.
 *
 * Only what the runtime lacks is added. Bun's own `fetch`, `Response`,
 * `URL`, streams, timers and events stay Bun's, so the backend's tests,
 * which share this process under the root `bun test`, see the runtime they
 * were written for.
 */

import { afterEach } from "bun:test";
import { Window } from "happy-dom";

const target = globalThis as Record<string, unknown>;
if (!("document" in target)) {
  const window = new Window({ url: "http://localhost/" });
  const own = window as unknown as Record<string, unknown>;
  const keys = new Set<string>();
  for (let o: object | null = window; o && o !== Object.prototype; o = Object.getPrototypeOf(o)) {
    for (const key of Object.getOwnPropertyNames(o)) keys.add(key);
  }
  // DOM events must be happy-dom's: its nodes dispatch only their own kind.
  const replace = new Set(["Event", "CustomEvent", "EventTarget", "KeyboardEvent", "MouseEvent", "PointerEvent", "FocusEvent", "UIEvent", "InputEvent"]);
  for (const key of keys) {
    if (key === "constructor" || key.startsWith("_")) continue;
    if (key in target && !replace.has(key)) continue;
    const value = own[key];
    target[key] = typeof value === "function" && !/^[A-Z]/.test(key) ? (value as (...a: unknown[]) => unknown).bind(window) : value;
  }
  target["window"] = window;
  target["document"] = window.document;
}
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

// ThemeProvider stamps the root and leaves it stamped on unmount; a later
// test that reads [data-theme] from its own panes must not see the last one's.
afterEach(() => {
  for (const a of ["data-theme", "data-density", "data-reduced-motion"]) document.documentElement.removeAttribute(a);
});
