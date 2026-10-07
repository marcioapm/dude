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
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
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
    // Confirmed, Deliver opens the pull request: the gate waits no more.
    this.patch = { ...this.patch, decider, ...(openPullRequest ? { awaitingDecision: null } : {}) };
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

  // Handed back while the conductor entered the gate, and its conductor
  // ended: Deliver holds the gate with no one live to ask the person. The
  // confirmation is still there, and only it opens the pull request.
  for (const conductor of ["completed", "running"] as const) {
    test(`a gate Deliver holds offers Let Deliver finish it, its conductor ${conductor}`, async () => {
      const held = [run({ id: CONDUCTOR, phase: null, role: "conductor", status: conductor, createdAt: at(0) }), runs[1]!];
      const client = new DecisionsClient({ status: "running", decider: "policy", handedBack: true, runs: held,
        awaitingDecision: { point: "before_pull_request" } }, events());
      client.refuse = new ApiError(409, "pull_request_gate", "Deliver will open the pull request now");
      const p = await page(client);
      const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "the gate's line");
      expect(line.textContent).toContain("Deliver decides · waiting on the person: whether to open the pull request");
      expect(p.querySelector("[data-testid=task-chat]")?.textContent).toContain("read-only");
      await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
      const open = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=hand-back-open]"), "the confirmation");
      expect(client.deciders).toEqual(["policy"]);
      await click(open);
      expect(client.deciders).toEqual(["policy", "policy+open"]);
      await until(() => p.querySelector("[data-testid=decider-line]") ? null : true, "the gate no longer held");
    });
  }

  test("Deliver deciding anywhere but a held gate shows no line", async () => {
    const p = await page(new DecisionsClient({ status: "running", decider: "policy", handedBack: true, runs,
      awaitingDecision: null }, events()));
    await until(() => p.querySelector("[data-testid=chat-screen]"), "the Chat");
    expect(p.querySelector("[data-testid=decider-line]")).toBeNull();
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

  // Deliver started it; the person took over, and the conductor steered it.
  test("a Run Deliver started and the conductor steered has its line in Chat, and stays Deliver's in Sessions", async () => {
    const auto = run({ id: "run_auto", status: "running", createdAt: at(-100) });
    const steer = { ...ev("run.steered", { directiveId: "dir_auto", text: "Use staging.", scope: "run", interrupt: false, by: "conductor",
      conductorRunId: CONDUCTOR }, 95, "run_auto"), actor: { type: "agent", id: CONDUCTOR } } as PersistedEvent;
    const p = await page(new DecisionsClient({ status: "running", decider: "conductor", runs: [run({ id: CONDUCTOR, phase: null,
      role: "conductor", status: "running", createdAt: at(0) }), auto] }, [steer]));
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=chat-run][data-run=run_auto]"), "the steered Run's line");
    expect(line.querySelector("[data-testid=conductor-steer]")?.getAttribute("data-directive")).toBe("dir_auto");
    expect(line.textContent).toContain("Use staging.");
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Sessions"))!;
    await act(async () => {
      tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      tab.click();
    });
    await settle();
    const rows = [...p.querySelectorAll("[data-testid=sessions] li")].map((li) =>
      li.getAttribute("data-testid") === "delivered-automatically" ? "— Delivered automatically" : `${li.getAttribute("data-under") ?? "top"}`);
    expect(rows).toEqual(["top", "— Delivered automatically", "delivered"]);
  });

  test("under Deliver, a Run the conductor steered still has its line", () => {
    const auto = run({ id: "run_auto", status: "running" });
    const steer = ev("run.steered", { directiveId: "dir_auto", text: "Use staging.", by: "conductor", conductorRunId: CONDUCTOR }, 5, "run_auto");
    const lines = conductedLines({ decider: "policy", runs: [auto] }, [steer]);
    expect(lines.map((l) => l.kind === "run" && [l.id, l.steers.map((s) => s.directiveId)])).toEqual([["run_auto", ["dir_auto"]]]);
  });
});

