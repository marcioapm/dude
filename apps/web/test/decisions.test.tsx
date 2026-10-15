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
import { act, mount, sessionOptions, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { conductedLines } from "../src/conducted.ts";
import { taskHistory } from "../src/taskHistory.ts";
import { asides, interleaved } from "../src/screens/RunScreen.tsx";
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
  // A decider as handed back, "+open" when the pull request is to be opened too.
  deciders: Array<Decider | `${Decider}+open`> = [];
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
  delivered = 0;
  override async deliver(_taskId: string) {
    this.delivered++;
    return { workflowRunId: "wf_dec", alreadyRunning: false };
  }
  override async setDecider(taskId: string, decider: Decider, openPullRequest = false) {
    this.deciders.push(openPullRequest ? `${decider}+open` : decider);
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
  const toTab = async (p: HTMLElement, name: string) => {
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
    await act(async () => void tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 })));
    await settle();
  };
  const reachable = (b: HTMLButtonElement | null) => b !== null && b.tagName === "BUTTON" && !b.disabled && b.tabIndex >= 0;

  test("on the Overview, Talk it through and Deliver are its two equal cards, and the header does not repeat them", async () => {
    const p = await page(new DecisionsClient({ status: "received", runs: [], decider: "policy" }));
    expect(selected(p)).toBe("Overview");
    const cards = [...p.querySelectorAll("[data-testid=start-choice] [data-option]")].map((c) => c.getAttribute("data-option"));
    expect(cards).toEqual(["talk", "deliver"]);
    const talk = p.querySelector<HTMLButtonElement>("[data-testid=start-talk]");
    const deliver = p.querySelector<HTMLButtonElement>("[data-testid=start-deliver]");
    expect([talk?.textContent, deliver?.textContent]).toEqual(["Talk it through", "Deliver"]);
    expect([reachable(talk), reachable(deliver)]).toEqual([true, true]);
    expect(p.querySelectorAll("[data-testid=talk]").length).toBe(0);
    expect(p.querySelectorAll("[data-testid=deliver]").length).toBe(0);
  });

  test("on another tab the header offers both, equal and reachable by keyboard; back on Overview it leaves them to the cards", async () => {
    const client = new DecisionsClient({ status: "received", runs: [], decider: "policy" });
    const p = await page(client);
    await toTab(p, "Findings");
    expect(selected(p)).toStartWith("Findings");
    const talk = p.querySelector<HTMLButtonElement>("[data-testid=talk]");
    const deliver = p.querySelector<HTMLButtonElement>("[data-testid=deliver]");
    expect([talk?.textContent, deliver?.textContent]).toEqual(["Talk it through", "Deliver"]);
    // Equal: the same variant, neither the primary one.
    expect([talk!.getAttribute("data-variant"), deliver!.getAttribute("data-variant")]).toEqual(["secondary", "secondary"]);
    expect([reachable(talk), reachable(deliver)]).toEqual([true, true]);
    expect(p.querySelectorAll("[data-testid=start-choice]").length).toBe(0);
    await click(deliver!);
    expect(client.delivered).toBe(1);

    await toTab(p, "Overview");
    expect(p.querySelectorAll("[data-testid=talk]").length).toBe(0);
    expect(p.querySelector("[data-testid=start-talk]")).not.toBeNull();
  });

  test("Talk it through starts the conductor and opens Chat on it", async () => {
    const client = new DecisionsClient({ status: "received", runs: [], decider: "policy" });
    const p = await page(client);
    await click(p.querySelector<HTMLButtonElement>("[data-testid=start-talk]")!);
    expect(client.talked).toBe(1);
    await until(() => selected(p) === "Chat" ? true : null, "Chat");
    expect(p.querySelectorAll("[data-testid=talk]").length).toBe(0);
    expect(p.querySelectorAll("[data-testid=deliver]").length).toBe(0);
  });

  test("from the header on another tab, Talk it through starts the conductor and opens Chat on it", async () => {
    const client = new DecisionsClient({ status: "received", runs: [], decider: "policy" });
    const p = await page(client);
    await toTab(p, "Findings");
    await click(p.querySelector<HTMLButtonElement>("[data-testid=talk]")!);
    expect(client.talked).toBe(1);
    await until(() => selected(p) === "Chat" ? true : null, "Chat");
  });

  test("a task already started offers neither, on the Overview or in the header", async () => {
    const p = await page(new DecisionsClient({ status: "running", decider: "policy" }));
    expect(p.querySelectorAll("[data-testid=start-choice]").length).toBe(0);
    await toTab(p, "Findings");
    expect(selected(p)).toStartWith("Findings");
    expect(p.querySelectorAll("[data-testid=talk]").length).toBe(0);
    expect(p.querySelectorAll("[data-testid=deliver]").length).toBe(0);
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

  const decidesIn = (p: HTMLElement, decider: string) =>
    until(() => p.querySelector(`[data-testid=decider-line][data-decider=${decider}]`) &&
      p.querySelector(`[data-testid=task-header] [data-testid=driver][data-driver=${decider === "policy" ? "deliver-chat" : "conductor"}]`) ? true : null,
    `the header and Chat both on ${decider}`);

  test("who decides, what it waits on, and Let Deliver finish it: the header and Chat agree after", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: { point: "after_review" } }, events());
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("The conductor decides · waiting on it: what to do with the review's findings");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    expect(client.deciders).toEqual(["policy"]);
    await decidesIn(p, "policy");
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
    // Refused: nothing changed on the page.
    expect(p.querySelector("[data-testid=task-header] [data-testid=driver]")?.getAttribute("data-driver")).toBe("conductor");
    await click(open);
    expect(client.deciders).toEqual(["policy", "policy+open"]);
    await decidesIn(p, "policy");
  });

  test("at the gate, keeping deciding in Chat asks nothing more and changes nothing", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: { point: "before_pull_request" } }, events());
    client.refuse = new ApiError(409, "pull_request_gate", "Deliver will open the pull request now");
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    await click(await until(() => document.querySelector<HTMLButtonElement>("[data-testid=hand-back-keep]"), "Keep deciding in Chat"));
    await until(() => document.querySelectorAll("[data-testid=hand-back-open]").length === 0 ? true : null, "the confirmation closed");
    expect(client.deciders).toEqual(["policy"]);
    expect(p.querySelector("[data-testid=decider-line]")?.getAttribute("data-decider")).toBe("conductor");
    expect(p.querySelector("[data-testid=task-header] [data-testid=driver]")?.getAttribute("data-driver")).toBe("conductor");
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
      expect(p.querySelector("[data-testid=composer-to]")?.textContent).toBe("To Conductor · answers only, Deliver decides");
      await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
      const open = await until(() => document.querySelector<HTMLButtonElement>("[data-testid=hand-back-open]"), "the confirmation");
      expect(client.deciders).toEqual(["policy"]);
      await click(open);
      expect(client.deciders).toEqual(["policy", "policy+open"]);
      await until(() => p.querySelector("[data-testid=decider-line]")?.textContent?.includes("waiting on") ? null : true, "the gate no longer held");
    });
  }

  test("handed back, Chat says messages are answered and take nothing back, and offers the decisions to the conductor", async () => {
    const client = new DecisionsClient({ status: "running", decider: "policy", handedBack: true, runs, awaitingDecision: null }, events());
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("Deliver decides · messages here are answered, and take no decisions back");
    expect(p.querySelector("[data-testid=composer-to]")?.textContent).toBe("To Conductor · answers only, Deliver decides");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=give-to-conductor]")!);
    expect(client.deciders).toEqual(["conductor"]);
    await decidesIn(p, "conductor");
    expect(p.querySelector("[data-testid=composer-to]")?.textContent).toBe("To Conductor");
  });

  test("the header's switch and Chat's are one: switching from the header moves Chat too", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: null }, events());
    const p = await page(client);
    const chip = await until(() => p.querySelector<HTMLElement>("[data-testid=task-header] [data-testid=driver]"), "the chip");
    expect(chip.getAttribute("data-switchable")).toBe("true");
    await act(async () => void chip.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    const deliver = await until(() => document.querySelector<HTMLElement>("[role=menuitemradio][data-testid=rowmenu-policy]"), "Let Deliver finish it");
    expect(deliver.textContent).toContain("Let Deliver finish it");
    await click(deliver);
    expect(client.deciders).toEqual(["policy"]);
    await decidesIn(p, "policy");
  });

  test("a switch that fails leaves both as they were and says why", async () => {
    const client = new DecisionsClient({ status: "running", decider: "policy", runs, awaitingDecision: null }, events());
    client.refuse = new ApiError(409, "conflict", "task has no delivery in progress");
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("a message from you gives it the decisions");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=give-to-conductor]")!);
    const problem = await until(() => p.querySelector<HTMLElement>("[data-testid=driver-problem]"), "the problem");
    expect(problem.textContent).toBe("Could not change who decides: task has no delivery in progress");
    expect(client.deciders).toEqual(["conductor"]);
    expect(p.querySelector("[data-testid=decider-line]")?.getAttribute("data-decider")).toBe("policy");
    expect(p.querySelector("[data-testid=task-header] [data-testid=driver]")?.getAttribute("data-driver")).toBe("deliver-chat");
  });

  test("someone else's task: the same chip, no switch, and Chat offers no switch either", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs, awaitingDecision: null,
      owner: { id: "u_ana", name: "Ana Ribeiro" } as TaskDetail["owner"] }, events());
    const p = await page(client);
    const chip = await until(() => p.querySelector<HTMLElement>("[data-testid=task-header] [data-testid=driver]"), "the chip");
    expect(chip.getAttribute("data-switchable")).toBe("false");
    expect(chip.getAttribute("aria-label")).toContain("Only the owner can change who decides. Ana owns this task.");
    await until(() => p.querySelector("[data-testid=decider-line]"), "who decides");
    expect(p.querySelectorAll("[data-testid=let-deliver-finish], [data-testid=give-to-conductor]").length).toBe(0);
  });

  test("a hand-back that fails says why", async () => {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs }, events());
    client.refuse = new ApiError(403, "forbidden", "only the task's owner decides");
    const p = await page(client);
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    await click(line.querySelector<HTMLButtonElement>("[data-testid=let-deliver-finish]")!);
    const problem = await until(() => p.querySelector<HTMLElement>("[data-testid=driver-problem]"), "the problem");
    expect(problem.textContent).toBe("Could not change who decides: only the task's owner decides");
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

  // Opens Sessions and its selector: each option's place in the tree, and the Delivered automatically heading.
  async function sessionTree(p: HTMLElement) {
    const tab = [...p.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Sessions"))!;
    await act(async () => {
      tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
      tab.click();
    });
    await sessionOptions(p);
    await settle();
    return [...document.querySelectorAll('[data-testid=sessions] [role="option"], [data-testid=sessions] [role="presentation"]')].filter((li) => li.getAttribute("role") === "option" || li.textContent === "Delivered automatically").map((li) =>
      li.getAttribute("role") === "presentation" ? "— Delivered automatically" : `${li.getAttribute("data-under") ?? "top"}`);
  }

  test("Sessions: the conductor's Runs under it, Deliver's under Delivered automatically", async () => {
    const before = run({ id: "run_auto", createdAt: at(-100) });
    const p = await page(new DecisionsClient({ status: "running", decider: "conductor", runs: [...runs, before] }));
    expect(await sessionTree(p)).toEqual(["top", "conductor", "conductor", "— Delivered automatically", "delivered"]);
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
    expect(await sessionTree(p)).toEqual(["top", "— Delivered automatically", "delivered"]);
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
  async function showing(attempt: number | undefined, tab: string | undefined, opened: string[] = [], runId?: string, expect = attempt ?? 2,
    deciders?: TaskDetail["deciders"]) {
    const client = new DecisionsClient({ status: "running", decider: "conductor", runs: two, ...(deciders ? { deciders } : {}) }, restart());
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
    if (tab === "sessions") { await sessionOptions(container); await settle(); }
    return { p: container, client };
  }
  const tree = (_p: HTMLElement) => [...document.querySelectorAll('[data-testid=sessions] li')].filter((li) => li.getAttribute("role") === "option" || li.textContent === "Delivered automatically").map((li) =>
    li.getAttribute("role") === "presentation" ? "— Delivered automatically"
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
    expect(p.querySelectorAll("[data-testid=talk]").length).toBe(0);
    expect(p.querySelectorAll("[data-testid=deliver]").length).toBe(0);
    expect(p.querySelectorAll("[data-testid=start-choice]").length).toBe(0);
    // Chat is the task's: opened with attempt 1 picked, attempt 1 stays shown, set aside.
    await toTab(p, "Chat");
    await until(() => p.querySelector("[data-testid=task-chat]"), "Chat");
    expect(p.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt")).toBe("1");
    expect(p.querySelector("[data-testid=earlier-bar]")).not.toBeNull();
    const line = await until(() => p.querySelector<HTMLElement>("[data-testid=decider-line]"), "who decides");
    expect(line.textContent).toContain("The conductor decides");
    expect(p.querySelectorAll("[data-testid=let-deliver-finish]").length).toBe(0);
  });

  test("on the current attempt, Chat keeps Let Deliver finish it", async () => {
    const { p } = await showing(undefined, "chat");
    await until(() => p.querySelector("[data-testid=let-deliver-finish]"), "Let Deliver finish it");
  });

  test("an earlier attempt's header says who decided it last, read-only, never the current attempt's conductor", async () => {
    const header = (p: HTMLElement) => p.querySelector<HTMLElement>("[data-testid=task-header] [data-testid=driver]");
    // Attempt 1 was Deliver's; the current one's conductor is live and decides.
    const known = await showing(1, "findings", [], undefined, 1, [{ attempt: 1, decider: "policy" }, { attempt: 2, decider: "conductor" }]);
    const chip = await until(() => header(known.p), "attempt 1's driver");
    expect(chip.textContent).toBe("Decided by Deliver");
    expect(chip.hasAttribute("data-ended")).toBe(true);
    expect(known.p.querySelectorAll("[data-testid=task-header] button[data-testid=driver]").length).toBe(0);
    for (const unmount of mounted) await unmount();
    mounted = [];
    // Unknown for attempt 1: nothing is said rather than something made up.
    const unknown = await showing(1, "findings", [], undefined, 1, [{ attempt: 2, decider: "conductor" }]);
    await settle();
    expect(unknown.p.querySelectorAll("[data-testid=task-header] [data-testid=driver]").length).toBe(0);
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
      if (offered) {
        // Overview: the cards; any other tab: the header's two.
        expect([there("[data-testid=start-talk]"), there("[data-testid=start-deliver]"), there("[data-testid=talk]")]).toEqual([true, true, false]);
        await toTab(container, "Findings");
        expect(container.querySelector("[data-testid=talk]")!.getAttribute("data-variant")).toBe("secondary");
        expect(container.querySelector("[data-testid=deliver]")!.getAttribute("data-variant")).toBe("secondary");
      } else {
        expect([there("[data-testid=talk]"), there("[data-testid=deliver]")]).toEqual([false, false]);
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
    const line = lines[0]!;
    expect(line.kind).toBe("commit");
    expect(line.kind === "commit" && [line.sha, line.paths, line.text]).toEqual(["0123456",
      ["README.md", "src/a.ts"], "The conductor published greeter@0123456: README.md, src/a.ts."]);
  });

  test("the history says the conductor's work is conducted", () => {
    const line = taskHistory({ status: "running", decider: "conductor", runs: [run({ id: "a", status: "running", conductorRunId: CONDUCTOR })] }, [], [], null, String);
    expect(line.lead).toBe("Conducting");
    expect(taskHistory({ status: "received", decider: "conductor", runs: [] }, [], [], null, String).lead).toBe("Planning with the conductor");
  });

  test("what an agent records between its tool calls stays in their aside, on the message column", () => {
    const t = (kind: string, id: string) => ({ kind, id, at: at(1), startedAt: at(1) }) as never;
    const out = asides([t("message", "m1"), t("tool", "a"), t("thought", "b"), t("event", "c"), t("progress", "d"),
      t("tool", "e"), t("message", "m2"), t("event", "f")]);
    const ids = (g: unknown) => Array.isArray(g) ? g.map((x: { id: string }) => x.id) : (g as { id: string }).id;
    expect(out.map(ids)).toEqual(["m1", ["a", "b", "c", "d", "e"], "m2", ["f"]]);
  });

  test("lines go before the first turn that came after them", () => {
    const turn = (id: string, s: number) => ({ kind: "message" as const, id, text: id, at: at(s), role: "conductor" }) as never;
    const out = interleaved([turn("t1", 1), turn("t2", 5)], [{ id: "l1", at: at(3), node: null }, { id: "l2", at: at(9), node: null }]);
    expect(out.map((o) => "group" in o ? (o.group as { id: string }).id : o.id)).toEqual(["t1", "l1", "t2", "l2"]);
  });

  test("a line and a turn in different zones are merged by instant, not by their text", () => {
    const turn = (id: string, at: string) => ({ kind: "message" as const, id, text: id, at, role: "conductor" }) as never;
    // 10:00:03Z written as Lisbon summer time: after "10:00:05Z" as text, before it in time.
    const out = interleaved([turn("t1", at(1)), turn("t2", at(5))], [{ id: "l1", at: "2026-10-03T11:00:03+01:00", node: null }]);
    expect(out.map((o) => "group" in o ? (o.group as { id: string }).id : o.id)).toEqual(["t1", "l1", "t2"]);
  });

  test("a Run's line and an event's are ordered by instant across zones", () => {
    const started = run({ id: "run_lisbon", conductorRunId: CONDUCTOR, createdAt: "2026-10-03T11:00:02+01:00" });
    const lines = conductedLines({ decider: "conductor", runs: [started] }, [ev("chat.notice", { text: "later" }, 4)]);
    expect(lines.map((l) => l.id)).toEqual(["run_lisbon", expect.stringMatching(/^evt_d/)]);
  });
});
