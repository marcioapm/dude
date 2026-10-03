/**
 * The conductor's decisions in the web app: Talk it through and Deliver
 * side by side on a task not started; on a conducted task, who decides
 * above the composer with Let Deliver finish it, each Run the conductor
 * started as a line in Chat that opens its session, the decision waited
 * on as dude's notice, and the Sessions tree — the conductor's Runs under
 * it, Deliver's under "Delivered automatically". Mounted in happy-dom
 * against the fixture client.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { Decider, PersistedEvent, Run } from "@dude/domain";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { conductedLines } from "../src/conducted.ts";
import { taskHistory } from "../src/taskHistory.ts";
import { interleaved } from "../src/screens/RunScreen.tsx";
import { ApiError, type ChatSent, type RunDetail, type TaskDetail } from "../src/api/client.ts";
import { firstMessage } from "../src/screens/ChatSection.tsx";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const CONDUCTOR = "run_cond_dec";
const at = (s: number) => new Date(Date.UTC(2026, 9, 3, 10, 0, s)).toISOString();

function run(p: Partial<Run> & { id: string }): Run {
  return { organizationId: "org", projectId: "prj", taskId: TASK_ID, attempt: 1, status: "completed", workerId: null,
    workspacePath: null, error: null, kind: "agent", phase: "implement", role: "implementer", category: null, parentRunId: null,
    conductorRunId: null, baseRefs: {}, heads: {}, branch: null, harness: "scripted", model: "fake/scripted", modelTier: null, dudePause: null,
    tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 }, machine: null, image: null, preparingImage: null,
    createdAt: at(0), startedAt: at(0), endedAt: null, ...p } as Run;
}

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, s: number, runId: string | null = null): PersistedEvent {
  cursor++;
  return { eventId: `evt_d${cursor}`, cursor: 10_000 + cursor, eventType, occurredAt: at(s), organizationId: "org", projectId: "prj",
    taskId: TASK_ID, runId, sessionId: null, workflowRunId: null, actor: { type: "system", id: "workflow" }, source: "orchestrator",
    correlationId: null, causationId: null, payload };
}

/** The fixture task as a test sets it: its status, who decides, its Runs and its ledger. */
class DecisionsClient extends FixtureClient {
  talked = 0;
  deciders: Decider[] = [];
  constructor(private patch: Partial<TaskDetail>, private readonly extra: PersistedEvent[] = []) {
    super("d");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), ...this.patch } as TaskDetail;
  }
  override async getRun(id: string): Promise<RunDetail> {
    const r = this.patch.runs?.find((x) => x.id === id);
    return r ? ({ ...r, sessions: [] } as unknown as RunDetail) : super.getRun(id);
  }
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    if (params.runId) return params.runId === CONDUCTOR ? [] : super.ledgerFor(params);
    return [...super.ledgerFor(params), ...this.extra.filter((e) => e.cursor > (params.after ?? 0))];
  }
  override async talk(taskId: string): Promise<ChatSent> {
    this.talked++;
    this.patch = { ...this.patch, decider: "conductor", runs: [run({ id: CONDUCTOR, phase: null, role: "conductor", status: "running" })] };
    return { runId: CONDUCTOR, taskId, created: true, decider: "conductor" };
  }
  override async setDecider(taskId: string, decider: Decider, openPullRequest = false) {
    this.deciders.push(openPullRequest ? `${decider}+open` as Decider : decider);
    if (this.refuse && !(this.refuse.code === "pull_request_gate" && openPullRequest)) throw this.refuse;
    this.patch = { ...this.patch, decider };
    return { taskId, decider };
  }
  refuse: ApiError | null = null;
}

async function page(client: DecisionsClient, opened: string[] = []) {
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={(id) => opened.push(id)} onBack={() => {}} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle();
  return container;
}

const click = async (el: HTMLElement) => {
  await act(async () => el.click());
  await settle();
};
const selected = (p: HTMLElement) => p.querySelector("[role=tab][aria-selected=true]")?.textContent ?? "";

