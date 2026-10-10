/**
 * The welcome's parts and the sidebar's rail, mounted in happy-dom: what is
 * a control and what it is called, what a press does, and what is drawn in
 * each state. The rail is drawn only on a wide viewport (happy-dom's window
 * is 1024px wide unless a test narrows it).
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ComposerLinks, RecentSessions, StarterPills, Welcome, type LinkableProject, type Starter } from "../src/components/Welcome.tsx";
import { Sidebar, SidebarRailItem, projectCountWords } from "../src/components/Sidebar.tsx";
import { ChatComposer } from "../src/components/ChatComposer.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { NavProject, NavRef } from "../src/util/navModel.ts";
import { projectCounts } from "../src/util/navModel.ts";
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

const STARTERS: Starter[] = [
  { id: "epic", icon: "layers", title: "Plan an epic", detail: "Turn a goal into an epic.", prompt: "I want to plan an epic for " },
  { id: "code", icon: "search", title: "Ask the code", detail: "How something works.", prompt: "How does " },
];
const WEB: LinkableProject = { id: "p_web", name: "web" };
const API: LinkableProject = { id: "p_api", name: "api" };

describe("Welcome", () => {
  test("its greeting is the page's heading, with the line, the composer, the starters and the footer in order", async () => {
    const el = await mount(<Welcome greeting="Afternoon, Márcio" line="What are we working out today?"
      composer={<textarea aria-label="composer" />} starters={<p>starters</p>} footer={<p>footer</p>} />);
    expect(byRole(el, "heading", "Afternoon, Márcio").tagName).toBe("H1");
    expect(el.textContent).toBe("Afternoon, MárcioWhat are we working out today?startersfooter");
  });
});

describe("StarterPills", () => {
  test("each is a button by its title; picking one hands back that starter, and sends nothing itself", async () => {
    const picked: Starter[] = [];
    const el = await mount(<StarterPills starters={STARTERS} onPick={(s) => picked.push(s)} />);
    expect(byRole(el, "group", "Ways to start")).toBeTruthy();
    await act(async () => byRole(el, "button", "Ask the code").click());
    expect(picked.map((s) => s.prompt)).toEqual(["How does "]);
  });
});

describe("ComposerLinks", () => {
  function Linking({ start = [] as LinkableProject[] }) {
    const [linked, setLinked] = useState(start);
    return <ComposerLinks linked={linked} projects={[WEB, API]} onLink={(p) => setLinked((l) => [...l, p])}
      onUnlink={(p) => setLinked((l) => l.filter((x) => x.id !== p.id))} />;
  }

  test("none linked: it reads memory only, and offers to link a project", async () => {
    const el = await mount(<Linking />);
    expect(el.textContent).toContain("Reads memory only");
    expect(byRole(el, "button", "Link a project")).toBeTruthy();
  });

  test("a linked project is a chip with its name and a close that unlinks it; Link then offers only the rest", async () => {
    const el = await mount(<Linking start={[WEB]} />);
    expect([...el.querySelectorAll("[data-project]")].map((c) => c.textContent)).toEqual(["WEweb"]);
    expect(el.textContent).not.toContain("Reads memory only");
    const link = byRole(el, "button", "Link");
    await act(async () => void link.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const items = [...document.querySelectorAll("[role=menuitem]")];
    expect(items.map((i) => i.textContent)).toEqual(["APapi"]);
    await act(async () => (items[0] as HTMLElement).click());
    expect([...el.querySelectorAll("[data-project]")].map((c) => c.getAttribute("data-project"))).toEqual(["p_web", "p_api"]);
    // All linked: nothing left to offer.
    expect(el.querySelector("[data-testid=composer-link]") === null).toBe(true);
    await act(async () => byRole(el, "button", "Stop reading web").click());
    expect([...el.querySelectorAll("[data-project]")].map((c) => c.getAttribute("data-project"))).toEqual(["p_api"]);
  });

  test("Escape closes the menu and links nothing", async () => {
    const el = await mount(<Linking />);
    await act(async () => void byRole(el, "button", "Link a project").dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const menu = document.querySelector("[role=menu]")!;
    expect(menu).toBeTruthy();
    await act(async () => void menu.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(document.querySelector("[role=menu]") === null).toBe(true);
    expect(el.querySelectorAll("[data-project]").length).toBe(0);
  });

  test("read only without onLink and onUnlink: no close, no Link", async () => {
    const el = await mount(<ComposerLinks linked={[WEB, API]} projects={[WEB, API]} />);
    expect(el.querySelectorAll("[data-project]").length).toBe(2);
    expect(el.querySelectorAll("button").length).toBe(0);
  });

  test("in a composer's leading, a press on Link sends nothing", async () => {
    const sent: string[] = [];
    const el = await mount(<ChatComposer mode="chat" defaultValue="hello" onSubmit={(s) => { sent.push(s.text); }}
      leading={<ComposerLinks linked={[]} projects={[WEB]} onLink={() => undefined} onUnlink={() => undefined} />} />);
    await act(async () => byRole(el, "button", "Link a project").click());
    expect(sent).toEqual([]);
  });
});

describe("ChatComposer variant", () => {
  test("toAside sits right after to, and only with it", async () => {
    const el = await mount(<>
      <ChatComposer mode="chat" to={<>To <b>Brainstorm</b></>} toAside={<button type="button">aside</button>} onSubmit={() => undefined} data-testid="with" />
      <ChatComposer mode="chat" toAside={<button type="button">aside</button>} onSubmit={() => undefined} data-testid="without" />
    </>);
    expect(el.querySelector("[data-testid=with] [data-testid=composer-to]")!.nextElementSibling!.textContent).toBe("aside");
    expect(el.querySelector("[data-testid=without]")!.textContent).not.toContain("aside");
  });

  test("stage marks the composer as the stage's; the default does not", async () => {
    const el = await mount(<>
      <ChatComposer mode="chat" variant="stage" onSubmit={() => undefined} data-testid="staged" />
      <ChatComposer mode="chat" onSubmit={() => undefined} data-testid="plain" />
    </>);
    expect(el.querySelector("[data-testid=staged]")!.getAttribute("data-variant")).toBe("stage");
    expect(el.querySelector("[data-testid=plain]")!.hasAttribute("data-variant")).toBe(false);
  });
});

describe("RecentSessions", () => {
  test("each row opens its session; the shared one carries the owner's mark; All sessions opens the list", async () => {
    const opened: string[] = [];
    let all = false;
    const el = await mount(<RecentSessions onOpen={(id) => opened.push(id)} onAll={() => { all = true; }} sessions={[
      { id: "s1", title: "Usage-based billing", summary: "Filed 1 epic, 4 tasks", age: "1d" },
      { id: "s2", title: "Q4 cleanup", summary: "Nothing filed yet", age: "3d", shared: { owner: { id: "u_ana", name: "Ana Ribeiro" } } },
    ]} />);
    expect(el.querySelector("section")!.getAttribute("aria-label")).toBe("Recent sessions");
    const rows = [...el.querySelectorAll<HTMLButtonElement>("[data-session]")];
    expect(rows.map((r) => r.getAttribute("data-session"))).toEqual(["s1", "s2"]);
    expect(rows[1]!.querySelector('[aria-label="Shared, Ana Ribeiro\'s"]')).toBeTruthy();
    expect(rows[0]!.querySelector('[aria-label^="Shared"]') === null).toBe(true);
    await act(async () => rows[1]!.click());
    await act(async () => byRole(el, "button", "All sessions").click());
    expect(opened).toEqual(["s2"]);
    expect(all).toBe(true);
  });
});

const task = (id: string, status: "awaiting_input" | "running" | "failed" | "ready") => ({ id, title: id, key: id, status });
const PROJECTS: NavProject[] = [
  { id: "p_cp", name: "control-plane", tasks: [task("CP-1", "awaiting_input"), task("CP-2", "running"), task("CP-3", "running"), task("CP-4", "failed")] },
  { id: "p_doc", name: "docs", tasks: [task("DOC-1", "running")] },
] as NavProject[];

describe("Sidebar collapsed", () => {
  function Rail(props: { onHome?: () => void; onWaiting?: () => void; onNew?: () => void; onList?: () => void; initial?: boolean }) {
    const [collapsed, setCollapsed] = useState(props.initial ?? true);
    const [selected, setSelected] = useState<NavRef | null>(null);
    return (
      <Sidebar projects={PROJECTS} selected={selected} onSelect={setSelected} title="dude" railMark={<span>D</span>} onHome={props.onHome}
        collapsed={collapsed} onCollapsedChange={setCollapsed} onWaitingSelect={props.onWaiting}
        railSessions={{ onNew: props.onNew ?? (() => undefined), onOpenList: props.onList ?? (() => undefined) }}
        footer={<span>full band</span>} railFooter={<SidebarRailItem label="Organisation settings">O</SidebarRailItem>} />
    );
  }

  test("is a labelled nav of named buttons, top to bottom, with no tree in it", async () => {
    const el = await mount(<Rail onWaiting={() => undefined} />);
    const rail = el.querySelector("nav")!;
    expect(rail.getAttribute("aria-label")).toBe("Navigation");
    expect(rail.getAttribute("data-testid")).toBe("sidebar-rail");
    expect([...rail.querySelectorAll("button")].map((b) => b.getAttribute("aria-label"))).toEqual([
      "Home", "Expand sidebar", "Find work", "Waiting on you: 1", "New session", "Sessions",
      "control-plane: 1 needs you · 2 running · 1 failed", "docs: 1 running", "Organisation settings",
    ]);
    expect(el.querySelector("[role=tree]") === null).toBe(true);
    expect(el.textContent).not.toContain("full band");
  });

  test("Waiting on you is the loud count itself; a project waiting on you wears the diamond, one that does not, none", async () => {
    const el = await mount(<Rail onWaiting={() => undefined} />);
    expect(byRole(el, "button", "Waiting on you: 1").querySelector('[aria-label="1 needs you"]')).toBeTruthy();
    // The diamond is aria-hidden: what it means is in the face's name.
    const faces = [...el.querySelectorAll("[data-testid=rail-project]")];
    expect(faces.map((f) => [f.getAttribute("aria-label"), f.hasAttribute("data-needs-you")])).toEqual([
      ["control-plane: 1 needs you · 2 running · 1 failed", true], ["docs: 1 running", false]]);
  });

  test("with nothing waiting, Waiting on you is named without a count", async () => {
    const el = await mount(<Sidebar projects={[PROJECTS[1]!]} title="dude" collapsed onCollapsedChange={() => undefined} onWaitingSelect={() => undefined} />);
    expect(byRole(el, "button", "Waiting on you")).toBeTruthy();
  });

  test("a project's face opens its board, current after; home, New session, Sessions and Waiting do what the sidebar's do", async () => {
    const calls: string[] = [];
    const el = await mount(<Rail onHome={() => calls.push("home")} onNew={() => calls.push("new")} onList={() => calls.push("list")}
      onWaiting={() => calls.push("waiting")} />);
    await act(async () => byRole(el, "button", "docs: 1 running").click());
    expect(byRole(el, "button", "docs: 1 running").getAttribute("aria-current")).toBe("page");
    for (const name of ["Home", "New session", "Sessions", "Waiting on you: 1"]) await act(async () => byRole(el, "button", name).click());
    expect(calls).toEqual(["home", "new", "list", "waiting"]);
  });

  test("expand gives the full sidebar back; its chevron collapses it again; search expands with the field focused", async () => {
    const el = await mount(<Rail />);
    await act(async () => byRole(el, "button", "Expand sidebar").click());
    expect(el.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(el.textContent).toContain("full band");
    await act(async () => byRole(el, "button", "Collapse sidebar").click());
    expect(el.querySelector("[data-testid=sidebar-rail]")).not.toBeNull();
    await act(async () => byRole(el, "button", "Find work").click());
    expect(el.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(document.activeElement?.getAttribute("type")).toBe("search");
  });

  test("/ on the rail expands it with the search field focused; in a field, or with ⌘ or Ctrl, it does not", async () => {
    const el = await mount(<Rail />);
    const slash = (target: EventTarget, init: KeyboardEventInit = {}) =>
      act(async () => void target.dispatchEvent(new KeyboardEvent("keydown", { key: "/", bubbles: true, cancelable: true, ...init })));
    const field = document.createElement("input");
    document.body.append(field);
    try {
      await slash(field);
      await slash(document.body, { metaKey: true });
      await slash(document.body, { ctrlKey: true });
      expect(el.querySelector("[data-testid=sidebar-rail]") !== null).toBe(true);
    } finally {
      field.remove();
    }
    await slash(document.body, { shiftKey: true });
    expect(el.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
    expect(document.activeElement === el.querySelector("input[type=search]")).toBe(true);
  });

  test("without onCollapsedChange there is no collapse chevron, and collapsed draws the full sidebar", async () => {
    const el = await mount(<Sidebar projects={PROJECTS} title="dude" collapsed />);
    expect(el.querySelector("[data-testid=sidebar-collapse]") === null).toBe(true);
    expect(el.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
  });

  test("under 1000px collapsed is ignored: the drawer's sidebar is drawn", async () => {
    const width = window.innerWidth;
    (window as unknown as { happyDOM: { setInnerWidth(w: number): void } }).happyDOM.setInnerWidth(800);
    try {
      const el = await mount(<Rail />);
      expect(el.querySelector("[data-testid=sidebar-rail]") === null).toBe(true);
      expect(el.textContent).toContain("full band");
    } finally {
      (window as unknown as { happyDOM: { setInnerWidth(w: number): void } }).happyDOM.setInnerWidth(width);
    }
  });

  test("a project's counts in words name the loud one first and leave out what is zero", () => {
    expect(projectCountWords(projectCounts(PROJECTS[0]!))).toBe("1 needs you · 2 running · 1 failed");
    expect(projectCountWords(projectCounts({ id: "p", name: "empty", tasks: [] }))).toBe("");
  });

  test("the viewport is followed with one listener while mounted, removed on unmount", async () => {
    const was = window.matchMedia;
    let added = 0;
    let removed = 0;
    window.matchMedia = ((query: string) => ({
      matches: false, media: query, onchange: null,
      addEventListener: () => void added++, removeEventListener: () => void removed++,
      addListener: () => undefined, removeListener: () => undefined, dispatchEvent: () => false,
    })) as typeof window.matchMedia;
    try {
      const el = await mount(<Rail />);
      for (let i = 0; i < 3; i++) await act(async () => byRole(el, "button", i % 2 ? "Collapse sidebar" : "Expand sidebar").click());
      expect([added, removed]).toEqual([1, 0]);
      await act(async () => root!.unmount());
      root = null;
      expect([added, removed]).toEqual([1, 1]);
    } finally {
      window.matchMedia = was;
    }
  });
});
