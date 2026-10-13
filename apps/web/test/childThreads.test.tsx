/**
 * A tool call's child sessions as the transcript renders them: one
 * subagent thread each under the call's card, its own calls and messages
 * inside, folded once done, and a permission it waits on said in its
 * header, on its stuck call and in a notice.
 */

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, mock, test } from "bun:test";
import { ChatThread } from "@dude/design-system/components";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { RunScreen, renderTurn } from "../src/screens/RunScreen.tsx";
import { FixtureClient, emit } from "../src/fixtures/client.ts";
import { RUN_ID } from "../src/fixtures/data.ts";
import { until } from "./dom.ts";
import { apply, emptyProjection, project, snapshot } from "../src/api/conversation.ts";
import * as viewModule from "../src/childThreadView.tsx";
import * as bodyModule from "../src/childBody.tsx";

// The real exports, taken before any spy replaces them.
const realView = { ...viewModule };
const realBody = { ...bodyModule };
const RealChildThreadView = viewModule.ChildThreadView;
const RealChildBody = bodyModule.ChildBody;

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown> = {}, minutesAgo = 0): PersistedEvent {
  cursor += 1;
  return {
    eventId: `evt_${cursor}`, cursor, eventType, organizationId: "org_test", projectId: null, taskId: null, runId: "run_test",
    sessionId: null, workflowRunId: null, actor: { type: "agent", id: "run_test" }, source: "runner", correlationId: null,
    causationId: null, payload, occurredAt: new Date(Date.now() - minutesAgo * 60_000).toISOString(),
  } as unknown as PersistedEvent;
}

const people = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false } as unknown as Parameters<typeof renderTurn>[4];
const kid = { childId: "ses_kid", callId: "c1" };

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function render(events: PersistedEvent[]): Promise<HTMLElement> {
  const { turns } = project(events);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<>{turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</>));
  return host;
}

// A card's threads are the ones drawn after it and before the next card at its level.
function threadsUnder(card: Element): string[] {
  const ids: string[] = [];
  for (let n = card.nextElementSibling; n && !n.matches("[data-tool]"); n = n.nextElementSibling) {
    if (n.matches("[data-testid='child-thread']")) ids.push(n.getAttribute("data-child")!);
  }
  return ids;
}
const card = (el: HTMLElement, text: string) => [...el.querySelectorAll(":scope > [data-tool]")].find((c) => c.textContent!.includes(text))!;

test("an open-only thread stays folded and opens by click, Enter and Space", async () => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const opened: string[] = [];
  await act(async () => root!.render(<ChatThread sessionId="row" role="subagent" status="running" task="Research" openOnly collapsed onOpen={(id) => opened.push(id)}>
    <span>Hidden transcript</span>
  </ChatThread>));
  const row = host.querySelector<HTMLElement>("[role='button']")!;
  expect(host.textContent).not.toContain("Hidden transcript");
  expect(row.getAttribute("aria-expanded")).toBeNull();
  await act(async () => row.click());
  for (const key of ["Enter", " "]) await act(async () => row.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true })));
  expect(opened).toEqual(["row", "row", "row"]);
  expect(host.querySelector("[role='group']")).toBeNull();
  await act(async () => host!.querySelector<HTMLElement>("section")!.click());
  expect(opened).toHaveLength(4);
});

const start = () => [
  ev(EventTypes.ToolCalled, { tool: "task", callId: "c1", input: { description: "Inspect test capture setup", subagent_type: "explore", prompt: "Read the browser fixtures and report the configured viewport." } }, 120),
  ev(EventTypes.ChildStarted, { ...kid, kind: "task", title: "Inspect test capture setup", agentType: "explore", model: "claude-haiku-4.5" }, 120),
];

