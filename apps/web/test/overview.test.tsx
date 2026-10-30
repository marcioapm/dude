/**
 * The task's Overview: the figures, the agents at work, the one needs-you
 * region, the branch card, the pull request beside them, milestones, and the
 * details folded under them. Mounted in happy-dom against the fixture client.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PersistedEvent } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { ORG, PROJECT, RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import type { TaskMetrics } from "../src/api/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { openAgentQuestions, recordedActivity } from "../src/screens/TaskOverview.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function overview(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector(props.tab ? "[data-testid=task-screen]" : "[data-testid=task-overview]"), "the page");
  await settle();
  return container;
}

const fx = (as?: string, scenario: "a" | "d" = "a") => {
  if (as) localStorage.setItem("dude.fixtures.run", as);
  return new FixtureClient(scenario);
};
const text = (el: Element | null | undefined) => el?.textContent ?? "";
const tile = (page: HTMLElement, id: string) => page.querySelector(`[data-testid=${id}]`);
const selectedTab = (page: HTMLElement) => page.querySelector("[role=tab][aria-selected=true]")?.textContent?.replace(/\d+$/, "");

let seq = 50_000;
const ev = (eventType: string, payload: Record<string, unknown>, runId: string | null = RUN_ID, minAgo = 1): PersistedEvent => ({
  eventId: `evt_ov_${++seq}`, eventType, occurredAt: new Date(Date.now() - minAgo * 60_000).toISOString(), organizationId: ORG.id, projectId: PROJECT.id,
  taskId: TASK_ID, runId, sessionId: null, workflowRunId: null, actor: { type: "agent", id: runId ?? "dude" }, source: "harness",
  correlationId: null, causationId: null, payload, cursor: seq,
});

/** Scenario a's client with extra events at the end of its ledger. */
function withEvents(client: FixtureClient, extra: PersistedEvent[]): FixtureClient {
  const events = client.events.bind(client);
  client.events = async (params) => {
    const r = await events(params);
    const more = (params.after ?? 0) < seq ? extra.filter((e) => e.cursor > (params.after ?? 0)) : [];
    return { events: [...r.events, ...more], nextCursor: Math.max(r.nextCursor, ...more.map((e) => e.cursor)) };
  };
  return client;
}

describe("the figures", () => {
  test("a task with one attempt: its spend, model + machine, lead time, agents at work and what is unresolved; no attempt tile", async () => {
    const page = await overview(fx(undefined, "d"));
    expect(text(tile(page, "overview-spend"))).toContain("$2.41");
    expect(text(tile(page, "overview-spend"))).toContain("this task · model + machine");
    expect(tile(page, "overview-attempt-spend")).toBeNull();
    expect(text(tile(page, "overview-lead"))).toContain("2d 0h");
    expect(text(tile(page, "overview-active"))).toContain("0");
    // Scenario d: one finding open (f4), feedback holding none.
    expect(text(tile(page, "overview-unresolved"))).toContain("1 open finding · 0 feedback holding");
  });

  test("a task started over: the spend of every attempt, and the attempt's beside it", async () => {
    const page = await overview(fx("restarted"));
    // restartedMetrics: attempt 1's 4 Runs at $3.35, attempt 2's 4 at $1.10.
    await until(() => (text(tile(page, "overview-spend")).includes("$17.80") ? true : null), "the task's spend");
    expect(text(tile(page, "overview-spend"))).toContain("all attempts · model + machine");
    expect(text(tile(page, "overview-attempt-spend"))).toContain("Attempt 2 spend");
    expect(text(tile(page, "overview-attempt-spend"))).toContain("$4.40");
    expect(text(tile(page, "overview-attempt-spend"))).toContain("current attempt");
  });

  test("no runs: spend is not reported, never $0.00", async () => {
    const client = fx();
    const none: TaskMetrics = { leadMs: 0, activeMs: 0, humanWaitMs: 0, reviewMs: 0, costUsd: 0, cost: { totalUsd: 0, tokensUsd: 0, machineUsd: 0 }, tokens: { input: 0, output: 0 }, runs: [] };
    client.taskMetrics = async () => none;
    const page = await overview(client);
    expect(text(tile(page, "overview-spend"))).toContain("Not reported");
    expect(text(tile(page, "overview-spend"))).not.toContain("$0.00");
    expect(text(page.querySelector("[data-testid=overview-time-cost]"))).toContain("Cost is not reported, not $0.00.");
  });

  test("Time & cost holds every tile and the run table, folded until opened", async () => {
    const page = await overview(fx(undefined, "d"));
    const fold = page.querySelector<HTMLDetailsElement>("[data-testid=overview-time-cost]")!;
    expect(fold.open).toBe(false);
    const metrics = await until(() => fold.querySelector("[data-testid=task-metrics]"), "the full figures");
    for (const label of ["Lead time", "Agents working", "Waiting on people", "In review", "Cost"]) expect(text(metrics)).toContain(label);
    expect([...metrics.querySelectorAll("[data-testid=run-metrics] th")].map((th) => th.textContent)).toEqual(["Run", "Working", "Parked", "Tokens in / out", "Cost"]);
    await click(fold.querySelector("summary")!);
    expect(fold.open).toBe(true);
  });
});

