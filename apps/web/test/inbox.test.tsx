import { afterEach, expect, test } from "bun:test";
import { useState } from "react";
import { type NavProject, type NavRef } from "@dude/design-system";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { SessionsList } from "@dude/domain";
import { InboxScreen } from "../src/screens/InboxScreen.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PEOPLE, YOU } from "../src/fixtures/data.ts";
import { click, mount, until } from "./dom.ts";

const me = PEOPLE.find((p) => p.id === YOU)!;
const other = PEOPLE.find((p) => p.id !== YOU)!;
const projects: NavProject[] = [{ id: "p1", name: "One", tasks: [
  { id: "decide", key: "ONE-1", title: "Decision", status: "awaiting_confirmation", people: [me], statusSince: "2026-10-01T00:00:00Z" },
  { id: "ask", key: "ONE-2", title: "Question", status: "running", people: [me], statusSince: "2026-10-02T00:00:00Z",
    runs: [{ id: "run", attempt: 1, status: "running", sessions: [{ id: "agent", role: "implementer", status: "awaiting_input", activity: "Which wallet?" }] }] },
  { id: "theirs", key: "ONE-3", title: "Their decision", status: "awaiting_confirmation", people: [other], statusSince: "2026-10-03T00:00:00Z" },
] }, { id: "p2", name: "Two", tasks: [
  { id: "second", title: "Second decision", status: "awaiting_confirmation", people: [me] },
] }];
const sessions: SessionsList = { sessions: [], questions: [{ id: "q", sessionId: "ss", title: "Private", prompt: "Why?", options: [], askedAt: "2026-10-04T00:00:00Z" }],
  invitations: [{ id: "inv", title: "Plan", role: "chat", becomesOwner: false, invitedAt: "2026-10-05T00:00:00Z", invitedBy: other, people: [other], projects: [], messages: 2 }] };
let cleanup: (() => Promise<void>)[] = [];
afterEach(async () => { for (const unmount of cleanup) await unmount(); cleanup = []; });

async function page(initial: "you" | "others" = "you") {
  const client = new FixtureClient("a");
  const opened: NavRef[] = [];
  const openedSessions: string[] = [];
  const accepted: string[] = [];
  const declined: string[] = [];
  let changed = () => {};
  client.reassignTask = async (id) => { changed(); return { id, ownerId: YOU }; };
  client.acceptSession = async (id) => { accepted.push(id); return { ok: true }; };
  client.declineSession = async (id) => { declined.push(id); return { ok: true }; };
  function Driven() {
    const [tree, setTree] = useState(projects);
    changed = () => setTree(tree.map((p) => ({ ...p, tasks: p.tasks?.map((t) => t.id === "theirs" ? { ...t, people: [me] } : t) })));
    return <InboxScreen client={client} projects={tree} sessions={sessions} tab={initial} onSelect={(ref) => opened.push(ref)} onOpenSession={(id) => openedSessions.push(id)} onChanged={() => {}} />;
  }
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider><PeopleProvider client={client}><Driven /></PeopleProvider></ToastProvider></TooltipProvider>);
  cleanup.push(unmount);
  await until(() => container.querySelector("[data-testid=inbox]"), "Waiting");
  await until(() => container.querySelector("[data-testid=inbox] [role=tab]")?.textContent === "On you5" ? true : null, "people read");
  return { container, client, opened, openedSessions, accepted, declined };
}
const list = (c: HTMLElement) => c.querySelector<HTMLElement>("[data-testid=inbox] ul")!;
const button = (c: HTMLElement, text: string) => [...c.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent === text)!;
const facet = (c: HTMLElement, group: string, label: string) => [...c.querySelectorAll<HTMLElement>(`[role=group][aria-label='${group}'] [role=checkbox]`)].find((b) => b.getAttribute("aria-labelledby")?.split(" ").some((id) => document.getElementById(id)?.textContent === label)) ?? [...c.querySelectorAll<HTMLElement>(`[role=group][aria-label='${group}'] [role=checkbox]`)].find((b) => b.closest("label")?.textContent === label)!;

test("kind and project compose, counts stay based on the tab, empties retain filters and task/session navigation", async () => {
  const { container: c, opened } = await page();
  expect(list(c).textContent).not.toContain("Their decision");
  expect([...list(c).children].map((r) => r.textContent)).toEqual(expect.arrayContaining([expect.stringContaining("Decision"), expect.stringContaining("Which wallet?")]));
  expect([...list(c).children].slice(2).map((r) => r.textContent)).toEqual([
    expect.stringContaining("ONE-1"), expect.stringContaining("ONE-2"), expect.stringContaining("Second decision"),
  ]);
  expect(c.querySelector("aside")?.textContent).toContain("ONE-1");
  await click(button(list(c), "Decide"));
  expect(opened).toEqual([{ kind: "task", id: "decide" }]);
  await click(button([...list(c).children].find((r) => r.textContent?.includes("Which wallet?")) as HTMLElement, "Answer"));
  expect(opened.at(-1)).toEqual({ kind: "session", id: "agent" });
  await click(facet(c, "Kind", "Questions"));
  expect(list(c).children.length).toBe(2);
  await click(facet(c, "Projects", "One"));
  expect(list(c).children.length).toBe(1);
  expect(list(c).textContent).toContain("Which wallet?");
  const kinds = c.querySelector("[role=group][aria-label=Kind]")!;
  expect(kinds.textContent).toBe("Questions2Decisions2Invitations1");
  await click(facet(c, "Projects", "Two"));
  expect(list(c).children.length).toBe(1);
  await click(facet(c, "Projects", "One"));
  expect(c.textContent).toContain("Nothing matches these filters");
  expect(c.querySelectorAll("[role=tab]").length).toBe(2);
  await click(button(c.querySelector("aside")!, "All · 5"));
  expect(list(c).children.length).toBe(2);
});

test("others starts selected, person/project compose, Take over moves the row and live tab counts", async () => {
  const { container: c } = await page("others");
  expect(c.querySelector("[role=tab][aria-selected=true]")?.textContent).toBe("On others1");
  await click(facet(c, "Waiting on", other.name));
  await click(facet(c, "Projects", "One"));
  expect(list(c).children.length).toBe(1);
  await click(button(c, "Take over"));
  await until(() => c.textContent?.includes("Nothing is waiting on others") ? true : null, "moved off others");
  expect(c.querySelector("[role=tab]")?.textContent).toBe("On you6");
  expect(c.querySelectorAll("[role=tab]")[1]?.textContent).toBe("On others0");
});

test("invitation Open/Decline and a brainstorm answer keep their existing actions", async () => {
  const { container: c, accepted, declined, openedSessions } = await page();
  await click(c.querySelector("[data-testid=invitation-decline]")!);
  expect(declined).toEqual(["inv"]);
  await click(c.querySelector("[data-testid=invitation-open]")!);
  expect(accepted).toEqual(["inv"]);
  expect(openedSessions).toEqual(["inv"]);
  await click(button(c.querySelector<HTMLElement>("[data-testid=session-question]")!, "Answer"));
  expect(openedSessions).toEqual(["inv", "ss"]);
});