test("a live child is a folded subagent row under the call's card, without its transcript body", async () => {
  const el = await render([...start(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", input: { pattern: "viewport", path: "tests" } }, 119),
    ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", status: "completed", output: { head: "tests/conftest.py:12" } }, 119),
    ev(EventTypes.ChildMessage, { ...kid, text: "No viewport is set in the repo." }, 118)]);
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.getAttribute("data-child")).toBe("ses_kid");
  expect(thread.getAttribute("data-status")).toBe("running");
  expect(thread.querySelector("[data-role='subagent']")).toBeTruthy();
  expect(thread.getAttribute("aria-label")).toBe("Subagent subagent: Inspect test capture setup · explore");
  // Under the parent's card, not inside it.
  expect(threadsUnder(el.querySelector("[data-tool='task']")!)).toEqual(["ses_kid"]);
  expect(thread.querySelector("[role='group']")).toBeNull();
  expect(el.querySelector("[data-tool='grep']")).toBeNull();
  expect(thread.getAttribute("data-waiting")).toBeNull();
});

test("a child waiting on a permission says so in its header, on its stuck call and in a notice", async () => {
  const el = await render([...start(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k8", tool: "grep", input: { path: "/opt/venv" } }, 117),
    ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "external_directory", patterns: ["/opt/venv/*"], answered: "none" }, 117)]);
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.getAttribute("data-waiting")).toBe("true");
  expect(thread.querySelector("[data-testid='chat-thread-waiting']")!.textContent).toMatch(/^waiting 1h 57m on a permission$/);
  expect(thread.querySelector("[data-testid='child-waiting-notice']")).toBeNull();
  expect(thread.textContent).not.toContain("nothing in this Run can allow or refuse it");
});