describe("running now", () => {
  test("one row per live Run: who, its model, what it last did as recorded, its cost and the way to its session", async () => {
    const opened: string[] = [];
    const page = await overview(fx(), { onOpenRun: (id) => opened.push(id) });
    const rows = [...page.querySelectorAll("[data-testid=running-run]")];
    expect(rows.map((r) => r.getAttribute("data-run"))).toEqual([RUN_ID]);
    const row = rows[0]!;
    expect(text(row)).toContain("Implement");
    expect(text(row)).toContain("claude-opus-5-5");
    // The ledger's last word from it: its `npm test` call, after its plan's step 4.
    expect(text(row)).toContain("bash · npm test -- checkout");
    expect(text(row)).toContain("$2.41");
    await click(row.querySelector("button[aria-label=\"Open Implement's session\"]")!);
    expect(opened).toEqual([RUN_ID]);
  });

  test("nothing live: one line from the recorded state, no rows", async () => {
    const page = await overview(fx(undefined, "d"));
    expect(page.querySelectorAll("[data-testid=running-run]").length).toBe(0);
    expect(text(page.querySelector("[data-testid=overview-waiting]"))).toBe("Nothing is running: the pull request waits on its review and checks.");
  });

  test("what a Run is doing: its plan's current step when that is the latest record, else its latest tool call, else nothing", () => {
    const plan = ev("agent.plan.updated", { todos: [{ content: "Map it", status: "completed" }, { content: "Split it", status: "in_progress" }, { content: "Gate it", status: "pending" }] });
    const tool = ev("agent.tool.called", { tool: "bash", title: "npm test" });
    expect(recordedActivity(RUN_ID, [tool, plan])).toBe("Split it · step 2 of 3");
    expect(recordedActivity(RUN_ID, [plan, tool])).toBe("bash · npm test");
    expect(recordedActivity(RUN_ID, [ev("agent.message", { text: "hi" })])).toBeNull();
    expect(recordedActivity("run_other", [plan])).toBeNull();
    // Its own question open: waiting on the answer; once answered, back to what it does.
    const ask = ev("question.asked", { kind: "agent", questionId: "q_9", toName: "Ana Ribeiro", prompt: "Which?" });
    expect(recordedActivity(RUN_ID, [tool, ask])).toBe("Asked Ana Ribeiro a question · waiting for the answer");
    expect(recordedActivity(RUN_ID, [tool, ask, ev("question.answered", { questionId: "q_9", answer: "A" })])).toBe("bash · npm test");
  });
});