describe("starting a task", () => {
  test("Deliver and Talk it through, side by side and equal, in the header and on the Overview", async () => {
    const p = await page(new DecisionsClient({ status: "received", runs: [], decider: "policy" }));
    const talk = p.querySelector<HTMLButtonElement>("[data-testid=talk]")!;
    const deliver = p.querySelector<HTMLButtonElement>("[data-testid=deliver]")!;
    expect(talk.textContent).toBe("Talk it through");
    expect(deliver.textContent).toBe("Deliver");
    // Equal: the same variant, neither the primary one.
    expect(talk.getAttribute("data-variant")).toBe("secondary");
    expect(deliver.getAttribute("data-variant")).toBe("secondary");
    const cards = [...p.querySelectorAll("[data-testid=start-choice] [data-option]")].map((c) => c.getAttribute("data-option"));
    expect(cards).toEqual(["talk", "deliver"]);
  });

  test("Talk it through starts the conductor and opens Chat on it", async () => {
    const client = new DecisionsClient({ status: "received", runs: [], decider: "policy" });
    const p = await page(client);
    await click(p.querySelector<HTMLButtonElement>("[data-testid=talk]")!);
    expect(client.talked).toBe(1);
    await until(() => selected(p) === "Chat" ? true : null, "Chat");
    expect(p.querySelector("[data-testid=talk]")).toBeNull();
    expect(p.querySelector("[data-testid=deliver]")).toBeNull();
  });

  test("a task already started offers neither", async () => {
    const p = await page(new DecisionsClient({ status: "running", decider: "policy" }));
    expect(p.querySelector("[data-testid=talk]")).toBeNull();
    expect(p.querySelector("[data-testid=deliver]")).toBeNull();
  });
});

describe("a conducted task's Chat", () => {
  const runs = [
    run({ id: CONDUCTOR, phase: null, role: "conductor", status: "running", createdAt: at(0) }),
    run({ id: "run_impl", conductorRunId: CONDUCTOR, createdAt: at(10), startedAt: at(10), endedAt: at(70) }),
    run({ id: "run_rev", phase: "review", role: "reviewer", category: "correctness", status: "running", conductorRunId: CONDUCTOR, createdAt: at(80) }),
  ];
  const events = () => {
    cursor = 0;
    return [
      ev("conductor.decision_awaited", { point: "after_implement" }, 75),
      ev("pull_request.reviewed", { to: "approved", number: 88 }, 90),
      ev("pull_request.commented", { body: "nit" }, 91),
    ];
  };

  test("who decides, what it waits on, and Let Deliver finish it", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: { point: "after_review" } }, events());
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("The conductor decides · waiting on it: what to do with the review's findings");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    expect(client.deciders).toEqual(["policy"]);
    await until(() => p.querySelector("[data-testid=decider-line]") ? null : true, "the line gone once Deliver decides");
  });

  test("at the pull request gate, Deliver opens it only once the person confirms", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: { point: "before_pull_request" } }, events());
    client.refuse = new ApiError(409, "pull_request_gate", "Deliver will open the pull request now");
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    const open = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=hand-back-open]"), "the confirmation");
    expect(document.body.textContent).toContain("Deliver will open the pull request now.");
    expect(client.deciders).toEqual(["policy"]);
    await click(open);
    expect(client.deciders).toEqual(["policy", "policy+open"]);
    await until(() => p.querySelector("[data-testid=decider-line]") ? null : true, "Deliver decides");
  });

  test("a hand-back that fails says why beside the conductor's Chat", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs }, events());
    client.refuse = new ApiError(403, "forbidden", "only the task's owner decides");
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    const problem = await until(() => p.querySelector<HTMLElement>("[data-testid=chat-problem]"), "the problem");
    expect(problem.textContent).toBe("Could not hand it back: only the task's owner decides");
  });

  test("each Run it started is a line that opens its session; the decision waited on and an approval are dude's notices", async () => {
    const opened: string[] = [];
    const p = await page(new DecisionsClient({ status: "running", decider: "conductor", runs }, events()), opened);
    const lines = await until(() => {
      const l = [...p.querySelectorAll<HTMLElement>("[data-testid=chat-run]")];
      return l.length === 2 ? l : null;
    }, "two Run lines");
    expect(lines.map((l) => [l.getAttribute("data-run"), l.getAttribute("data-role"), l.getAttribute("data-status")])).toEqual([
      ["run_impl", "implementer", "completed"], ["run_rev", "reviewer", "running"]]);
    expect(lines[1]!.textContent).toContain("correctness");
    await click(lines[0]!.querySelector("button")!);
    expect(opened).toEqual(["run_impl"]);
    expect(p.querySelector("[data-testid=chat-decision]")?.textContent).toContain("Waiting on the conductor: what to do after the implementer.");
    const notices = [...p.querySelectorAll("[data-testid=chat-dude-notice]")].map((n) => n.textContent);
    // An approval is a notice; a comment is not one.
    expect(notices).toHaveLength(1);
    expect(notices[0]).toContain("Pull request #88 approved.");
  });

  test("Sessions: the conductor's Runs under it, Deliver's under Delivered automatically", async () => {
    const before = run({ id: "run_auto", createdAt: at(-100) });
    const p = await page(new DecisionsClient({ status: "running", decider: "conductor", runs: [...runs, before] }));
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Sessions"))!;
    await act(async () => {
      tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      tab.click();
    });
    await settle();
    const rows = [...p.querySelectorAll("[data-testid=sessions] li")].map((li) =>
      li.getAttribute("data-testid") === "delivered-automatically" ? "— Delivered automatically" : `${li.getAttribute("data-under") ?? "top"}`);
    expect(rows).toEqual(["top", "conductor", "conductor", "— Delivered automatically", "delivered"]);
  });
});

