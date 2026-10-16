/**
 * Who drives a task: the header's chip in each mode, the owner's switch
 * (a radio menu, the current one ticked, a pick calling back with the
 * other), the member's read-only chip and its reason, an ended task's
 * muted words, and the glyph a board card and a sidebar row carry while
 * in progress only. Mounted in happy-dom.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Board } from "../src/components/Board.tsx";
import { DriverEnded, DriverSwitch, driverMode, type DriverMode } from "../src/components/DriverMark.tsx";
import { NavTree } from "../src/components/NavTree.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { NavProject, NavTask } from "../src/util/navModel.ts";
import { accessibleName, allByRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
  return host;
}

const chip = (el: HTMLElement) => el.querySelector<HTMLElement>("[data-testid=driver]")!;
const icons = (el: Element) => [...el.querySelectorAll("svg")].length;

async function openMenu(el: HTMLElement) {
  const trigger = chip(el);
  await act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  return document.querySelector("[role=menu]")!;
}

describe("driverMode", () => {
  test("two stored values make three shown: a live conductor under Deliver is in Chat, never the driver", () => {
    expect(driverMode("policy", false)).toBe("deliver");
    expect(driverMode("policy", true)).toBe("deliver-chat");
    expect(driverMode("conductor", false)).toBe("conductor");
    expect(driverMode("conductor", true)).toBe("conductor");
  });
});

describe("the header's chip", () => {
  const cases: Array<[DriverMode, string, number]> = [
    ["deliver", "Deliver · runs to the pull request on its own", 1],
    ["conductor", "Conductor · each finished step comes back to it", 1],
    ["deliver-chat", "Deliver · conductor in Chat · the conductor answers; Deliver decides", 2],
  ];
  for (const [mode, words, glyphs] of cases) {
    test(`${mode}: its words, its glyphs, and for the owner a switch`, async () => {
      const el = await mount(<DriverSwitch mode={mode} handedBack={false} canSwitch onSwitch={() => undefined} />);
      const c = chip(el);
      expect(c.tagName).toBe("BUTTON");
      expect(c.getAttribute("data-driver")).toBe(mode);
      // Words, then the chevron: glyphs plus one.
      expect(c.textContent).toBe(words);
      expect(icons(c)).toBe(glyphs + 1);
      expect(accessibleName(c)).toEndWith("Change who decides");
    });
  }

  test("what it waits on replaces what it means, in full in its name", async () => {
    const el = await mount(<DriverSwitch mode="conductor" waiting={{ point: "before_pull_request", on: "you" }} handedBack={false} canSwitch onSwitch={() => undefined} />);
    expect(chip(el).textContent).toBe("Conductor · waiting on you: open the PR?");
    expect(accessibleName(chip(el))).toBe("Conductor: waiting on you: whether to open the pull request. Change who decides");
  });

  test("a member reads the same chip, with no chevron and no menu, and is told only the owner can change it", async () => {
    const el = await mount(<DriverSwitch mode="deliver" handedBack={false} canSwitch={false} ownerName="Ana" onSwitch={() => undefined} />);
    const c = chip(el);
    expect(c.tagName).toBe("SPAN");
    expect(c.getAttribute("data-switchable")).toBe("false");
    expect(icons(c)).toBe(1);
    expect(el.querySelectorAll("button").length).toBe(0);
    expect(accessibleName(c)).toBe("Deliver: runs to the pull request on its own. Only the owner can change who decides. Ana owns this task.");
    // Focus opens the tooltip, which says the same.
    await act(async () => c.focus());
    await act(async () => new Promise((r) => setTimeout(r, 50)));
    expect(document.body.textContent).toContain("Only the owner can change who decides. Ana owns this task.");
  });

  test("an ended task says who decided last, muted, with nothing to switch", async () => {
    const el = await mount(<DriverEnded decider="conductor" />);
    expect(chip(el).textContent).toBe("Decided by the conductor");
    expect(chip(el).hasAttribute("data-ended")).toBe(true);
    expect(el.querySelectorAll("button").length).toBe(0);
    await unmountAndRemount(<DriverEnded decider="policy" />);
    expect(chip(host!).textContent).toBe("Decided by Deliver");
  });
});

async function unmountAndRemount(el: React.ReactElement) {
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
}

describe("the owner's switch", () => {
  const radios = () => allByRole(document, "menuitemradio");
  // An item's label: the first line of its two (label, then what it does).
  const label = (r: HTMLElement) => [...r.querySelectorAll("span")].find((s) => !s.hasAttribute("aria-hidden") && s.children.length === 0)?.textContent;
  const labels = () => radios().map(label);
  const checked = () => radios().filter((r) => r.getAttribute("aria-checked") === "true").map(label);

  test("under Deliver: Deliver ticked, the other offered as Give decisions to the conductor, and picking it calls back once", async () => {
    const to: string[] = [];
    const el = await mount(<DriverSwitch mode="deliver" running="fixer" handedBack={false} canSwitch onSwitch={(d) => to.push(d)} />);
    const menu = await openMenu(el);
    expect(labels()).toEqual(["Deliver decides", "Give decisions to the conductor"]);
    expect(checked()).toEqual(["Deliver decides"]);
    expect(menu.textContent).toContain("It takes over when the fixer finishes; the fixer keeps running. A conductor starts in Chat to take it.");
    expect(menu.textContent).toContain("Writing in Chat also gives the conductor the decisions.");
    await act(async () => radios()[1]!.click());
    expect(to).toEqual(["conductor"]);
  });

  test("picking the one already ticked changes nothing", async () => {
    const to: string[] = [];
    const el = await mount(<DriverSwitch mode="deliver" handedBack={false} canSwitch onSwitch={(d) => to.push(d)} />);
    await openMenu(el);
    await act(async () => radios()[0]!.click());
    expect(to).toEqual([]);
  });

  test("under the conductor: it is ticked, and Let Deliver finish it calls back with policy", async () => {
    const to: string[] = [];
    const el = await mount(<DriverSwitch mode="conductor" handedBack={false} canSwitch onSwitch={(d) => to.push(d)} />);
    await openMenu(el);
    expect(checked()).toEqual(["The conductor decides"]);
    expect(labels()).toEqual(["Let Deliver finish it", "The conductor decides"]);
    await act(async () => radios()[0]!.click());
    expect(to).toEqual(["policy"]);
  });

  test("a conductor live in Chat is not the driver: Deliver stays ticked, and the conductor already there is the one to take it", async () => {
    const el = await mount(<DriverSwitch mode="deliver-chat" handedBack canSwitch onSwitch={() => undefined} />);
    const menu = await openMenu(el);
    expect(checked()).toEqual(["Deliver decides"]);
    expect(menu.textContent).toContain("The conductor already in Chat takes it.");
    expect(menu.textContent).toContain("You let Deliver finish it: writing in Chat no longer takes the decisions back.");
  });

  test("at the pull request question, letting Deliver finish says it opens the pull request and asks first", async () => {
    const el = await mount(<DriverSwitch mode="conductor" waiting={{ point: "before_pull_request", on: "you" }} handedBack={false} canSwitch onSwitch={() => undefined} />);
    const menu = await openMenu(el);
    expect(menu.textContent).toContain("Deliver opens the pull request now, and finishes on its own. You are asked to confirm.");
  });
});

describe("cards and rows", () => {
  const task = (id: string, status: NavTask["status"], driver?: NavTask["driver"]): NavTask =>
    ({ id, key: id.toUpperCase(), title: `Task ${id}`, status, ...(driver ? { driver } : {}) });
  const project: NavProject = { id: "p", name: "Web", tasks: [
    task("a", "running", "deliver"), task("b", "awaiting_input", "conductor"), task("c", "review", "deliver-chat"),
    task("d", "queued", "deliver"), task("e", "done", "conductor"), task("f", "running"),
  ] };
  const glyphOf = (el: Element) => el.querySelector("[data-testid=driver-glyph]")?.getAttribute("data-driver") ?? null;

  test("a board card in progress carries the glyph and says it in its name; queued, ended, or unknown carry none", async () => {
    const el = await mount(<Board project={project} />);
    const cards = [...el.querySelectorAll<HTMLElement>("[data-board-key]")];
    const by = Object.fromEntries(cards.map((c) => [c.getAttribute("data-board-key"), c]));
    expect(glyphOf(by["task:a"]!)).toBe("deliver");
    expect(glyphOf(by["task:b"]!)).toBe("conductor");
    expect(glyphOf(by["task:c"]!)).toBe("deliver-chat");
    expect(by["task:a"]!.getAttribute("aria-label")).toBe("A Task a; Deliver, runs to the pull request on its own");
    expect(by["task:c"]!.getAttribute("aria-label")).toBe("C Task c; Deliver · conductor in Chat, the conductor answers; Deliver decides");
    for (const k of ["task:d", "task:e", "task:f"]) expect(by[k]!.querySelectorAll("[data-testid=driver-glyph]").length).toBe(0);
    // A mark, never a switch.
    expect(el.querySelectorAll("[data-testid=driver-glyph] button").length).toBe(0);
  });

  test("a sidebar row in progress carries the glyph and its words; queued and ended ones none", async () => {
    const el = await mount(<NavTree projects={[project]} expanded={new Map([["project:p", true]])} />);
    const rows = [...el.querySelectorAll<HTMLElement>("[data-kind=task]")];
    const by = Object.fromEntries(rows.map((r) => [r.getAttribute("data-nav-key"), r]));
    expect(glyphOf(by["task:b"]!)).toBe("conductor");
    expect(accessibleName(by["task:b"]!)).toBe("B Task b; Conductor, each finished step comes back to it");
    for (const k of ["task:d", "task:e", "task:f"]) expect(by[k]!.querySelectorAll("[data-testid=driver-glyph]").length).toBe(0);
  });
});
