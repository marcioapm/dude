/**
 * The gallery's Brainstorm section draws every state in the gallery's
 * normal frames, dark and light in each density, and shows the
 * brainstorm's face beside the conductor's and beside needs-you.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { BrainstormSection } from "../src/gallery/sections/Brainstorm.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <PaneDensityContext.Provider value="both"><BrainstormSection mode="both" /></PaneDensityContext.Provider>,
  ));
  return host;
}

const FRAMES = ["comfortable dark", "comfortable light", "compact dark", "compact light"];

test("every Brainstorm block renders in dark and light, comfortable and compact", async () => {
  const el = await mount();
  const blocks = [...el.querySelectorAll("section#brainstorm [id^='bs-']")];
  expect(blocks.map((b) => b.id)).toEqual(["bs-avatar", "bs-proposal", "bs-row", "bs-sidebar", "bs-rail"]);
  for (const block of blocks) {
    const panes = [...block.querySelectorAll("[data-theme][data-density]")];
    expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`), block.id).toEqual(FRAMES);
    for (const pane of panes) expect(pane.textContent!.length, `${block.id} ${pane.getAttribute("data-theme")}`).toBeGreaterThan(20);
  }
});

// `awaiting_input` is the vocabulary's needs-you state ("Needs you", solid).
test("each frame shows the brainstorm's face beside the conductor's and a needs-you mark", async () => {
  const el = await mount();
  const panes = [...el.querySelectorAll("#bs-avatar [data-theme]")];
  expect(panes).toHaveLength(4);
  for (const pane of panes) {
    const first = pane.querySelector("[data-role='brainstorm']")!.closest("div")!;
    expect(first.querySelector("[data-role='conductor'][aria-label='Conductor']")).toBeTruthy();
    expect(first.querySelector("[data-role='brainstorm'][aria-label='Brainstorm']")).toBeTruthy();
    expect(first.querySelector("[data-status='awaiting_input']")).toBeTruthy();
  }
});
