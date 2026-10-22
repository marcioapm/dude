/**
 * The tree's "⋯" menu and guide lines, read from the computed style with
 * the shipped stylesheets loaded (class names kept as written).
 *
 * The menu reserves no width on any row: until the row is hovered or
 * focused, or the menu is open, its slot is not laid out, so the row's
 * title has the width it would have without a menu. Then it takes the
 * place of what trails the row (a task's people, an epic's count).
 *
 * Guide lines: one per ancestor level, drawn by the row's `::before`,
 * as wide as `--depth` levels of `--indent`.
 */

import { afterEach, beforeAll, describe, expect, mock, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { NavProject } from "../src/util/navModel.ts";

const asWritten = () => ({ default: new Proxy({}, { get: (_, key) => (typeof key === "string" ? key : undefined) }) });
mock.module("../src/components/NavTree.module.css", asWritten);

const { NavTree } = await import("../src/components/NavTree.tsx");

const SHEETS = ["tokens/tokens.css", "components/NavTree.module.css"];

beforeAll(async () => {
  for (const path of SHEETS) {
    const el = document.createElement("style");
    el.textContent = await Bun.file(new URL(`../src/${path}`, import.meta.url)).text();
    document.head.append(el);
  }
});

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

const ANA = { id: "ana", name: "Ana Ribeiro" };
const PROJECTS: NavProject[] = [{
  id: "p", name: "web", people: [ANA],
  epics: [{ id: "e", title: "Checkout", tasks: [{ id: "t1", key: "WC-1", title: "Cart summary", status: "running", people: [ANA] }] }],
  tasks: [{ id: "t2", key: "WC-2", title: "Free task", status: "queued", people: [ANA] }],
}];
const OPEN = new Map([["project:p", true], ["epic:e", true]]);

async function mount(withMenu: boolean): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(
    <NavTree projects={PROJECTS} expanded={OPEN} onExpandedChange={() => undefined}
      menuItems={withMenu ? () => [{ id: "open", label: "Open" }] : undefined} />,
  ));
  return host;
}

const row = (h: HTMLElement, key: string) => h.querySelector<HTMLElement>(`[data-nav-key="${key}"]`)!;
const shown = (el: Element | null) => el !== null && getComputedStyle(el).display !== "none";

describe("the row menu", () => {
  test("reserves no width on a project, epic or task row until asked for", async () => {
    const h = await mount(true);
    for (const key of ["project:p", "epic:e", "task:t1", "task:t2"]) {
      const slot = row(h, key).querySelector(".menuSlot");
      expect(slot, key).not.toBeNull();
      expect(shown(slot), key).toBe(false);
    }
  });

  test("a task's title has the same room with a menu as without one", async () => {
    const withMenu = await mount(true);
    const laidOut = (h: HTMLElement) => [...row(h, "task:t1").children].filter(shown).map((c) => c.className);
    const a = laidOut(withMenu);
    await act(async () => root?.unmount());
    host?.remove();
    const without = await mount(false);
    expect(a).toEqual(laidOut(without));
  });


  test("an open menu shows in place of the people", async () => {
    const h = await mount(true);
    const r = row(h, "task:t1");
    await act(async () => r.dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, cancelable: true })));
    expect(r.querySelector(".menuSlot")!.getAttribute("data-open")).toBe("true");
    expect(shown(r.querySelector(".menuSlot"))).toBe(true);
    expect(shown(r.querySelector(".wiPeople"))).toBe(false);
  });
});

