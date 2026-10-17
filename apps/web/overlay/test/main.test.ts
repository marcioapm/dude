/**
 * The script lux injects, booted as a page boots it: `main.ts` imported in
 * the test's window with `document.currentScript` set to the tag. Each
 * import is a fresh evaluation (a query on the specifier), as a second tag
 * on the page would be.
 */

import { afterEach, expect, mock, test } from "bun:test";

const DUDE = "https://dude.example.test";
let n = 0;
let opener: { postMessage: ReturnType<typeof mock>; closed: boolean } | null = null;

function tag(origin: string | null) {
  const s = document.createElement("script");
  if (origin !== null) s.setAttribute("data-dude-origin", origin);
  Object.defineProperty(document, "currentScript", { configurable: true, get: () => s });
}

async function load() {
  await import(`../main.ts?boot=${++n}`);
}

const hosts = () => document.querySelectorAll("dude-preview-overlay").length;

afterEach(() => {
  // A booted overlay is told to stop by its opener, so none is left running.
  if (opener) window.dispatchEvent(new window.MessageEvent("message", { data: { type: "stop", v: 1 }, origin: DUDE, source: opener as unknown as MessageEventSource }));
  for (const h of document.querySelectorAll("dude-preview-overlay")) h.remove();
  delete (window as { __dudeOverlay?: boolean }).__dudeOverlay;
  Object.defineProperty(window, "top", { configurable: true, get: () => window });
  delete (document as { currentScript?: unknown }).currentScript;
});

function withOpener() {
  opener = { postMessage: mock(() => {}), closed: false };
  Object.defineProperty(window, "opener", { configurable: true, get: () => opener });
}

test("at the top of the page with an exact origin it boots once, however often the script runs", async () => {
  withOpener();
  tag(DUDE);
  await load();
  expect(hosts()).toBe(1);
  expect(opener!.postMessage).toHaveBeenCalledWith(expect.objectContaining({ type: "hello" }), DUDE);
  await load();
  expect(hosts()).toBe(1);
});

test("inside a frame it never boots", async () => {
  withOpener();
  Object.defineProperty(window, "top", { configurable: true, get: () => ({}) });
  tag(DUDE);
  await load();
  expect(hosts()).toBe(0);
  expect(opener!.postMessage).not.toHaveBeenCalled();
});

test("an origin not written exactly as the browser writes one, or none, boots nothing", async () => {
  withOpener();
  for (const bad of [`${DUDE}/`, `${DUDE}/feedback`, "dude.example.test", "null", "", null]) {
    tag(bad);
    await load();
    expect([bad, hosts()]).toEqual([bad, 0]);
  }
  expect(opener!.postMessage).not.toHaveBeenCalled();
});
