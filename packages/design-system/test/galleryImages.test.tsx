/**
 * The gallery's Image library section draws "Can run containers" in each
 * frame: its badges (a Run header's too, with its tooltip), the box with its
 * hint and warning, the check stage in its states, a waiting Run, and the
 * history's flips.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { PaneDensityContext } from "../src/gallery/Frame.tsx";
import { ImagesGallerySection } from "../src/gallery/sections/Images.tsx";

/** cond's value once it is truthy, polling for up to 2 s. */
async function until<T>(cond: () => T | null | undefined): Promise<T> {
  for (const end = Date.now() + 2000; Date.now() < end; ) {
    const v = cond();
    if (v) return v;
    await act(async () => { await new Promise((r) => setTimeout(r, 10)); });
  }
  throw new Error("timed out");
}

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
      ["Can run containers", "Can run containers", "can run containers", "Can run containers"]);
    // A Run's header: the badge after the image chip, focusable, its sentence in a tooltip.
    const facts = pane.querySelector("[data-testid=run-header-facts]")!;
    const badge = facts.lastElementChild as HTMLElement;
    expect(badge.getAttribute("data-testid")).toBe("can-run-containers");
    expect(badge.getAttribute("aria-label")).toBe("Can run containers");
    expect(badge.hasAttribute("title")).toBe(false);
    await act(async () => badge.focus());
    expect(document.activeElement).toBe(badge);
    const tip = await until(() => document.querySelector("[role=tooltip]"));
    expect(tip.textContent).toBe("This Run can start containers inside it. Set when the session started; resuming keeps it.");
    await act(async () => badge.blur());
    await until(() => document.querySelector("[role=tooltip]") ? null : true);
    expect(pane.querySelectorAll("[role=checkbox]")).toHaveLength(3);
    expect(text).toContain("unless the base has them");
    expect(text).toContain("Existing ones keep theirs until they start a fresh run.");
    expect(text).toContain("Check containers");
    expect(text).toContain("Waiting for a host that can run containers.");
    expect(pane.querySelector("[data-testid=flag-diff]")!.textContent).toBe("Can run containers off → on");
    expect([...pane.querySelectorAll("[data-testid=version-flag]")].map((f) => f.textContent!.trim())).toEqual(
      ["Can run containers turned off", "Can run containers turned on"]);
  }
  // Each pane's box is described by its own pane's hint and warning: no id repeats across panes.
  const ids = [...host.querySelectorAll("#i-containers [id]")].map((e) => e.id);
  expect(new Set(ids).size).toBe(ids.length);
  for (const pane of panes) {
    const described = [...pane.querySelectorAll("[role=checkbox][aria-describedby]")]
      .flatMap((b) => b.getAttribute("aria-describedby")!.split(" "));
    expect(described.length).toBeGreaterThanOrEqual(5);
    for (const id of described) expect(pane.contains(document.getElementById(id))).toBe(true);
  }
});
