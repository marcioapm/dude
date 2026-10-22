/**
 * The full sidebar's room: who else is online in the brand row, no
 * "Waiting on others" row, and Sessions showing your latest few under a
 * heading that folds and starts a new one.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Sidebar, SidebarSessions, type SidebarSession } from "../src/components/Sidebar.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { NavProject } from "../src/util/navModel.ts";
import { byRole } from "./queries.ts";

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

const ME = { id: "me", name: "Márcio Martins" };
const PEOPLE = [ME, { id: "a", name: "Ana Ribeiro" }, { id: "t", name: "Tomás Ortega" }, { id: "l", name: "Lin Zhao" }, { id: "k", name: "Kenji Nakamura" }];
// One task waiting on someone else: before, the full sidebar listed it as "Waiting on others".
const PROJECTS: NavProject[] = [{ id: "p", name: "web", tasks: [{ id: "t1", key: "WC-1", title: "Theirs", status: "awaiting_input", people: [PEOPLE[1]!] }] }];

describe("the brand row's online faces", () => {
  test("are the others, at most three and +N, named on the group", async () => {
    const el = await mount(<Sidebar projects={PROJECTS} title="dude" you="me" online={PEOPLE} onWaitingSelect={() => undefined} />);
    const header = el.querySelector("header")!;
    const online = header.querySelector("[data-testid=online]")!;
    expect(online).not.toBeNull();
    expect(online.getAttribute("aria-label")).toBe("Online: Ana Ribeiro, Tomás Ortega, Lin Zhao, Kenji Nakamura");
    expect(online.getAttribute("role")).toBe("group");
    expect((online as HTMLElement).tabIndex).toBe(0);
    await act(async () => (online as HTMLElement).focus());
    expect(document.activeElement).toBe(online);
    expect(online.textContent).toContain("+1");
    expect(online.textContent).not.toContain("MM");
  });

  test("are not drawn when only you are online", async () => {
    const el = await mount(<Sidebar projects={PROJECTS} title="dude" you="me" online={[ME]} />);
    expect(el.querySelectorAll("[data-testid=online]").length).toBe(0);
  });
});

test("the full sidebar has no Waiting on others row; Waiting on you stays", async () => {
  const el = await mount(<Sidebar projects={PROJECTS} title="dude" you="me" onWaitingSelect={() => undefined} />);
  expect(el.querySelectorAll("[data-testid=waiting-on-others]").length).toBe(0);
  expect(el.querySelectorAll("[data-testid=waiting-on-you]").length).toBe(1);
});

describe("SidebarSessions", () => {
  const at = (h: number) => new Date(Date.UTC(2026, 9, 22, h)).toISOString();
  const FIVE: SidebarSession[] = [
    { id: "s1", title: "One", lastActivityAt: at(1) },
    { id: "s5", title: "Five", lastActivityAt: at(5) },
    { id: "s3", title: "Three", lastActivityAt: at(3) },
    { id: "s2", title: "Two", lastActivityAt: at(2) },
    { id: "s4", title: "Four", lastActivityAt: at(4) },
  ];
  const shown = (el: HTMLElement) => [...el.querySelectorAll("[data-session]")].map((r) => r.getAttribute("data-session"));

  test("five sessions show the three with the latest activity, newest first", async () => {
    const el = await mount(<SidebarSessions sessions={FIVE} onSelect={() => undefined} onNew={() => undefined} />);
    expect(shown(el)).toEqual(["s5", "s4", "s3"]);
  });

  test("the heading's + starts a new session; there is no New session row", async () => {
    let started = 0;
    const el = await mount(<SidebarSessions sessions={FIVE} onSelect={() => undefined} onNew={() => started++} />);
    const plus = byRole(el, "button", "New session");
    expect(plus.getAttribute("data-testid")).toBe("new-session");
    expect(plus.textContent).not.toContain("New session");
    await act(async () => plus.click());
    expect(started).toBe(1);
  });

  test("the chevron folds and unfolds the list, telling its owner", async () => {
    const told: boolean[] = [];
    function Owned() {
      const [folded, setFolded] = useState(true);
      return <SidebarSessions sessions={FIVE} onSelect={() => undefined} onNew={() => undefined} folded={folded}
        onFoldedChange={(f) => { told.push(f); setFolded(f); }} />;
    }
    const el = await mount(<Owned />);
    expect(shown(el)).toEqual([]);
    await act(async () => byRole(el, "button", "Unfold sessions").click());
    expect(told).toEqual([false]);
    expect(shown(el)).toEqual(["s5", "s4", "s3"]);
    const fold = byRole(el, "button", "Fold sessions");
    expect(fold.getAttribute("aria-expanded")).toBe("true");
    await act(async () => fold.click());
    expect(told).toEqual([false, true]);
    expect(shown(el)).toEqual([]);
  });
});