// A conducted task started over: the conductor is the task's, its Runs
// are each an attempt's. Attempt 1 had a conducted implementer and a
// Deliver reviewer; attempt 2 has a conducted implementer and fixer and a
// Deliver simplifier.
describe("a conducted task with two attempts", () => {
  const two = [
    run({ id: CONDUCTOR, attempt: 1, phase: null, role: "conductor", status: "running", createdAt: at(0) }),
    run({ id: "run_a1_impl", attempt: 1, status: "aborted", conductorRunId: CONDUCTOR, createdAt: at(10), endedAt: at(20) }),
    run({ id: "run_a1_rev", attempt: 1, phase: "review", role: "reviewer", category: "correctness", createdAt: at(15) }),
    run({ id: "run_a2_impl", attempt: 2, conductorRunId: CONDUCTOR, createdAt: at(30) }),
    run({ id: "run_a2_fix", attempt: 2, phase: "fix", status: "running", conductorRunId: CONDUCTOR, createdAt: at(40) }),
    run({ id: "run_a2_simp", attempt: 2, phase: "simplify", role: "simplifier", createdAt: at(35) }),
  ];
  const restart = () => {
    cursor = 0;
    return [ev("task.recovered", { action: "restart", attempt: 2 }, 25)];
  };
  async function showing(attempt: number | undefined, tab: string | undefined, opened: string[] = [], runId?: string, expect = attempt ?? 2) {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs: two }, restart());
    const { container, unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <PeopleProvider client={client}>
            <TaskScreen client={client} taskId={TASK_ID} tab={tab as never} attempt={attempt} runId={runId}
              onOpenRun={(id) => opened.push(id)} onBack={() => {}} />
          </PeopleProvider>
        </ToastProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    await until(() => container.querySelector(`[data-testid=task-screen][data-attempt="${expect}"]`), `attempt ${expect}`);
    await settle();
    return { p: container, client };
  }
  const tree = (p: HTMLElement) => [...p.querySelectorAll("[data-testid=sessions] li")].map((li) =>
    li.getAttribute("data-testid") === "delivered-automatically" ? "— Delivered automatically"
      : `${li.getAttribute("data-under") ?? "top"}:${li.getAttribute("data-run")}`);

  test("Sessions on the current attempt: the conductor, its Runs of this attempt under it, the attempt's others under Delivered automatically", async () => {
    const { p } = await showing(undefined, "sessions");
    const rows = await until(() => (tree(p).length > 0 ? tree(p) : null), "the sessions");
    expect(rows).toEqual([`top:${CONDUCTOR}`, "conductor:run_a2_fix", "conductor:run_a2_impl", "— Delivered automatically", "delivered:run_a2_simp"]);
  });

  test("Sessions on the earlier attempt: the conductor first, and under it only the Runs it started in that attempt", async () => {
    const { p } = await showing(1, "sessions");
    const rows = await until(() => (tree(p).length > 0 ? tree(p) : null), "the sessions");
    expect(rows).toEqual([`top:${CONDUCTOR}`, "conductor:run_a1_impl", "— Delivered automatically", "delivered:run_a1_rev"]);
  });

  const toTab = async (p: HTMLElement, name: string) => {
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
    await act(async () => void tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })));
    await settle();
  };

  test("Chat is the task's: a line for every Run the conductor started, whichever attempt is shown, each opening its session", async () => {
    for (const attempt of [undefined, 1]) {
      const opened: string[] = [];
      // Chat's URL names no attempt: attempt 1 is picked on Findings, and kept on Chat.
      const { p } = await showing(attempt, "findings", opened);
      await toTab(p, "Chat");
      expect(p.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt")).toBe(String(attempt ?? 2));
      const lines = await until(() => {
        const l = [...p.querySelectorAll<HTMLElement>("[data-testid=chat-run]")];
        return l.length === 3 ? l : null;
      }, `three Run lines, attempt ${attempt ?? "current"} shown`);
      expect(lines.map((l) => l.getAttribute("data-run"))).toEqual(["run_a1_impl", "run_a2_impl", "run_a2_fix"]);
      await click(lines[0]!.querySelector("button")!);
      await click(lines[2]!.querySelector("button")!);
      expect(opened).toEqual(["run_a1_impl", "run_a2_fix"]);
      for (const unmount of mounted) await unmount();
      mounted = [];
    }
  });

  test("a session opened from a Chat line opens on its Run's own attempt", async () => {
    // The URL a line opens names the session alone: attempt 1 is the Run's, not the current one.
    await showing(undefined, undefined, [], "run_a1_impl", 1);
  });

  test("an earlier attempt offers no way to start or hand back: Chat on it has no Let Deliver finish it", async () => {
    const { p } = await showing(1, "findings");
    expect(p.querySelector("[data-testid=talk]")).toBeNull();
    expect(p.querySelector("[data-testid=deliver]")).toBeNull();
    expect(p.querySelector("[data-testid=start-choice]")).toBeNull();
    // Chat is the task's: opened with attempt 1 picked, attempt 1 stays shown, set aside.
    await toTab(p, "Chat");
    await until(() => p.querySelector("[data-testid=task-chat]"), "Chat");
    expect(p.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt")).toBe("1");
    expect(p.querySelector("[data-testid=earlier-bar]")).not.toBeNull();
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("The conductor decides");
    expect(p.querySelector("[data-testid=let-deliver-finish]")).toBeNull();
  });

  test("on the current attempt, Chat keeps Let Deliver finish it", async () => {
    const { p } = await showing(undefined, "chat");
    await until(() => p.querySelector("[data-testid=let-deliver-finish]"), "Let Deliver finish it");
  });

  // A new attempt queued (POST /v1/tasks/:id/runs) and not started: its
  // start choice is offered, and attempt 1, shown, is only to read.
  for (const [attempt, offered] of [[undefined, true], [1, false]] as const) {
    test(`a new attempt not started: the start choice ${offered ? "on it" : "not on attempt 1"}`, async () => {
      const queued = [
        run({ id: "run_q1_impl", attempt: 1, status: "aborted", createdAt: at(10), endedAt: at(20) }),
        run({ id: "run_q2", attempt: 2, phase: null, status: "pending", startedAt: null, createdAt: at(30) }),
      ];
      const client = new DecisionsClient({ status: "queued", decider: "policy", runs: queued });
      const { container, unmount } = await mount(
        <TooltipProvider>
          <ToastProvider>
            <PeopleProvider client={client}>
              <TaskScreen client={client} taskId={TASK_ID} tab={attempt ? "findings" : undefined} attempt={attempt} onOpenRun={() => {}} onBack={() => {}} />
            </PeopleProvider>
          </ToastProvider>
        </TooltipProvider>,
      );
      mounted.push(unmount);
      await until(() => container.querySelector(`[data-testid=task-screen][data-attempt="${attempt ?? 2}"]`), `attempt ${attempt ?? 2}`);
      await settle();
      const there = (sel: string) => container.querySelector(sel) !== null;
      expect([there("[data-testid=talk]"), there("[data-testid=deliver]")]).toEqual([offered, offered]);
      if (offered) {
        expect(container.querySelector("[data-testid=talk]")!.getAttribute("data-variant")).toBe("secondary");
        expect(container.querySelector("[data-testid=deliver]")!.getAttribute("data-variant")).toBe("secondary");
      } else {
        await toTab(container, "Overview");
        expect(container.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt")).toBe("1");
        expect(there("[data-testid=start-choice]")).toBe(false);
        expect(there("[data-testid=talk]")).toBe(false);
      }
    });
  }
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

  test("ready to merge, and no longer, are dude's notices in its own words, once each", () => {
    const lines = conductedLines({ decider: "conductor", runs: [] }, [
      ev("task.ready_to_merge", { pullRequests: 1 }, 1),
      ev("chat.notice", { text: "greeter#3 is ready to merge: approved, checks green. Merging is yours.", about: "ready_to_merge" }, 1),
      ev("chat.notice", { text: "greeter#3 is no longer ready to merge: checks are failing.", about: "no_longer_ready" }, 2),
    ]);
    expect(lines.map((l) => l.kind === "notice" ? l.text : l.kind)).toEqual([
      "greeter#3 is ready to merge: approved, checks green. Merging is yours.",
      "greeter#3 is no longer ready to merge: checks are failing.",
    ]);
  });

  test("a commit the conductor published is its line, with its short sha and files; a phase's is not", () => {
    const sha = "0123456789abcdef0123456789abcdef01234567";
    const lines = conductedLines({ decider: "conductor", runs: [] }, [
      ev("git.commit_created", { repo: "greeter", headSha: sha, changedPaths: ["README.md", "src/a.ts"], by: "conductor" }, 1, CONDUCTOR),
      ev("git.commit_created", { repo: "greeter", headSha: "fedcba9876", changedPaths: ["x.ts"] }, 2, "run_impl"),
    ]);
    expect(lines).toHaveLength(1);
    const [line] = lines;
    expect(line!.kind).toBe("commit");
    expect(line!.kind === "commit" && [line.sha, line.paths, line.text]).toEqual(["0123456",
      ["README.md", "src/a.ts"], "The conductor published greeter@0123456: README.md, src/a.ts."]);
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
