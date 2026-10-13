/**
 * The gallery's Peers section draws every piece in the gallery's frames,
 * dark and light in each density, with the states the design doc lists.
 */

import { afterEach, expect, test } from "bun:test";
import { StrictMode, act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { PeersSection } from "../src/gallery/sections/Peers.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

const FRAMES = ["comfortable dark", "comfortable light", "compact dark", "compact light"];

test("every Peers block renders in dark and light, comfortable and compact", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <StrictMode><PaneDensityContext.Provider value="both"><PeersSection mode="both" /></PaneDensityContext.Provider></StrictMode>,
  ));
  const blocks = [...host.querySelectorAll("section#peers [id^='pe-']")];
  expect(blocks.map((b) => b.id)).toEqual(["pe-chip", "pe-message", "pe-wait", "pe-note", "pe-notice", "pe-thread", "pe-digest", "pe-mark", "pe-list"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll("[data-theme][data-density]")];
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
  }
  const first = (id: string) => host!.querySelector(`#${id} [data-theme="dark"][data-density="comfortable"]`)!;
  // The message states the doc lists: read, carrying on, queued, no Run going, reply, refused, compact waiting.
  const message = first("pe-message");
  expect(message.querySelectorAll('[data-kind="peer"]').length).toBe(7);
  expect(message.textContent).toContain("has no Run going");
  expect(message.textContent).toContain("Not sent:");
  // Waiting by wait, by a turn that ended, answered, and the peer ending.
  expect([...first("pe-wait").querySelectorAll('[data-testid="peer-wait-line"]')].map((l) => l.getAttribute("data-state")))
    .toEqual(["waiting", "waiting", "reply", "peer_ended"]);
  expect([...first("pe-notice").querySelectorAll('[data-testid="epic-note-notice"]')].map((n) => n.getAttribute("data-change")))
    .toEqual(["added", "replaced", "retired", "replaced"]);
  expect([...first("pe-thread").querySelectorAll('[data-testid="peer-thread"]')].map((t) => t.getAttribute("data-state")))
    .toEqual(["waiting", "open", "answered", "answered"]);
  expect([...first("pe-list").querySelectorAll("ul[data-variant]")].map((l) => l.getAttribute("data-variant"))).toEqual(["tool", "page", "rail"]);
});
