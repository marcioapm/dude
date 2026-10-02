/**
 * A stopped task, picked back up: the notice that says how it stopped and
 * offers the ways back, the one dialog they open, and — once it has been
 * started over — the page showing one attempt at a time, picked in its
 * header. Mounted in happy-dom against the fixture client, its task
 * stopped (or started over) by `dude.fixtures.run`.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient, emit } from "../src/fixtures/client.ts";
import { ORG, ORPHAN_FINDING, PROJECT, RESTART, RESTARTED_RUNS, RUN_ID, TASK_ID, YOU, taskFor } from "../src/fixtures/data.ts";
import { stopOf } from "../src/screens/Recovery.tsx";
import type { RecoverAction } from "../src/api/client.ts";
import type { PersistedEvent } from "@dude/domain";
import { App } from "../src/App.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function taskPage(client: FixtureClient, props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

const stoppedAs = (as: "aborted" | "failed" | "restarted") => {
  localStorage.setItem("dude.fixtures.run", as);
  return new FixtureClient("a");
};

// Radix's tabs switch on mousedown, as a person's click starts.
const openTab = (page: HTMLElement, name: string) => act(async () => {
  const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))!;
  tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
});

describe("a stopped task", () => {
  test("says who stopped what and why, what it left, and offers the three ways back", async () => {
    const page = await taskPage(stoppedAs("aborted"));
    const notice = await until(() => page.querySelector<HTMLElement>("[data-testid=stopped]"), "the stopped notice");
    await until(() => page.querySelector("[data-testid=recover-resume]"), "the ways back");
    expect(notice.textContent).toContain("Ana Ribeiro aborted the implement");
    expect(notice.textContent).toContain("rewriting the checkout's routing");
    expect(notice.textContent).toContain("3f2a9c1");
    expect(notice.textContent).toContain("kept until");
    const buttons = [...notice.querySelectorAll("[data-testid^=recover-]")].map((b) => b.textContent);
    expect(buttons).toEqual(["Resume…", "Try again…", "Start over…"]);
  });

  test("a failure says so in its own words", async () => {
    const page = await taskPage(stoppedAs("failed"));
    const notice = await until(() => page.querySelector<HTMLElement>("[data-testid=stopped]"), "the stopped notice");
    expect(notice.textContent).toContain("The implement failed");
    expect(notice.textContent).toContain("host lost");
  });

  test("each way opens the one dialog on it, and what it keeps follows the choice", async () => {
    const client = stoppedAs("aborted");
    const sent: Array<[RecoverAction, string]> = [];
    const recover = client.recover.bind(client);
    client.recover = (id, action, note) => {
      sent.push([action, note]);
      return recover(id, action, note);
    };
    const page = await taskPage(client);
    await click(await until(() => page.querySelector("[data-testid=recover-retry]"), "Try again…"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
    const radios = () => [...dialog.querySelectorAll<HTMLElement>("[role=radio]")];
    expect(radios().map((r) => r.getAttribute("aria-checked"))).toEqual(["false", "true", "false"]);
    expect(dialog.textContent).toContain("not carried over");
    // Resume, in the same dialog: what it keeps changes with it.
    await click(radios()[0]!);
    expect(dialog.textContent).toContain("still in its context");
    const note = dialog.querySelector<HTMLTextAreaElement>("textarea")!;
    const { act } = await import("react");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      set.call(note, "Keep the routing as it is.");
      note.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(dialog.querySelector("[data-testid=recover-confirm]")!);
    await until(() => !document.querySelector("[role=dialog]"), "the dialog to close");
    expect(sent).toEqual([["resume", "Keep the routing as it is."]]);
  });

  test("anyone but its owner is told whom it waits on, and offered nothing", async () => {
    const client = stoppedAs("aborted");
    const listPeople = client.listPeople.bind(client);
    client.listPeople = async () => ({ ...(await listPeople()), you: "u_ana" });
    const page = await taskPage(client);
    const waiting = await until(() => page.querySelector("[data-testid=recover-waiting]"), "whom it waits on");
    expect(waiting.textContent).toContain("Only Márcio Martins, its owner, can pick it back up");
    expect(page.querySelector("[data-testid=recover-resume]")).toBeNull();
  });

  test("its session's end strip offers Resume… and the other ways", async () => {
    const page = await taskPage(stoppedAs("aborted"), { runId: RUN_ID });
    const strip = await until(() => page.querySelector<HTMLElement>("[data-testid=run-ended]"), "the end strip");
    await until(() => strip.querySelector("[data-testid=run-ended-resume]"), "Resume… on the strip");
    expect(strip.textContent).toContain("Kept until");
    await click(strip.querySelector("[data-testid=run-ended-other]")!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
    expect(dialog.querySelector("[role=radio][aria-checked=true]")?.textContent).toContain("Try again");
  });
});

describe("a task started over", () => {
  const restarted = () => stoppedAs("restarted");
  const press = (el: Element) =>
    act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  const picker = (page: HTMLElement) => page.querySelector<HTMLElement>("[data-testid=attempt-picker]");
  const shown = (page: HTMLElement) => page.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt");
  const tabCount = (page: HTMLElement, name: string) =>
    [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith(name))?.textContent?.slice(name.length) ?? "";
  const texts = (page: HTMLElement, sel: string) => [...page.querySelectorAll<HTMLElement>(sel)].map((e) => e.textContent ?? "");
  // How many match: `toBeNull()` on a happy-dom element can pass, a count cannot.
  const count = (root: ParentNode, sel: string) => root.querySelectorAll(sel).length;
  /** Opens the picker and picks attempt `n` as a person would. */
  async function pick(page: HTMLElement, n: number) {
    await press(await until(() => picker(page), "the attempt picker"));
    const option = await until(() => document.querySelector<HTMLElement>(`[role=option][data-value="${n}"]`), `attempt ${n} in the picker`);
    await click(option);
    await until(() => (shown(page) === String(n) ? true : null), `attempt ${n} shown`);
  }
  /** The page on attempt 1, through the picker. */
  async function onAttempt1(client = restarted(), props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
    const page = await taskPage(client, props);
    await pick(page, 1);
    return page;
  }
  const pipelineHead = (page: HTMLElement) => [...page.querySelectorAll("h2")].find((h) => h.textContent?.startsWith("Pipeline"))?.textContent;
  /** The Activity line that says `text`, once it is there. */
  const activityLine = (page: HTMLElement, text: string) =>
    until(() => [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")].find((l) => l.textContent?.includes(text)), text);
  /** What the page asked the URL to say: [tab, attempt, replace], in order. */
  function navigations() {
    const said: Array<[string | undefined, number | undefined, boolean]> = [];
    return { said, onNavigate: (tab: string | undefined, attempt: number | undefined, replace: boolean) => void said.push([tab, attempt, replace]) };
  }

  test("a task with one attempt has no picker, no bar, and no attempt anywhere", async () => {
    const page = await taskPage(new FixtureClient("d"));
    await until(() => page.querySelector("[data-testid=pipeline]"), "the pipeline");
    await settle();
    expect(count(page, "[data-testid=attempt-picker]")).toBe(0);
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    expect(page.textContent).not.toContain("attempt");
    expect(page.textContent).not.toContain("Attempt");
  });

  test("a task with one attempt keeps the Activity it had: no findings raised, no files saved, no ways there", async () => {
    const client = new FixtureClient("d");
    const events = client.events.bind(client);
    client.events = async (params) => {
      const r = await events(params);
      if (r.events.length === 0) return r;
      const like = r.events.find((e) => e.runId === "run_d2")!;
      const extra = [
        { ...like, eventId: "evt_d_review", cursor: 30_000, eventType: "review.completed", payload: { phase: "review", count: 4 } },
        { ...like, eventId: "evt_d_file", cursor: 30_001, eventType: "artifact.created", payload: { artifactId: "art_d", name: "notes/vat.md" } },
      ];
      return { events: [...r.events, ...extra], nextCursor: 30_001 };
    };
    const page = await taskPage(client, { tab: "activity" });
    await until(() => (count(page, "[data-testid=activity-item]") > 0 ? true : null), "Activity's lines");
    // The ledger's acts are there; the lines and ways only a started-over task has are not.
    expect(texts(page, "[data-testid=activity-item]").join()).toContain("created the task");
    expect(texts(page, "[data-testid=activity-item]").filter((t) => t.includes("raised") || t.includes("saved"))).toHaveLength(0);
    expect(count(page, "[data-testid=activity-open]")).toBe(0);
  });

  test("Activity is not worked out again for what an agent says as it works", async () => {
    const client = restarted();
    // Counts each pass over the ledger: the pass reads the last event's type.
    let passes = 0;
    const events = client.events.bind(client);
    client.events = async (params) => {
      const r = await events(params);
      const last = r.events.at(-1);
      if (!last) return r;
      const counted = { ...last };
      Object.defineProperty(counted, "eventType", { enumerable: true, get: () => (passes++, last.eventType) });
      return { ...r, events: [...r.events.slice(0, -1), counted] };
    };
    const page = await taskPage(client, { tab: "activity" });
    await until(() => (count(page, "[data-testid=activity-item]") > 0 ? true : null), "Activity's lines");
    await settle(300);
    const before = passes;
    for (let i = 0; i < 10; i++) {
      await act(async () => emit({ eventType: "agent.message", occurredAt: new Date().toISOString(), organizationId: ORG.id, projectId: PROJECT.id,
        taskId: TASK_ID, runId: "run_a2_fix", sessionId: null, workflowRunId: null, actor: { type: "agent", id: "run_a2_fix" }, source: "runner",
        correlationId: null, causationId: null, payload: { text: "Still on it." } } as never));
    }
    await settle(300);
    expect(passes - before).toBe(0);
  });

  test("the picker opens on the current attempt, and its options say how each went", async () => {
    const page = await taskPage(restarted());
    const trigger = await until(() => picker(page), "the attempt picker");
    expect(shown(page)).toBe("2");
    // The status mark is icon-only: its word is for a screen reader.
    expect(trigger.textContent).toBe("RunningAttempt 2current");
    // The page is attempt 2's: its branch, its pull request, its pipeline.
    expect(page.querySelector("[data-testid=branch]")?.textContent).toBe("dude/task_wc214/attempt-2");
    expect(texts(page, "[data-testid=pr-link]").join()).toContain("483");
    expect(pipelineHead(page)).toBe("Pipeline · attempt 2");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    await press(trigger);
    const options = await until(() => {
      const o = [...document.querySelectorAll<HTMLElement>("[role=option]")];
      return o.length === 2 ? o : null;
    }, "two attempts in the picker");
    // Newest first.
    expect(options.map((o) => o.getAttribute("data-value"))).toEqual(["2", "1"]);
    expect(options[0]!.textContent).toContain("Started");
    expect(options[0]!.textContent).toContain("PR #483 open");
    expect(options[0]!.textContent).not.toContain("#478");
    expect(options[1]!.textContent).toContain("Set aside");
    expect(options[1]!.textContent).toContain("stopped at Fix, aborted by Ana");
    expect(options[1]!.textContent).toContain("dude/task_wc214/attempt-1");
    expect(options[1]!.textContent).toContain("PR #478 closed");
    expect(options[1]!.textContent).not.toContain("#483");
    expect(options[1]!.textContent).toContain("$13.40");
    expect(document.body.textContent).toContain("Activity always shows every attempt.");
  });

  test("attempt 1 shows its branch, pull request, pipeline, findings, sessions and files, and its counts", async () => {
    const page = await taskPage(restarted());
    await until(() => picker(page), "the attempt picker");
    // Attempt 2's counts first: no findings, its one file, four sessions.
    expect(tabCount(page, "Findings")).toBe("");
    expect(tabCount(page, "Sessions")).toBe("4");
    expect(tabCount(page, "Files")).toBe("1");
    await pick(page, 1);
    expect(page.querySelector("[data-testid=branch]")?.textContent).toBe("dude/task_wc214/attempt-1");
    expect(texts(page, "[data-testid=pr-link]")).toHaveLength(1);
    expect(texts(page, "[data-testid=pr-link]")[0]).toContain("478");
    expect(pipelineHead(page)).toBe("Pipeline · attempt 1");
    expect([...page.querySelectorAll("[data-testid=pipeline] [data-testid=phase]")].map((p) => p.getAttribute("data-phase")))
      .toEqual(["implement", "review", "simplify", "fix"]);
    expect([...page.querySelectorAll("[data-testid=pr-step]")].map((p) => p.textContent)).toEqual([expect.stringContaining("#478")]);
    expect(tabCount(page, "Findings")).toBe("3");
    expect(tabCount(page, "Sessions")).toBe("4");
    expect(tabCount(page, "Files")).toBe("1");
    // Its time and cost are its own: four Runs, attempt 1's.
    const metrics = await until(() => page.querySelector<HTMLElement>("[data-testid=task-metrics]"), "attempt 1's time and cost");
    await until(() => (metrics.querySelectorAll("[data-testid=run-metrics] tbody tr").length === 4 ? true : null), "four Runs of attempt 1");
    expect(metrics.textContent).toContain("start to set aside");

    await openTab(page, "Findings");
    const titles = await until(() => {
      const t = texts(page, "[data-testid=finding]");
      return t.length > 0 ? t : null;
    }, "attempt 1's findings");
    expect(titles).toHaveLength(3);
    expect(titles.join()).toContain("Route moved under /billing/v2");
    expect(titles.join()).not.toContain("Totals rounded");

    await openTab(page, "Sessions");
    const sessions = await until(() => {
      const s = texts(page, "[data-testid=session]");
      return s.length === 4 ? s : null;
    }, "attempt 1's sessions");
    expect(sessions.map((s) => s.split("claude")[0])).toEqual(["Fix", "Simplify", "Review · correctness", "Implement"]);
    expect(count(page, "[data-testid=attempt-sessions]")).toBe(0);

    await openTab(page, "Files");
    await until(() => page.querySelector("[data-testid=files]"), "attempt 1's files");
    expect(page.querySelector("[data-testid=files]")?.textContent).toContain("routing.md");
    expect(page.querySelector("[data-testid=files]")?.textContent).not.toContain("form-split.md");
  });

  test("each attempt's Files lists its own files alone", async () => {
    const page = await taskPage(restarted(), { tab: "files" });
    const files = () => page.querySelector("[data-testid=files]")?.textContent ?? "";
    await until(() => (files().includes("form-split.md") ? true : null), "attempt 2's file");
    expect(files()).not.toContain("routing.md");
    await pick(page, 1);
    await until(() => (files().includes("routing.md") ? true : null), "attempt 1's file");
    expect(files()).not.toContain("form-split.md");
  });

  test("the header says how attempt 1 ended, and the bar who set it aside, when and why, with the way back", async () => {
    const page = await onAttempt1();
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("aborted");
    const bar = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(bar.textContent).toContain("Attempt 1 was set aside");
    expect(bar.textContent).toContain("ago, when Márcio Martins started over: “Keep the routing as it is; split the form only.”");
    expect(bar.textContent).toContain("It had stopped at Fix, aborted by Ana.");
    expect(bar.textContent).toContain("nothing in it can be merged, resumed or steered");
    await click(bar.querySelector("[data-testid=go-current]")!);
    await until(() => (shown(page) === "2" ? true : null), "attempt 2 again");
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("running");
  });

  test("attempt 1 offers nothing to do: no Merge, no servers, no pick-up, no composer", async () => {
    const page = await taskPage(restarted());
    // The servers are read and shown beside attempt 2's pull request first.
    await until(() => page.querySelector("[data-testid=servers-summary]"), "the servers on attempt 2");
    await pick(page, 1);
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel]"), "attempt 1's pull request");
    expect(panel.getAttribute("data-pr")).toBe("pr_478");
    expect(count(panel, "[data-testid=pr-merge]")).toBe(0);
    expect(panel.textContent).toContain("Open on GitHub");
    expect(panel.textContent).toContain("ago, when Márcio started over.");
    expect(count(page, "[data-testid=servers-summary]")).toBe(0);
    expect(count(page, "[data-testid=stopped]")).toBe(0);
    expect(count(page, "[data-testid^=recover-]")).toBe(0);
  });

  test("attempt 1's pull request left open on GitHub (its close is best effort) still offers no Merge", async () => {
    const client = restarted();
    const list = client.listPullRequests.bind(client);
    client.listPullRequests = async (id) => ({
      pullRequests: (await list(id)).pullRequests.map((pr) => (pr.id === "pr_478" ? { ...pr, state: "open" as const, display: "ci_running" as const } : pr)),
    });
    const page = await taskPage(client);
    await until(() => page.querySelector("[data-testid=pr-merge]"), "Merge on attempt 2's pull request");
    await pick(page, 1);
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel][data-pr=pr_478]"), "attempt 1's pull request");
    expect(count(panel, "[data-testid=pr-merge]")).toBe(0);
    expect(count(panel, "[data-testid=pr-request-review]")).toBe(0);
    expect(panel.textContent).toContain("Open on GitHub");
    // Still open: nothing says it was closed.
    expect(panel.textContent).not.toContain("Closed");
  });

  test("on attempt 2 the open pull request has Merge and the servers are beside it", async () => {
    const page = await taskPage(restarted());
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel]"), "attempt 2's pull request");
    expect(panel.getAttribute("data-pr")).toBe("pr_483");
    expect(count(panel, "[data-testid=pr-merge]")).toBe(1);
    await until(() => page.querySelector("[data-testid=servers-summary]"), "the servers beside it");
  });

  type Task = Awaited<ReturnType<FixtureClient["getTask"]>>;
  /** The restarted task as `change` makes it, wherever the page reads it. */
  function restartedAs(change: (t: Task) => Task) {
    const client = restarted();
    const getTask = client.getTask.bind(client);
    client.getTask = async (id) => change(await getTask(id));
    const getRun = client.getRun.bind(client);
    client.getRun = async (id) => {
      const r = await getRun(id);
      return { ...r, ...change({ ...(await getTask(TASK_ID)) }).runs.find((x) => x.id === id) } as typeof r;
    };
    return client;
  }
  const at = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
  /** Attempt 2's fixer aborted, and the task with it, to be picked back up. */
  function attempt2Aborted() {
    const client = restartedAs((t) => ({ ...t, status: "aborted",
      runs: t.runs.map((r) => (r.id === "run_a2_fix" ? { ...r, status: "aborted" as const, endedAt: at(5) } : r)) }));
    client.recoveryOptions = async () => ({ taskId: TASK_ID, actions: ["resume", "retry", "restart"], attempt: 2, keptUntil: at(-6 * 24 * 60) });
    return client;
  }

  test("attempt 2 stopped offers the ways back; attempt 1 offers none of them", async () => {
    const page = await taskPage(attempt2Aborted());
    const notice = await until(() => page.querySelector<HTMLElement>("[data-testid=stopped]"), "attempt 2's stopped notice");
    await until(() => notice.querySelector("[data-testid=recover-resume]"), "the ways back on attempt 2");
    expect(count(page, "[data-testid^=recover-]")).toBe(3);
    await pick(page, 1);
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "attempt 1's bar");
    expect(count(page, "[data-testid=stopped]")).toBe(0);
    expect(count(page, "[data-testid^=recover-]")).toBe(0);
  });

  test("attempt 2's stopped session offers Resume on its strip; attempt 1's sessions only the way to attempt 2", async () => {
    const page = await taskPage(attempt2Aborted(), { runId: "run_a2_fix" });
    const strip = await until(() => page.querySelector<HTMLElement>("[data-testid=run-ended]"), "attempt 2's end strip");
    await until(() => strip.querySelector("[data-testid=run-ended-resume]"), "Resume… on attempt 2's strip");
    const one = await taskPage(attempt2Aborted(), { runId: "run_a1_fix" });
    const old = await until(() => one.querySelector<HTMLElement>("[data-testid=run-ended]"), "attempt 1's end strip");
    await until(() => old.querySelector("[data-testid=run-ended-current]"), "the way to attempt 2");
    expect(count(old, "[data-testid=run-ended-resume]")).toBe(0);
    expect(count(old, "[data-testid=run-ended-other]")).toBe(0);
  });

  test("attempt 2 escalated shows its decision; attempt 1 does not", async () => {
    const page = await taskPage(restartedAs((t) => ({ ...t, status: "awaiting_input",
      escalation: { reason: "implement_failed", detail: { runId: "run_a2_fix", error: "host lost" }, actions: ["retry", "stop"], at: at(3) } })));
    await until(() => page.querySelector("[data-testid=escalation]"), "attempt 2's escalation");
    expect(count(page, "[data-testid=escalation]")).toBe(1);
    await pick(page, 1);
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "attempt 1's bar");
    expect(count(page, "[data-testid=escalation]")).toBe(0);
  });

  test("attempt 2 finished with no pull request offers Mark done; attempt 1 does not", async () => {
    const client = restartedAs((t) => ({ ...t, status: "review",
      runs: t.runs.map((r) => (r.id === "run_a2_fix" ? { ...r, status: "completed" as const, endedAt: at(2) } : r)) }));
    client.listPullRequests = async () => ({ pullRequests: [] });
    const page = await taskPage(client);
    await until(() => page.querySelector("[data-testid=mark-done]"), "Mark done on attempt 2");
    await pick(page, 1);
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "attempt 1's bar");
    expect(count(page, "[data-testid=mark-done]")).toBe(0);
  });

  /** The restarted task's ledger as `change` makes it. */
  function restartedWithEvents(change: (events: PersistedEvent[]) => PersistedEvent[], client = restarted()) {
    const events = client.events.bind(client);
    client.events = async (params) => {
      const r = await events(params);
      return { ...r, events: change(r.events) };
    };
    return client;
  }

  test("an attempt 1 whose fixer failed says failed, in the header and in the picker", async () => {
    const page = await onAttempt1(restartedAs((t) => ({ ...t,
      runs: t.runs.map((r) => (r.id === "run_a1_fix" ? { ...r, status: "failed" as const, error: "host lost" } : r)) })));
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("failed");
    expect(picker(page)?.textContent).toBe("FailedAttempt 1set aside");
    await press(picker(page)!);
    const option = await until(() => document.querySelector<HTMLElement>('[role=option][data-value="1"]'), "attempt 1 in the picker");
    expect(option.textContent).toContain("stopped at Fix, failed");
  });

  test("a branch preview that failed after attempt 1's fixer is not where attempt 1 stopped", async () => {
    const preview = { ...RESTARTED_RUNS.find((r) => r.id === "run_a1_fix")!, id: "run_a1_preview", kind: "preview" as const, phase: null, role: null,
      status: "failed" as const, error: "the preview's server never started", createdAt: at(105), startedAt: at(105), endedAt: at(96) };
    const page = await onAttempt1(restartedAs((t) => ({ ...t, runs: [...t.runs, preview] })));
    expect(page.querySelector("[data-testid=header-status]")?.getAttribute("data-status")).toBe("aborted");
    const bar = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "the earlier-attempt bar");
    expect(bar.textContent).toContain("It had stopped at Fix, aborted by Ana.");
  });

  test("with three attempts, each earlier one's bar says who started the next one over, and why", async () => {
    // Attempt 2's fixer aborted 8 minutes ago; Ana started over as attempt 3 six minutes ago.
    const client = restartedAs((t) => ({ ...t,
      runs: [
        { ...t.runs.find((r) => r.id === "run_attempt2")!, id: "run_a3_impl", attempt: 3, status: "running" as const, branch: "dude/task_wc214/attempt-3",
          createdAt: at(5), startedAt: at(5), endedAt: null },
        ...t.runs.map((r) => (r.id === "run_a2_fix" ? { ...r, status: "aborted" as const, endedAt: at(8) } : r)),
      ] }));
    const page = await taskPage(restartedWithEvents((events) => [...events, { ...events.find((e) => e.eventType === "task.recovered")!,
      eventId: "evt_restart_3", cursor: 20_000, occurredAt: at(6), actor: { type: "human", id: "u_ana" },
      payload: { action: "restart", attempt: 3, note: "Start from main again." } }], client));
    await until(() => (shown(page) === "3" ? true : null), "attempt 3, the current one");
    await pick(page, 2);
    const two = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-bar]"), "attempt 2's bar");
    expect(two.textContent).toContain("Attempt 2 was set aside 6m ago, when Ana Ribeiro started over: “Start from main again.”");
    expect(two.textContent).not.toContain(RESTART.note);
    await pick(page, 1);
    await until(() => (page.querySelector("[data-testid=earlier-bar]")?.textContent?.includes("Attempt 1") ? true : null), "attempt 1's bar");
    const one = page.querySelector<HTMLElement>("[data-testid=earlier-bar]")!;
    expect(one.textContent).toContain(`when Márcio Martins started over: “${RESTART.note}”`);
    expect(one.textContent).not.toContain("Start from main again.");
  });
  const movedClose = (min: number, actor?: PersistedEvent["actor"]) => (events: PersistedEvent[]) =>
    events.map((e) => (e.eventType === "pull_request.closed" ? { ...e, occurredAt: at(min), ...(actor ? { actor } : {}) } : e));

  test("attempt 1's pull request closed before the start over is not said to be dude's close", async () => {
    // Closed on GitHub 100 minutes ago; started over 95 minutes ago.
    const page = await onAttempt1(restartedWithEvents(movedClose(100, { type: "system", id: "forge" })));
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel][data-pr=pr_478]"), "attempt 1's pull request");
    expect(panel.textContent).toContain("Closed. Attempt 1 was set aside");
    expect(panel.textContent).not.toContain("Closed by");
    expect(panel.textContent).not.toContain("started over");
  });

  test("attempt 1's pull request closed after the start over is dude's close, dated when it closed", async () => {
    const page = await onAttempt1(restartedWithEvents(movedClose(20)));
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel][data-pr=pr_478]"), "attempt 1's pull request");
    expect(panel.textContent).toMatch(/Closed by .+ 20m ago, when Márcio started over\./);
  });

  test("attempt 1's pull request closed on GitHub after attempt 2 began is still attempt 1's in Activity", async () => {
    const page = await taskPage(restartedWithEvents(movedClose(20)), { tab: "activity" });
    const line = await activityLine(page, "#478 was closed");
    expect(line.getAttribute("data-attempt")).toBe("1");
  });

  test("a person's act on no Run is marked with the attempt under way when they did it", async () => {
    const page = await taskPage(restarted(), { tab: "activity" });
    // Decided 112 minutes ago, while attempt 1 was at work; handed over 50 minutes ago, in attempt 2.
    expect((await activityLine(page, "decided how delivery goes on")).getAttribute("data-attempt")).toBe("1");
    expect((await activityLine(page, "handed the task to")).getAttribute("data-attempt")).toBe("2");
  });

  test("a branch preview does not say when its attempt began", async () => {
    // A preview recorded on attempt 2 before attempt 2's first agent: the decision 112 minutes ago is still attempt 1's.
    const preview = { ...RESTARTED_RUNS.find((r) => r.id === "run_attempt2")!, id: "run_a2_preview", kind: "preview" as const, phase: null, role: null,
      status: "completed" as const, createdAt: at(115), startedAt: at(115), endedAt: at(100) };
    const page = await taskPage(restartedAs((t) => ({ ...t, runs: [...t.runs, preview] })), { tab: "activity" });
    const line = await activityLine(page, "decided how delivery goes on");
    expect(line.getAttribute("data-attempt")).toBe("1");
  });

  test("what attempt 1's Run recorded after attempt 2 began is still attempt 1's", async () => {
    const late = (events: PersistedEvent[]) => events.map((e) => (e.eventType === "artifact.created" && e.payload.artifactId === "art_a1_notes"
      ? { ...e, occurredAt: at(20) } : e));
    const page = await taskPage(restartedWithEvents(late), { tab: "activity" });
    const line = await activityLine(page, "saved notes/routing.md");
    expect(line.getAttribute("data-attempt")).toBe("1");
  });

  test("a fix's reason is read from its own attempt's pull request, not an earlier one's", async () => {
    // Attempt 1's #478 failed CI 115 minutes ago, before its fixer (110) and long before attempt 2's (10).
    const failing = (events: PersistedEvent[]) => [...events, { ...events.find((e) => e.eventType === "pull_request.closed")!,
      eventId: "evt_ci_478", cursor: 20_000, eventType: "pull_request.checks_changed", occurredAt: at(115),
      payload: { number: 478, repo: "example/web-console", from: "pending", to: "failing" } }];
    const page = await taskPage(restartedWithEvents(failing));
    await until(() => picker(page), "the attempt picker");
    const fix = () => [...page.querySelectorAll<HTMLElement>("[data-testid=pipeline] [data-testid=phase][data-phase=fix]")].at(-1)?.textContent ?? "";
    await until(() => (fix() ? true : null), "attempt 2's fixer");
    expect(fix()).not.toContain("failing CI");
    expect(fix()).toContain("for the pull request's feedback");
    await pick(page, 1);
    await until(() => (fix().includes("for failing CI") ? true : null), "attempt 1's fixer, for its CI");
  });

  test("a non-existent attempt in the URL is written as the task's, and the current attempt is shown", async () => {
    const { said, onNavigate } = navigations();
    const page = await taskPage(restarted(), { attempt: 9, onNavigate });
    await until(() => (said.length > 0 ? true : null), "the URL rewritten");
    expect(said).toEqual([[undefined, undefined, true]]);
    await until(() => picker(page), "the attempt picker");
    expect(count(page, "[data-testid=task-screen][data-attempt='2']")).toBe(1);
    expect(count(page, "[data-testid=earlier-bar]")).toBe(0);
  });

  test("while attempt 1's figures are on their way, attempt 2's are not shown in their place", async () => {
    const client = restarted();
    const metrics = client.taskMetrics.bind(client);
    let release: () => void = () => {};
    const held = new Promise<void>((r) => (release = r));
    client.taskMetrics = async (id, attempt) => {
      if (attempt === 1) await held;
      return metrics(id, attempt);
    };
    const page = await taskPage(client);
    await until(() => page.querySelector("[data-testid=task-metrics]"), "attempt 2's figures");
    await pick(page, 1);
    await settle(100);
    expect(count(page, "[data-testid=task-metrics]")).toBe(0);
    release();
    const section = await until(() => page.querySelector<HTMLElement>("[data-testid=task-metrics]"), "attempt 1's figures");
    expect(section.textContent).toContain("start to set aside");
    expect(section.querySelectorAll("[data-testid=run-metrics] tbody tr").length).toBe(4);
  });

  test("the page reads attempt 2's figures alone; the picker reads each attempt's cost when it opens", async () => {
    const client = restarted();
    const reads: Array<number | undefined> = [];
    const metrics = client.taskMetrics.bind(client);
    client.taskMetrics = (id, attempt) => {
      reads.push(attempt);
      return metrics(id, attempt);
    };
    const page = await taskPage(client);
    await until(() => page.querySelector("[data-testid=task-metrics]"), "attempt 2's figures");
    await settle(100);
    expect(reads).toStrictEqual([2]);
    // A reload the stream causes reads attempt 2's again, and not the whole task's.
    await act(async () => emit({ eventType: "task.status_changed", occurredAt: new Date().toISOString(), organizationId: ORG.id, projectId: PROJECT.id,
      taskId: TASK_ID, runId: null, sessionId: null, workflowRunId: null, actor: { type: "system", id: "dude" }, source: "control-plane",
      correlationId: null, causationId: null, payload: { status: "running" } } as never));
    await until(() => (reads.length === 2 ? true : null), "the reload's read");
    await settle(400);
    expect(reads).toStrictEqual([2, 2]);
    await press(picker(page)!);
    const option = await until(() => document.querySelector<HTMLElement>('[role=option][data-value="1"]'), "attempt 1 in the picker");
    await until(() => (option.textContent?.includes("$13.40") ? true : null), "attempt 1's cost");
    // Strict: `toEqual` takes a trailing undefined as absent, so it would not see a fourth read.
    expect(reads).toStrictEqual([2, 2, undefined]);
    // Closing the list reads nothing.
    await click(document.querySelector<HTMLElement>('[role=option][data-value="2"]')!);
    await until(() => (document.querySelector("[role=option]") ? null : true), "the list closed");
    await settle(100);
    expect(reads).toStrictEqual([2, 2, undefined]);
  });

  test("a cost read that answers late does not replace the one that answered after it", async () => {
    const client = restarted();
    const metrics = client.taskMetrics.bind(client);
    let release: () => void = () => {};
    const held = new Promise<void>((r) => (release = r));
    let wholeTask = 0;
    client.taskMetrics = async (id, attempt) => {
      if (attempt !== undefined) return metrics(id, attempt);
      // The first read of the whole task is held, and then says every Run cost $10.
      if (++wholeTask > 1) return metrics(id);
      await held;
      const m = await metrics(id);
      return { ...m, runs: m.runs.map((r) => ({ ...r, cost: { ...r.cost, totalUsd: 10 } })) };
    };
    const page = await taskPage(client);
    await until(() => picker(page), "the attempt picker");
    const one = () => document.querySelector<HTMLElement>('[role=option][data-value="1"]');
    await press(picker(page)!);
    await until(one, "attempt 1 in the picker");
    await click(document.querySelector<HTMLElement>('[role=option][data-value="2"]')!);
    await until(() => (one() ? null : true), "the list closed");
    await press(picker(page)!);
    await until(() => (one()?.textContent?.includes("$13.40") ? true : null), "attempt 1's cost from the second read");
    release();
    await settle(100);
    expect(one()?.textContent).toContain("$13.40");
    expect(one()?.textContent).not.toContain("$40.00");
  });

  test("a session of attempt 1 is read-only: no Pause, Abort or composer, and its strip leads to attempt 2", async () => {
    const page = await taskPage(restarted(), { runId: "run_a1_fix" });
    await until(() => (shown(page) === "1" ? true : null), "the session's attempt shown");
    expect(page.querySelector("[data-testid=attempt-picker]")?.textContent).toBe("AbortedAttempt 1set aside");
    const strip = await until(() => page.querySelector<HTMLElement>("[data-testid=run-ended]"), "the session's end strip");
    expect(strip.textContent).toContain("Set aside when its task was started over.");
    expect(count(strip, "[data-testid=run-ended-resume]")).toBe(0);
    expect(count(page, "[data-testid=abort]")).toBe(0);
    expect(count(page, "textarea")).toBe(0);
    await click(strip.querySelector("[data-testid=run-ended-current]")!);
    await until(() => (shown(page) === "2" ? true : null), "attempt 2 again");
  });

  /** A repository request, a queued steer and a failed one, on `runId`'s transcript. */
  async function askAndSteer(runId: string) {
    const at = new Date().toISOString();
    const send = (eventType: string, payload: Record<string, unknown>, actor = { type: "agent", id: runId }) => act(async () => {
      emit({ eventType, occurredAt: at, organizationId: ORG.id, projectId: PROJECT.id, taskId: TASK_ID, runId, sessionId: `${runId}-s`,
        workflowRunId: null, actor, source: "control-plane", correlationId: null, causationId: null, payload } as never);
    });
    const me = { type: "human", id: YOU };
    await send("repository.requested", { requestId: "rr_1", repository: "example/billing", access: "read", reason: "The invoice types live there." });
    await send("run.steered", { text: "Keep the old route too.", directiveId: "dir_q" }, me);
    await send("run.steered", { text: "And the tests.", directiveId: "dir_f" }, me);
    await send("run.directive.failed", { directiveId: "dir_f", error: "the agent exited" });
    await settle(50);
  }
  // A question card's options read "1Approve": their index, then the word.
  const buttons = (root: ParentNode, text: string) => [...root.querySelectorAll("button")].filter((b) => b.textContent?.endsWith(text)).length;

  test("on a running session of attempt 2, a repository request can be decided and steers interrupted or retried", async () => {
    const page = await taskPage(restarted(), { runId: "run_a2_fix" });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on attempt 2's fixer");
    await until(() => page.querySelector("[data-testid=terminal-link]"), "the terminal in attempt 2's rail");
    expect(count(page, "[data-testid=terminal-link]")).toBe(1);
    await askAndSteer("run_a2_fix");
    const request = await until(() => page.querySelector<HTMLElement>("[data-testid=repository-request]"), "the repository request");
    expect(buttons(request, "Approve")).toBe(1);
    expect(buttons(page, "Interrupt now")).toBe(1);
    expect(buttons(page, "Retry")).toBe(1);
  });

  test("an attempt 1 session still recorded as running is read-only too, its transcript included", async () => {
    const client = restartedAs((t) => ({ ...t,
      runs: t.runs.map((r) => (r.id === "run_a1_fix" ? { ...r, status: "running" as const, endedAt: null } : r)) }));
    const page = await taskPage(client, { runId: "run_a1_fix" });
    await until(() => page.querySelector("[data-testid=run-ended]"), "the set-aside strip");
    expect(count(page, "[data-testid=abort]")).toBe(0);
    expect(count(page, "textarea")).toBe(0);
    await askAndSteer("run_a1_fix");
    const request = await until(() => page.querySelector<HTMLElement>("[data-testid=repository-request]"), "the repository request");
    await until(() => (count(page, "[data-testid=human-turn]") === 2 ? true : null), "both steers");
    expect(buttons(request, "Approve")).toBe(0);
    expect(buttons(request, "Decline")).toBe(0);
    expect(buttons(page, "Interrupt now")).toBe(0);
    expect(buttons(page, "Retry")).toBe(0);
    // No shell into its sandbox either, in the rail or the header.
    expect(count(page, "[data-testid=terminal-link]")).toBe(0);
    expect(count(page, "[data-testid=terminal-icon]")).toBe(0);
  });

  test("a running session of attempt 2 can be steered and stopped", async () => {
    const page = await taskPage(restarted(), { runId: "run_a2_fix" });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on attempt 2's fixer");
    expect(count(page, "textarea")).toBe(1);
    expect(count(page, "[data-testid=run-ended]")).toBe(0);
  });

  test("an empty tab says what the other attempts had, and goes there", async () => {
    // Attempt 2 saved nothing yet.
    const client = restarted();
    const listArtifacts = client.listArtifacts.bind(client);
    client.listArtifacts = async () => ({ artifacts: (await listArtifacts()).artifacts.filter((a) => a.runId !== "run_attempt2") });
    const page = await taskPage(client, { tab: "files" });
    await until(() => picker(page), "the attempt picker");
    const line = await until(() => page.querySelector<HTMLElement>("[data-testid=elsewhere]"), "what attempt 1 had");
    expect(page.textContent).toContain("No files in attempt 2 yet");
    expect(line.textContent).toContain("Attempt 1 had 1 file.");
    await click(line.querySelector("button")!);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 shown");
    await until(() => page.querySelector("[data-testid=files]"), "attempt 1's files");
  });

  test("an empty Findings tab says attempt 1 had three, and shows them", async () => {
    const page = await taskPage(restarted(), { tab: "findings" });
    await until(() => picker(page), "the attempt picker");
    const line = await until(() => page.querySelector<HTMLElement>("[data-testid=elsewhere][data-attempt='1']"), "what attempt 1 had");
    expect(page.textContent).toContain("No findings in attempt 2");
    expect(line.textContent).toContain("Attempt 1 had 3 findings.");
    expect(count(page, "[data-testid=finding]")).toBe(0);
    await click(line.querySelector("button")!);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 shown");
    await until(() => (count(page, "[data-testid=finding]") === 3 ? true : null), "attempt 1's three findings");
  });

  test("a tab's tooltip says what the other attempts had", async () => {
    const page = await taskPage(restarted());
    await until(() => picker(page), "the attempt picker");
    const tab = [...page.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Findings"))!;
    await act(async () => {
      tab.focus();
    });
    const tip = await until(() => document.querySelector<HTMLElement>("[role=tooltip]"), "the Findings tooltip");
    expect(tip.textContent).toBe("Attempt 2's findings; attempt 1 had 3");
  });

  test("a finding whose Run is unknown shows with the current attempt", async () => {
    const client = restarted();
    const listFindings = client.listFindings.bind(client);
    client.listFindings = async (id) => ({ findings: [...(await listFindings(id)).findings, ORPHAN_FINDING] });
    const page = await taskPage(client, { tab: "findings" });
    await until(() => picker(page), "the attempt picker");
    const titles = await until(() => {
      const t = texts(page, "[data-testid=finding]");
      return t.length > 0 ? t : null;
    }, "attempt 2's findings");
    expect(titles).toHaveLength(1);
    expect(titles[0]).toContain("Totals rounded before VAT");
    await pick(page, 1);
    await until(() => (texts(page, "[data-testid=finding]").length === 3 ? true : null), "attempt 1's three");
    expect(texts(page, "[data-testid=finding]").join()).not.toContain("Totals rounded");
  });

  test("Activity shows both attempts whichever is picked, each line marked, and its links go to theirs", async () => {
    const opened: string[] = [];
    const page = await taskPage(restarted(), { tab: "activity", onOpenRun: (id) => opened.push(id) });
    const items = await until(() => {
      const i = [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")];
      return i.some((l) => l.getAttribute("data-attempt") === "1") ? i : null;
    }, "Activity's lines");
    const attempts = () => new Set([...page.querySelectorAll("[data-testid=activity-item]")].map((l) => l.getAttribute("data-attempt")));
    expect(attempts()).toEqual(new Set(["1", "2"]));
    const restart = items.find((l) => l.textContent?.includes("picked the task back up"))!;
    expect(restart.textContent).toContain("started over as attempt 2");
    expect(restart.textContent).toContain("· attempt 2");
    // Picked on attempt 1, Activity is the same.
    await pick(page, 1);
    expect(attempts()).toEqual(new Set(["1", "2"]));
    // Attempt 1's findings, from its review's line, while attempt 2 is picked again.
    await pick(page, 2);
    const raised = [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")].find((l) => l.textContent?.includes("raised 3 findings"))!;
    expect(raised.getAttribute("data-attempt")).toBe("1");
    await click(raised.querySelector("[data-testid=activity-open]")!);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 shown");
    await until(() => (texts(page, "[data-testid=finding]").length === 3 ? true : null), "attempt 1's findings");
    // A session's line opens that session, whose attempt it is.
    await openTab(page, "Activity");
    const aborted = await activityLine(page, "aborted the fix");
    await click(aborted.querySelector("[data-testid=activity-open]")!);
    expect(opened).toEqual(["run_a1_fix"]);
  });

  test("the URL names the attempt only when it is not the current one", async () => {
    const { said, onNavigate } = navigations();
    const page = await taskPage(restarted(), { onNavigate });
    await pick(page, 1);
    expect(said.at(-1)).toEqual([undefined, 1, false]);
    await openTab(page, "Findings");
    expect(said.at(-1)).toEqual(["findings", 1, true]);
    // Activity shows every attempt: its URL names none.
    await openTab(page, "Activity");
    expect(said.at(-1)).toEqual(["activity", undefined, true]);
    await openTab(page, "Findings");
    expect(said.at(-1)).toEqual(["findings", 1, true]);
    await pick(page, 2);
    expect(said.at(-1)).toEqual(["findings", undefined, false]);
  });

  test("a URL naming attempt 1 opens on it; one naming the current attempt is written as the task's", async () => {
    const page = await taskPage(restarted(), { tab: "findings", attempt: 1 });
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 from the URL");
    await until(() => (texts(page, "[data-testid=finding]").length === 3 ? true : null), "attempt 1's findings");

    const { said, onNavigate } = navigations();
    const current = await taskPage(restarted(), { attempt: 2, onNavigate });
    await until(() => (said.length > 0 ? true : null), "the URL rewritten");
    expect(said).toEqual([[undefined, undefined, true]]);
    expect(current.querySelectorAll("[data-attempt='2'][data-testid=task-screen]").length).toBe(1);
  });
});

describe("a task started over, from its URL", () => {
  let gone: Array<() => Promise<void>> = [];
  afterEach(async () => {
    for (const u of gone) await u();
    gone = [];
    window.history.replaceState(null, "", " ");
  });
  async function app(hash: string) {
    localStorage.setItem("dude.fixtures.run", "restarted");
    window.history.replaceState(null, "", hash);
    const client = new FixtureClient("a");
    const { container, unmount } = await mount(
      <TooltipProvider>
        <ToastProvider>
          <PeopleProvider client={client}>
            <App client={client} onSignOut={() => {}} onKeyRefused={() => {}} />
          </PeopleProvider>
        </ToastProvider>
      </TooltipProvider>,
    );
    gone.push(unmount);
    return container;
  }
  const shownIn = (page: HTMLElement) => page.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt");

  test("a plain task link opens the current attempt; ?attempt=1 opens attempt 1; Back and Forward move between them", async () => {
    const page = await app(`#/task/${TASK_ID}`);
    await until(() => (shownIn(page) === "2" ? true : null), "attempt 2 from a plain link");
    await until(() => page.querySelector("[data-testid=attempt-picker]"), "the picker");
    await act(async () => void page.querySelector("[data-testid=attempt-picker]")!.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
    await click(await until(() => document.querySelector<HTMLElement>('[role=option][data-value="1"]'), "attempt 1 in the picker"));
    await until(() => (shownIn(page) === "1" ? true : null), "attempt 1");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}?attempt=1`);
    await openTab(page, "Findings");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}/findings?attempt=1`);

    // Back: the step before picking attempt 1 is the plain task.
    await act(async () => {
      window.history.back();
      await new Promise((r) => setTimeout(r, 50));
    });
    await until(() => (shownIn(page) === "2" ? true : null), "attempt 2 after Back");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}`);
    await act(async () => {
      window.history.forward();
      await new Promise((r) => setTimeout(r, 50));
    });
    await until(() => (shownIn(page) === "1" ? true : null), "attempt 1 after Forward");
  });

  test("a session's link opens its attempt", async () => {
    const page = await app("#/session/run_a1_review");
    await until(() => (shownIn(page) === "1" ? true : null), "the session's attempt");
    await until(() => page.querySelector("[data-testid=earlier-bar]"), "the earlier-attempt bar");
  });
});

describe("what stopped the task", () => {
  const people = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => people, seen: () => false } as never;
  const at = (min: number) => new Date(Date.now() - min * 60_000).toISOString();
  const ev = (eventType: string, min: number, payload: Record<string, unknown> = {}) =>
    ({ eventType, occurredAt: at(min), runId: null, payload, actor: { type: "human", id: "u" } }) as never;

  test("a failure the work went on past, by a decision, is not what stopped it later", () => {
    const base = taskFor("a");
    const failed = { ...base.runs[0]!, id: "run_old", status: "failed" as const, endedAt: at(60), error: "host lost" };
    const task = { ...base, status: "aborted" as const, runs: [failed] };
    // Tried again on the escalation, and later stopped on no Run.
    const stop = stopOf(task, [ev("task.decided", 50, { action: "retry" }), ev("task.decided", 5, { action: "stop" })], people);
    expect(stop.run).toBeNull();
    // Without that decision, the failure is what stopped it.
    expect(stopOf(task, [ev("task.decided", 5, { action: "stop" })], people).run?.id).toBe("run_old");
  });
});