test("a done child folds to its summary, with its turns counted", async () => {
  const el = await render([...start(),
    ev(EventTypes.ChildMessage, { ...kid, text: "Read it." }, 3),
    ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Playwright's 1280×720 default applies.\nMore.", costUsd: 0.37 }, 2),
    ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" }, 2)]);
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.getAttribute("data-status")).toBe("completed");
  expect(thread.querySelector("[role='group']")).toBeNull();
  expect(thread.textContent).toContain("Playwright's 1280×720 default applies.");
  expect(thread.textContent).not.toContain("More.");
  expect(thread.textContent).toContain("1 turns");
  expect(thread.textContent).toContain("$0.37");
});

test("two children of one turn are two threads, each under its own call", async () => {
  const el = await render([
    ev(EventTypes.ToolCalled, { tool: "task", callId: "s1", input: { description: "Inspect test capture setup" } }),
    ev(EventTypes.ToolCalled, { tool: "task", callId: "s2", input: { description: "Find the chart fixture dataset" } }),
    // Started in the other order from their calls: each still lands under its own.
    ev(EventTypes.ChildStarted, { childId: "b", callId: "s2", kind: "spawn", title: "Chart" }),
    ev(EventTypes.ChildStarted, { childId: "a", callId: "s1", kind: "spawn", title: "Capture" })]);
  expect(el.querySelectorAll(":scope > [data-tool]")).toHaveLength(2);
  expect(threadsUnder(card(el, "Inspect test capture setup"))).toEqual(["a"]);
  expect(threadsUnder(card(el, "Find the chart fixture dataset"))).toEqual(["b"]);
});

test("a child heard before its parent's call is drawn once, under that call's one card", async () => {
  const errors: unknown[] = [];
  const original = console.error;
  console.error = (...args: unknown[]) => { errors.push(args); };
  try {
    // Built in this order so the child's cursor precedes its call's.
    const child = ev(EventTypes.ChildStarted, { ...kid, kind: "task", title: "Inspect test capture setup" }, 120);
    const call = ev(EventTypes.ToolCalled, { tool: "task", callId: "c1", input: { description: "Inspect test capture setup" } }, 120);
    const el = await render([child, call]);
    expect(el.querySelectorAll("[data-testid='child-thread']")).toHaveLength(1);
    expect(el.querySelectorAll("[data-tool]")).toHaveLength(1);
    expect(errors.filter((e) => String(e).includes("same key"))).toEqual([]);
  } finally {
    console.error = original;
  }
});

test("a child watched while it works folds when it ends", async () => {
  const before = [...start(), ev(EventTypes.ChildMessage, { ...kid, text: "Reading." }, 3)];
  const el = await render(before);
  expect(el.querySelector("[data-testid='child-thread'] [role='group']")).toBeNull();
  const { turns } = project([...before, ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Done: 1280×720." }, 1)]);
  await act(async () => root!.render(<>{turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</>));
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.querySelector("[role='group']")).toBeNull();
  expect(thread.textContent).toContain("Done: 1280×720.");
});

// c1 → ses_kid (depth 1) → its call k2 → ses_grand (depth 2) → its call k3 → ses_great (depth 3).
const deep = () => [...start(),
  ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k2", tool: "task", input: { description: "Deeper" } }, 110),
  ev(EventTypes.ChildStarted, { childId: "ses_grand", callId: "k2", parentChildId: "ses_kid", kind: "task", title: "Deeper" }, 110),
  ev(EventTypes.ChildToolCalled, { childId: "ses_grand", parentCallId: "k2", callId: "k3", tool: "task", input: { description: "Deepest" } }, 109),
  ev(EventTypes.ChildStarted, { childId: "ses_great", callId: "k3", parentChildId: "ses_grand", kind: "task", title: "Deepest" }, 109),
  ev(EventTypes.ChildMessage, { childId: "ses_great", callId: "k3", text: "Found it at depth three." }, 108),
];

const threadOf = (el: HTMLElement, id: string) => el.querySelector<HTMLElement>(`[data-testid='child-thread'][data-child='${id}']`);
const header = (thread: HTMLElement) => thread.querySelector<HTMLElement>(":scope > [role='button']")!;

test("a live child starts folded and deeper children are not drawn inline", async () => {
  const el = await render(deep());
  expect(threadOf(el, "ses_kid")!.querySelector("[role='group']")).toBeNull();
  expect(header(threadOf(el, "ses_kid")!).getAttribute("aria-expanded")).toBeNull();
  expect(threadOf(el, "ses_grand")).toBeNull();
  expect(threadOf(el, "ses_great")).toBeNull();
});

test("opening a row uses its screen callback without creating a per-thread dialog", async () => {
  const { ChildOpenContext } = await import("../src/childThreads.tsx");
  const events = deep();
  const el = await render(events);
  const opened: string[] = [];
  await act(async () => root!.render(<ChildOpenContext.Provider value={(id) => opened.push(id)}>{project(events).turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</ChildOpenContext.Provider>));
  await act(async () => header(threadOf(el, "ses_kid")!).click());
  expect(opened).toEqual(["ses_kid"]);
  expect(document.querySelector("[role='dialog']")).toBeNull();
});

test("a child still running when its Run ended shows it settled, then how it ended once that is heard", async () => {
  const state = apply(emptyProjection(), start());
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const draw = (runStatus?: "aborted") =>
    act(async () => root!.render(<>{snapshot(state, runStatus).turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</>));
  await draw();
  expect(threadOf(host, "ses_kid")!.getAttribute("data-status")).toBe("running");

  await draw("aborted");
  expect(threadOf(host, "ses_kid")!.getAttribute("data-status")).toBe("aborted");

  apply(state, [
    ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Done now", costUsd: 0.37 }),
    ev(EventTypes.ToolCompleted, { tool: "task", callId: "c1", status: "completed" })]);
  await draw("aborted");
  const thread = threadOf(host, "ses_kid")!;
  expect(thread.getAttribute("data-status")).toBe("completed");
  expect(thread.textContent).toContain("Done now");
  expect(thread.textContent).toContain("$0.37");
});

// The Run's status as the screen reads it again; `aborted` settles the conversation.
let runStatus: "running" | "aborted" = "running";
afterEach(() => { runStatus = "running"; });

async function screen(events: PersistedEvent[]) {
  class Client extends FixtureClient {
    override async events() { return { events, nextCursor: events.at(-1)?.cursor ?? 0 }; }
    override async getRun(id: string) { return { ...await super.getRun(id), status: runStatus }; }
  }
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<RunScreen client={new Client("a")} runId={RUN_ID} onBack={() => {}} />));
  await until(() => host!.querySelector("[data-testid='session-rail']"), "session rail");
  await act(async () => { for (const event of events) emit({ ...event, runId: RUN_ID }); });
  return host;
}

const send = (event: PersistedEvent) => act(async () => emit({ ...event, runId: RUN_ID }));
const openPanel = async (el: HTMLElement, id: string) => {
  await act(async () => header(threadOf(el, id)!).click());
  return el.querySelector<HTMLElement>("[data-testid='child-panel']")!;
};

describe("the selected child's transcript follows it while open", () => {
  test("a new message appears in the open panel", async () => {
    const el = await screen([...start(), ev(EventTypes.ChildMessage, { ...kid, text: "Initial child message." })]);
    const panel = await openPanel(el, "ses_kid");
    expect(panel.textContent).toContain("Initial child message.");
    await send(ev(EventTypes.ChildMessage, { ...kid, text: "Live child message." }));
    expect(panel.isConnected).toBe(true);
    expect(panel.textContent).toContain("Live child message.");
  });

  test("a call completing with output updates its card in the open panel", async () => {
    const el = await screen([...start(),
      ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", input: { pattern: "viewport" } })]);
    const panel = await openPanel(el, "ses_kid");
    expect(panel.querySelector("[data-tool='grep']")!.getAttribute("data-status")).toBe("running");
    await send(ev(EventTypes.ChildToolCompleted, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", status: "completed", output: { head: "tests/conftest.py:12 viewport" } }));
    const card = panel.querySelector<HTMLElement>("[data-tool='grep']")!;
    expect(card.getAttribute("data-status")).toBe("completed");
    await act(async () => card.querySelector<HTMLElement>("[aria-expanded]")!.click());
    expect(panel.querySelector("[data-tool='grep']")!.textContent).toContain("tests/conftest.py:12 viewport");
  });

  test("waiting on a permission clears in the open panel when the next call comes", async () => {
    const el = await screen([...start(),
      ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k8", tool: "grep", input: { path: "/opt/venv" } }, 117),
      ev(EventTypes.ChildWaiting, { ...kid, permission: "external_directory", patterns: ["/opt/venv/*"], answered: "none" }, 117)]);
    const panel = await openPanel(el, "ses_kid");
    expect(panel.querySelector("[data-testid='child-waiting-notice']")).toBeTruthy();
    await send(ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k9", tool: "bash", input: { command: "ls /opt/venv" } }));
    expect(panel.querySelector("[data-testid='child-waiting-notice']") === null).toBe(true);
    expect(panel.textContent).not.toContain("nothing in this Run can allow or refuse it");
    expect(panel.querySelector("[data-tool='bash']")).toBeTruthy();
  });

  test("the Run ending settles the open panel's body, not only its header", async () => {
    const el = await screen([...start(),
      ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k1", tool: "grep", input: { pattern: "viewport" } })]);
    const panel = await openPanel(el, "ses_kid");
    expect(panel.querySelector("[data-tool='grep']")!.getAttribute("data-status")).toBe("running");
    runStatus = "aborted";
    await send(ev(EventTypes.RunAborted, {}));
    await until(() => panel.querySelector("[data-tool='grep'][data-status='aborted']"), "the settled call in the panel");
  });
});

test("the screen opens a child beside its transcript, navigates children and returns, then Escape closes", async () => {
  const el = await screen(deep());
  expect(el.querySelector("[data-testid='child-panel']")).toBeNull();
  expect(el.querySelectorAll("[data-testid='child-thread']")).toHaveLength(1);
  const rail = el.querySelector("[data-testid='rail-subagents']")!;
  expect(rail.textContent).toContain("Subagents · 3");
  expect(rail.querySelectorAll("button[data-child]")).toHaveLength(1);
  expect(rail.textContent).toContain("running · task");
  await act(async () => header(threadOf(el, "ses_kid")!).click());
  const panel = () => el.querySelector<HTMLElement>("[data-testid='child-panel']")!;
  expect(panel().getAttribute("data-child")).toBe("ses_kid");
  expect(panel().querySelector("[data-intent='prompt']")!.textContent).toContain("Read the browser fixtures and report the configured viewport.");
  expect(panel().querySelector("[data-tool='task']")).toBeTruthy();
  expect(el.querySelector("[data-testid='session-rail']")).toBeNull();
  expect(threadOf(panel(), "ses_grand")!.querySelector("[role='group']")).toBeNull();
  await act(async () => header(threadOf(panel(), "ses_grand")!).click());
  expect(panel().getAttribute("data-child")).toBe("ses_grand");
  await act(async () => header(threadOf(panel(), "ses_great")!).click());
  expect(panel().getAttribute("data-child")).toBe("ses_great");
  expect(panel().textContent).toContain("Found it at depth three.");
  await act(async () => [...panel().querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "Deeper")!.click());
  expect(panel().getAttribute("data-child")).toBe("ses_grand");
  const back = [...panel().querySelectorAll<HTMLButtonElement>("button")].find((button) => button.textContent === "Inspect test capture setup")!;
  await act(async () => back.click());
  expect(panel().getAttribute("data-child")).toBe("ses_kid");
  await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(el.querySelector("[data-testid='child-panel']")).toBeNull();
  await act(async () => el.querySelector<HTMLButtonElement>("[data-testid='rail-subagents'] button")!.click());
  expect(panel().getAttribute("data-child")).toBe("ses_kid");
});

test("a description-only call is the child's prompt turn fallback", async () => {
  const el = await screen([
    ev(EventTypes.ToolCalled, { tool: "task", callId: "c1", input: { description: "Read the test defaults." } }),
    ev(EventTypes.ChildStarted, { ...kid, kind: "task", title: "Capture defaults" }),
  ]);
  const panel = await openPanel(el, "ses_kid");
  expect(panel.querySelector("[data-intent='prompt']")!.textContent).toContain("Read the test defaults.");
});

test("desktop keyboard browsing returns focus to the root row and Back to the nested row", async () => {
  const el = await screen(deep());
  const opener = header(threadOf(el, "ses_kid")!);
  opener.focus();
  await act(async () => opener.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
  let panel = el.querySelector<HTMLElement>("[data-testid='child-panel']")!;
  await act(async () => header(threadOf(panel, "ses_grand")!).click());
  panel = el.querySelector<HTMLElement>("[data-testid='child-panel']")!;
  const back = [...panel.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent === "Inspect test capture setup")!;
  back.focus();
  await act(async () => back.click());
  expect(document.activeElement === header(threadOf(panel, "ses_grand")!)).toBe(true);
  panel.querySelector<HTMLButtonElement>("button[aria-label='Close subagent']")!.focus();
  await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
  expect(document.activeElement === opener).toBe(true);
});

test("a rail opener moves focus into the panel, then restores the newly mounted quicklink", async () => {
  const el = await screen(start());
  const opener = el.querySelector<HTMLButtonElement>("[data-testid='rail-subagents'] button[data-child='ses_kid']")!;
  opener.focus();
  await act(async () => opener.click());
  const close = el.querySelector<HTMLButtonElement>("button[aria-label='Close subagent']")!;
  expect(document.activeElement === close).toBe(true);
  await act(async () => close.click());
  const restored = el.querySelector<HTMLButtonElement>("[data-testid='rail-subagents'] button[data-child='ses_kid']")!;
  expect(restored !== opener).toBe(true);
  expect(document.activeElement === restored).toBe(true);
});

test("permission waiting stays on the row and rail, with the notice and stuck call only in the panel", async () => {
  const el = await screen([...start(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k8", tool: "grep", input: { path: "/opt/venv" } }, 117),
    ev(EventTypes.ChildWaiting, { ...kid, permission: "external_directory", patterns: ["/opt/venv/*"], answered: "none" }, 117)]);
  expect(threadOf(el, "ses_kid")!.textContent).toContain("waiting 1h 57m on a permission");
  expect(el.querySelector("[data-testid='rail-subagents']")!.textContent).toContain("waiting 1h 57m on a permission");
  expect(el.querySelector("[data-testid='child-waiting-notice']")).toBeNull();
  await act(async () => header(threadOf(el, "ses_kid")!).click());
  const panel = el.querySelector("[data-testid='child-panel']")!;
  expect(panel.querySelector("[data-testid='child-waiting-notice']")!.textContent).toContain("Nothing in this Run can answer it.");
  expect(panel.textContent).toContain("nothing in this Run can allow or refuse it");
  expect(panel.textContent).toContain("waiting 1h 57m on a permission");
});

test("below 960px the screen uses the xl dialog fallback", async () => {
  const original = window.matchMedia;
  window.matchMedia = ((query: string) => ({ matches: query.includes("959px"), addEventListener() {}, removeEventListener() {} })) as typeof window.matchMedia;
  try {
    const el = await screen(start());
    await act(async () => header(threadOf(el, "ses_kid")!).click());
    expect(document.querySelector("[role='dialog'] [data-testid='child-panel']")?.getAttribute("data-child")).toBe("ses_kid");
    expect(el.querySelector("[data-testid='session-rail']")).toBeTruthy();
    await act(async () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(document.querySelector("[data-testid='child-panel']")).toBeNull();
  } finally {
    window.matchMedia = original;
  }
});

test("a Run without children has no Subagents block", async () => {
  const el = await screen([]);
  expect(el.querySelector("[data-testid='rail-subagents']")).toBeNull();
});

describe("a child's thread is drawn again only when it or a child under it changed", () => {
  const ids = ["a", "b", "a-grand"] as const;
  type Runs = Record<(typeof ids)[number], number>;
  let state: ReturnType<typeof emptyProjection>;
  // How many times each child's real thread view and body functions ran, by child id.
  const threadRuns: Runs = { a: 0, b: 0, "a-grand": 0 };
  const bodyRuns: Runs = { a: 0, b: 0, "a-grand": 0 };
  const count = (runs: Runs, id: string) => { runs[id as keyof Runs] += 1; };
  const clear = () => { for (const id of ids) threadRuns[id] = bodyRuns[id] = 0; };

  // Call-through spies at each real function's own export; the memo around the
  // view in childThreads.tsx renders whatever that export is at render time.
  beforeAll(() => {
    mock.module("../src/childThreadView.tsx", () => ({
      ...realView,
      ChildThreadView: (props: Parameters<typeof RealChildThreadView>[0]) => { count(threadRuns, props.child.id); return RealChildThreadView(props); },
    }));
    mock.module("../src/childBody.tsx", () => ({
      ...realBody,
      ChildBody: (props: Parameters<typeof RealChildBody>[0]) => { count(bodyRuns, props.child.id); return RealChildBody(props); },
    }));
  });
  afterAll(() => {
    mock.module("../src/childThreadView.tsx", () => realView);
    mock.module("../src/childBody.tsx", () => realBody);
  });

  async function draw(): Promise<void> {
    await act(async () => root!.render(<>{snapshot(state).turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</>));
  }

  beforeEach(async () => {
    state = apply(emptyProjection(), [
      ev(EventTypes.ToolCalled, { tool: "task", callId: "s1", input: { description: "First" } }),
      ev(EventTypes.ToolCalled, { tool: "task", callId: "s2", input: { description: "Second" } }),
      ev(EventTypes.ChildStarted, { childId: "a", callId: "s1", kind: "task", title: "Alpha" }),
      ev(EventTypes.ChildStarted, { childId: "b", callId: "s2", kind: "task", title: "Beta" }),
      ev(EventTypes.ChildToolCalled, { childId: "a", parentCallId: "s1", callId: "a1", tool: "task", input: {} }),
      ev(EventTypes.ChildStarted, { childId: "a-grand", callId: "a1", parentChildId: "a", kind: "task", title: "Alpha's own" }),
    ]);
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    clear();
    await draw();
    // The spies are the ones rendering: every thread and every open body ran on mount.
    expect(threadRuns).toEqual({ a: 1, b: 1, "a-grand": 0 });
    expect(bodyRuns).toEqual({ a: 0, b: 0, "a-grand": 0 });
    clear();
  });

  test("an event on a runs a's thread and body and neither b's thread nor its body", async () => {
    apply(state, [ev(EventTypes.ChildMessage, { childId: "a", callId: "s1", text: "Alpha speaks." })]);
    await draw();
    expect(threadRuns.a).toBeGreaterThan(0);
     expect(bodyRuns.a).toBe(0);
    expect(threadRuns.b).toBe(0);
    expect(bodyRuns.b).toBe(0);
    expect(threadOf(host!, "a")!.textContent).toContain("Alpha speaks.");
  });

  test("an event on b runs b's thread and neither a's nor a's grandchild's", async () => {
    apply(state, [ev(EventTypes.ChildMessage, { childId: "b", callId: "s2", text: "Beta speaks." })]);
    await draw();
    expect(threadRuns.b).toBeGreaterThan(0);
    expect(threadRuns.a).toBe(0);
    expect(bodyRuns.a).toBe(0);
    expect(threadRuns["a-grand"]).toBe(0);
    expect(threadOf(host!, "b")!.textContent).toContain("Beta speaks.");
  });

  test("an event on a's grandchild runs its thread and a's, and not b's", async () => {
    apply(state, [ev(EventTypes.ChildMessage, { childId: "a-grand", callId: "a1", text: "Grandchild speaks." })]);
    await draw();
    expect(threadRuns["a-grand"]).toBe(0);
    expect(threadRuns.a).toBeGreaterThan(0);
    expect(threadRuns.b).toBe(0);
    expect(bodyRuns.b).toBe(0);
    expect(threadOf(host!, "a-grand")).toBeNull();
  });

  test("a hidden grandchild's event does not mount a transcript body", async () => {
    apply(state, [ev(EventTypes.ChildMessage, { childId: "a-grand", callId: "a1", text: "Grandchild speaks." })]);
    await draw();
    expect(bodyRuns["a-grand"]).toBe(0);
    expect(threadRuns.a).toBeGreaterThan(0);
    expect(threadRuns.b).toBe(0);
    expect(host!.textContent).not.toContain("Grandchild speaks.");
  });

  test("a waiting on a permission, then its next call clearing it, runs a both times and never b", async () => {
    apply(state, [ev(EventTypes.ChildWaiting, { childId: "a", callId: "s1", permission: "bash", patterns: ["cat /etc/hostname"], answered: "none" })]);
    await draw();
    expect(threadRuns.a).toBeGreaterThan(0);
    expect(threadRuns.b).toBe(0);
    expect(threadOf(host!, "a")!.getAttribute("data-waiting")).toBe("true");

    clear();
    apply(state, [ev(EventTypes.ChildToolCalled, { childId: "a", parentCallId: "s1", callId: "a2", tool: "bash", input: { command: "cat /etc/hostname" } })]);
    await draw();
    expect(threadRuns.a).toBeGreaterThan(0);
    expect(threadRuns.b).toBe(0);
    expect(threadOf(host!, "a")!.getAttribute("data-waiting")).toBeNull();
  });
});
