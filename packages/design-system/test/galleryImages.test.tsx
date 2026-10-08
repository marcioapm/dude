/**
 * The gallery's Image library section draws "Can run containers" in each
 * frame: its badges, the box with its hint and warning, the check stage in
 * its states, a waiting Run, and the history's flips.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { ImagesGallerySection } from "../src/gallery/sections/Images.tsx";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

test("Can run containers renders in dark and light, comfortable and compact", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <PaneDensityContext.Provider value="both"><ImagesGallerySection mode="both" /></PaneDensityContext.Provider>,
  ));
  const panes = [...host.querySelectorAll("#i-containers [data-theme][data-density]")];
  expect(panes.map((p) => `${p.getAttribute("data-density")} ${p.getAttribute("data-theme")}`))
    .toEqual(["comfortable dark", "comfortable light", "compact dark", "compact light"]);
  for (const pane of panes) {
    const text = pane.textContent!;
    expect([...pane.querySelectorAll("[data-testid=can-run-containers]")].map((b) => b.textContent)).toEqual(
      ["Can run containers", "Can run containers", "can run containers"]);
    expect(pane.querySelectorAll("[role=checkbox]")).toHaveLength(3);
    expect(text).toContain("unless the base has them");
    expect(text).toContain("Existing ones keep theirs until they start a fresh run.");
    expect(text).toContain("Check containers");
    expect(text).toContain("Waiting for a host that can run containers.");
    expect(pane.querySelector("[data-testid=flag-diff]")!.textContent).toBe("Can run containers off → on");
    expect([...pane.querySelectorAll("[data-testid=version-flag]")].map((f) => f.textContent!.trim())).toEqual(
      ["Can run containers turned off", "Can run containers turned on"]);
  }
});