describe("needs you", () => {
  const asked = (questionId: string, extra: Record<string, unknown> = {}) =>
    ev("question.asked", { kind: "agent", questionId, prompt: "Keep the invoice option on monthly plans?", toName: "Ana Ribeiro", ...extra });

  test("an agent's open question is quoted once, above the figures, naming who can answer, with the way to it", async () => {
    const opened: string[] = [];
    const page = await overview(withEvents(fx(), [asked("q_1")]), { onOpenRun: (id) => opened.push(id) });
    const region = await until(() => page.querySelector("[data-testid=needs-you]"), "the needs-you region");
    expect(region.getAttribute("data-tone")).toBe("attention");
    expect(text(region)).toContain("Needs Ana Ribeiro · Implement asks");
    expect(text(region)).toContain("Keep the invoice option on monthly plans?");
    // Inside the Overview, before its figures: one place.
    const overviewEl = page.querySelector("[data-testid=task-overview]")!;
    expect(overviewEl.contains(region)).toBe(true);
    expect(region.compareDocumentPosition(page.querySelector("[data-testid=overview-summary]")!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(page.querySelectorAll("[data-testid=needs-you]").length).toBe(1);
    await click(region.querySelector("[data-testid=needs-you-answer]")!);
    expect(opened).toEqual([RUN_ID]);
  });

  test("answered or closed, it is gone", () => {
    expect(openAgentQuestions([asked("q_1"), ev("question.answered", { questionId: "q_1", answer: "No" })])).toEqual([]);
    expect(openAgentQuestions([asked("q_1"), ev("question.closed", { questionId: "q_1", by: "withdrawn" })])).toEqual([]);
    // The workflow's escalation is a question too, but not one an agent asked: its panel says it.
    expect(openAgentQuestions([ev("question.asked", { kind: "escalation", questionId: "q_2", prompt: "Decide" })])).toEqual([]);
    const several = openAgentQuestions([asked("q_3", { items: [{ header: "Scope", question: "A?" }, { header: "Route", question: "B?" }] })]);
    expect(several.map((q) => q.text)).toEqual(["2 questions: Scope, Route"]);
  });

  test("other tabs keep the task's notices above the tabs, without the Overview's needs-you region", async () => {
    const page = await overview(withEvents(fx("stalled"), [asked("q_1")]), { tab: "findings" });
    await until(() => page.querySelector("[data-testid=stalled]"), "the stalled notice");
    expect(page.querySelector("[data-testid=needs-you]")).toBeNull();
    expect(page.querySelector("[data-testid=task-notices]")!.closest("[data-testid=task-overview]")).toBeNull();
  });

  test("on Overview the task's other notices render in the same region, above the figures", async () => {
    const page = await overview(fx("stalled"));
    const stalled = await until(() => page.querySelector("[data-testid=stalled]"), "the stalled notice");
    expect(stalled.closest("[data-testid=task-overview]")).not.toBeNull();
    expect(page.querySelectorAll("[data-testid=task-notices]").length).toBe(1);
  });
});

describe("the branch, the pull request and the details", () => {
  test("the Changes card names the branch, its target and the last head, and leads to Changes; no counts it cannot read", async () => {
    const page = await overview(fx(undefined, "d"));
    const card = page.querySelector("[data-testid=overview-changes]")!;
    const branch = card.querySelector("[data-testid=overview-branch]")!;
    expect(text(branch)).toBe("dude/tsk_01j9x4kq/checkout-v2-split-payment→ mainat c91f7e2");
    expect(text(card)).not.toMatch(/\d+ files?/);
    await click(card.querySelector("[data-testid=overview-review-changes]")!);
    expect(selectedTab(page)).toBe("Changes");
  });

  test("the pull request's panel sits in its own column with its actions", async () => {
    const page = await overview(fx(undefined, "d"));
    const column = page.querySelector("[data-testid=overview-pull-requests]")!;
    expect(column.querySelector("[data-testid=pr-panel]")).not.toBeNull();
    expect(text(column)).toContain("Open on GitHub");
  });

  test("milestones are Activity's newest lines, and Full activity opens Activity", async () => {
    const page = await overview(fx(undefined, "d"));
    const lines = [...page.querySelectorAll("[data-testid=overview-milestone]")].map((l) => text(l));
    expect(lines.length).toBe(4);
    expect(lines[0]).toContain("opened #482");
    await click(page.querySelector("[data-testid=overview-full-activity]")!);
    expect(selectedTab(page)).toBe("Activity");
  });

  test("goal and every criterion, the pipeline and the task's facts are folded under the summary, whole", async () => {
    const page = await overview(fx(undefined, "d"));
    const goal = page.querySelector("[data-testid=overview-goal-criteria]")!;
    expect(text(goal.querySelector("summary"))).toBe("Goal & acceptance criteria3 criteria");
    expect(goal.querySelectorAll("[data-testid=overview-criterion]").length).toBe(3);
    expect(page.querySelector("[data-testid=overview-pipeline] [data-testid=pipeline]")).not.toBeNull();
    expect(page.querySelector("[data-testid=overview-servers] [data-testid=task-facts]")).not.toBeNull();
    // Opened, a fold stays open after a trip to another tab.
    await act(async () => { (page.querySelector("[data-testid=overview-goal-criteria] summary") as HTMLElement).click(); });
    expect(page.querySelector<HTMLDetailsElement>("[data-testid=overview-goal-criteria]")!.open).toBe(true);
    const tab = (name: string) => [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
    await act(async () => void tab("Findings").dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })));
    await act(async () => void tab("Overview").dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })));
    await settle();
    expect(page.querySelector<HTMLDetailsElement>("[data-testid=overview-goal-criteria]")!.open).toBe(true);
  });
});