describe("what the Chat is made of", () => {
  test("a first message: planning on a task not started, taking over a delivery Deliver runs, read-only only where Chat is", async () => {
    const phase = run({ id: "run_p", status: "running" });
    const p = await page(new DecisionsClient({ status: "received", runs: [], decider: "policy" }));
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Chat"))!;
    await act(async () => {
      tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      tab.click();
    });
    await settle();
    const chat = await until(() => p.querySelector<HTMLElement>("[data-testid=task-chat]"), "Chat");
    expect(chat.textContent).toContain("starts planning this task with its conductor (Talk it through)");
    expect(chat.textContent).not.toContain("read-only");
    expect(firstMessage({ status: "running", decider: "policy", runs: [phase] }, true).readOnly).toBe(false);
    expect(firstMessage({ status: "running", decider: "policy", runs: [phase] }, true).empty).toContain("hands this delivery's decisions to its conductor");
    expect(firstMessage({ status: "running", decider: "policy", runs: [phase], handedBack: true }, true).readOnly).toBe(true);
    expect(firstMessage({ status: "done", decider: "policy", runs: [phase] }, false).readOnly).toBe(true);
    expect(firstMessage({ status: "aborted", decider: "policy", runs: [phase] }, false).readOnly).toBe(true);
    expect(firstMessage({ status: "review", decider: "policy", runs: [] }, false).readOnly).toBe(true);
  });

  test("nothing for a task its conductor never decided for", () => {
    expect(conductedLines({ decider: "policy", runs: [run({ id: "a" })] }, [ev("conductor.decision_awaited", { point: "start" }, 1)])).toEqual([]);
  });

  test("the history says the conductor's work is conducted", () => {
    const line = taskHistory({ status: "running", decider: "conductor", runs: [run({ id: "a", status: "running", conductorRunId: CONDUCTOR })] }, [], [], null, String);
    expect(line.lead).toBe("Conducting");
    expect(taskHistory({ status: "received", decider: "conductor", runs: [] }, [], [], null, String).lead).toBe("Planning with the conductor");
  });

  test("lines go before the first turn that came after them", () => {
    const turn = (id: string, s: number) => ({ kind: "message" as const, id, text: id, at: at(s), role: "conductor" }) as never;
    const out = interleaved([turn("t1", 1), turn("t2", 5)], [{ id: "l1", at: at(3), node: null }, { id: "l2", at: at(9), node: null }]);
    expect(out.map((o) => "group" in o ? (o.group as { id: string }).id : o.id)).toEqual(["t1", "l1", "t2", "l2"]);
  });
});
