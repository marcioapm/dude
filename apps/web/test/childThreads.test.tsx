/**
 * A tool call's child sessions as the transcript renders them: one
 * subagent thread each under the call's card, its own calls and messages
 * inside, folded once done, and a permission it waits on said in its
 * header, on its stuck call and in a notice.
 */

import { afterEach, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EventTypes } from "@dude/domain";
import type { PersistedEvent } from "@dude/domain";
import { renderTurn } from "../src/screens/RunScreen.tsx";
import { project } from "../src/api/conversation.ts";

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

const start = () => [
  ev(EventTypes.ToolCalled, { tool: "task", callId: "c1", input: { description: "Inspect test capture setup", subagent_type: "explore" } }, 120),
  ev(EventTypes.ChildStarted, { ...kid, kind: "task", title: "Inspect test capture setup", agentType: "explore", model: "claude-haiku-4.5" }, 120),
];

test("a live child is an open subagent thread under the call's card, with its call and message inside", async () => {
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
  expect(thread.previousElementSibling?.textContent).toContain("task");
  const body = thread.querySelector("[role='group']")!;
  expect(body.textContent).toContain("grep");
  expect(body.textContent).toContain("No viewport is set in the repo.");
  expect(thread.getAttribute("data-waiting")).toBeNull();
});

test("a child waiting on a permission says so in its header, on its stuck call and in a notice", async () => {
  const el = await render([...start(),
    ev(EventTypes.ChildToolCalled, { ...kid, parentCallId: "c1", callId: "k8", tool: "grep", input: { path: "/opt/venv" } }, 117),
    ev(EventTypes.ChildWaiting, { ...kid, on: "permission", permission: "external_directory", patterns: ["/opt/venv/*"], answered: "none" }, 117)]);
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.getAttribute("data-waiting")).toBe("true");
  expect(thread.querySelector("[data-testid='chat-thread-waiting']")!.textContent).toMatch(/^waiting 1h 57m on a permission$/);
  expect(thread.querySelector("[data-testid='child-waiting-notice']")!.textContent)
    .toContain("Waiting on a permission: external_directory for /opt/venv/*. Nothing in this Run can answer it.");
  expect(thread.textContent).toContain("nothing in this Run can allow or refuse it");
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
    ev(EventTypes.ToolCalled, { tool: "task", callId: "s1" }), ev(EventTypes.ToolCalled, { tool: "task", callId: "s2" }),
    ev(EventTypes.ChildStarted, { childId: "a", callId: "s1", kind: "spawn", title: "Inspect test capture setup" }),
    ev(EventTypes.ChildStarted, { childId: "b", callId: "s2", kind: "spawn", title: "Find the chart fixture dataset" })]);
  expect([...el.querySelectorAll("[data-testid='child-thread']")].map((t) => t.getAttribute("data-child"))).toEqual(["a", "b"]);
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
  expect(el.querySelector("[data-testid='child-thread'] [role='group']")).toBeTruthy();
  const { turns } = project([...before, ev(EventTypes.ChildEnded, { ...kid, status: "completed", summary: "Done: 1280×720." }, 1)]);
  await act(async () => root!.render(<>{turns.map((t) => renderTurn(t, "implementer", 0, false, people, "dude"))}</>));
  const thread = el.querySelector("[data-testid='child-thread']")!;
  expect(thread.querySelector("[role='group']")).toBeNull();
  expect(thread.textContent).toContain("Done: 1280×720.");
});