describe("the fixtures' Overview states", () => {
  test("reviewing: two reviewers at work on the open pull request, each with what it records", async () => {
    const page = await overview(fx("reviewing"));
    const rows = [...page.querySelectorAll("[data-testid=running-run]")];
    expect(rows.map((r) => r.getAttribute("data-run"))).toEqual(["run_ov_quality", "run_ov_security"]);
    expect(text(rows[0])).toContain("Check retry of the payment step against concurrent submits · step 2 of 3");
    expect(text(rows[1])).toContain("bash · rg -n \"invoice\" apps/web/src/checkout");
    expect(page.querySelector("[data-testid=overview-pull-requests] [data-testid=pr-panel]")).not.toBeNull();
    // Lead time runs from before the first Run (200 minutes before the fixtures loaded), not from the newest (6).
    expect(text(tile(page, "overview-lead"))).toMatch(/Lead time3h 3\dm/);
  });

  test("question: the implementer's question is the needs-you region, and its row says it waits on the answer", async () => {
    const page = await overview(fx("question"));
    const region = await until(() => page.querySelector("[data-testid=needs-you]"), "the question");
    expect(text(region)).toContain("Should the Invoice option stay hidden on monthly plans");
    expect(text(page.querySelector("[data-testid=running-run]"))).toContain("Asked Márcio Martins a question · waiting for the answer");
  });

  test("ready: approved and passing, nothing running, nothing unresolved", async () => {
    const page = await overview(fx("ready"));
    expect(page.querySelectorAll("[data-testid=running-run]").length).toBe(0);
    expect(text(page.querySelector("[data-testid=pr-panel]"))).toContain("Ready to merge");
    expect(text(tile(page, "overview-unresolved"))).toContain("nothing open");
  });

  test("new: the two ways to start in Running now's place, no spend, no branch", async () => {
    const page = await overview(fx("new"));
    expect(page.querySelector("[data-testid=overview-running] [data-testid=start-choice]")).not.toBeNull();
    expect(text(tile(page, "overview-spend"))).toContain("Not reported");
    expect(page.querySelector("[data-testid=overview-changes-none]")).not.toBeNull();
  });
});
