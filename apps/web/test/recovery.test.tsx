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
import { ORG, ORPHAN_FINDING, PROJECT, RUN_ID, TASK_ID, YOU, taskFor } from "../src/fixtures/data.ts";
import { stopOf } from "../src/screens/Recovery.tsx";
import type { RecoverAction } from "../src/api/client.ts";
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
    await press(picker(page)!);
    const option = await until(() => document.querySelector<HTMLElement>(`[role=option][data-value="${n}"]`), `attempt ${n} in the picker`);
    await click(option);
    await until(() => (shown(page) === String(n) ? true : null), `attempt ${n} shown`);
  }
  /** The page on attempt 1, through the picker. */
  async function onAttempt1(props: Partial<Parameters<typeof TaskScreen>[0]> = {}) {
    const page = await taskPage(restarted(), props);
    await until(() => picker(page), "the attempt picker");
    await pick(page, 1);
    return page;
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

  test("the picker opens on the current attempt, and its options say how each went", async () => {
    const page = await taskPage(restarted());
    const trigger = await until(() => picker(page), "the attempt picker");
    expect(shown(page)).toBe("2");
    // The status mark is icon-only: its word is for a screen reader.
    expect(trigger.textContent).toBe("RunningAttempt 2current");
    // The page is attempt 2's: its branch, its pull request, its pipeline.
    expect(page.querySelector("[data-testid=branch]")?.textContent).toBe("dude/task_wc214/attempt-2");
    expect(texts(page, "[data-testid=pr-link]").join()).toContain("483");
    expect([...page.querySelectorAll("h2")].find((h) => h.textContent?.startsWith("Pipeline"))?.textContent).toBe("Pipeline · attempt 2");
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
    expect(options[1]!.textContent).toContain("Set aside");
    expect(options[1]!.textContent).toContain("stopped at Fix, aborted by Ana");
    expect(options[1]!.textContent).toContain("dude/task_wc214/attempt-1");
    expect(options[1]!.textContent).toContain("PR #478 closed");
    expect(options[1]!.textContent).toContain("$13.40");
    expect(document.body.textContent).toContain("Activity always shows every attempt.");
  });

  test("attempt 1 shows its branch, pull request, pipeline, findings, sessions and files, and its counts", async () => {
    const page = await taskPage(restarted());
    await until(() => picker(page), "the attempt picker");
    // Attempt 2's counts first: no findings, no files, four sessions.
    expect(tabCount(page, "Findings")).toBe("");
    expect(tabCount(page, "Sessions")).toBe("4");
    expect(tabCount(page, "Files")).toBe("");
    await pick(page, 1);
    expect(page.querySelector("[data-testid=branch]")?.textContent).toBe("dude/task_wc214/attempt-1");
    expect(texts(page, "[data-testid=pr-link]")).toHaveLength(1);
    expect(texts(page, "[data-testid=pr-link]")[0]).toContain("478");
    expect([...page.querySelectorAll("h2")].find((h) => h.textContent?.startsWith("Pipeline"))?.textContent).toBe("Pipeline · attempt 1");
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
    await until(() => picker(page), "the attempt picker");
    await pick(page, 1);
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel][data-pr=pr_478]"), "attempt 1's pull request");
    expect(count(panel, "[data-testid=pr-merge]")).toBe(0);
    expect(count(panel, "[data-testid=pr-request-review]")).toBe(0);
    expect(panel.textContent).toContain("Open on GitHub");
  });

  test("on attempt 2 the open pull request has Merge and the servers are beside it", async () => {
    const page = await taskPage(restarted());
    const panel = await until(() => page.querySelector<HTMLElement>("[data-testid=pr-panel]"), "attempt 2's pull request");
    expect(panel.getAttribute("data-pr")).toBe("pr_483");
    expect(count(panel, "[data-testid=pr-merge]")).toBe(1);
    await until(() => page.querySelector("[data-testid=servers-summary]"), "the servers beside it");
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
    await askAndSteer("run_a2_fix");
    const request = await until(() => page.querySelector<HTMLElement>("[data-testid=repository-request]"), "the repository request");
    expect(buttons(request, "Approve")).toBe(1);
    expect(buttons(page, "Interrupt now")).toBe(1);
    expect(buttons(page, "Retry")).toBe(1);
  });

  test("an attempt 1 session still recorded as running is read-only too, its transcript included", async () => {
    const client = restarted();
    const getTask = client.getTask.bind(client);
    const live = (r: Awaited<ReturnType<typeof getTask>>["runs"][number]) => (r.id === "run_a1_fix" ? { ...r, status: "running" as const, endedAt: null } : r);
    client.getTask = async (id) => {
      const t = await getTask(id);
      return { ...t, runs: t.runs.map(live) };
    };
    const getRun = client.getRun.bind(client);
    client.getRun = async (id) => live(await getRun(id)) as Awaited<ReturnType<typeof getRun>>;
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
  });

  test("a running session of attempt 2 can be steered and stopped", async () => {
    const page = await taskPage(restarted(), { runId: "run_a2_fix" });
    await until(() => page.querySelector("[data-testid=abort]"), "Abort on attempt 2's fixer");
    expect(count(page, "textarea")).toBe(1);
    expect(count(page, "[data-testid=run-ended]")).toBe(0);
  });

  test("an empty tab says what the other attempts had, and goes there", async () => {
    const page = await taskPage(restarted(), { tab: "files" });
    await until(() => picker(page), "the attempt picker");
    const line = await until(() => page.querySelector<HTMLElement>("[data-testid=elsewhere]"), "what attempt 1 had");
    expect(page.textContent).toContain("No files in attempt 2 yet");
    expect(line.textContent).toContain("Attempt 1 had 1 file.");
    await click(line.querySelector("button")!);
    await until(() => (shown(page) === "1" ? true : null), "attempt 1 shown");
    await until(() => page.querySelector("[data-testid=files]"), "attempt 1's files");
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
    const aborted = await until(() => [...page.querySelectorAll<HTMLElement>("[data-testid=activity-item]")].find((l) => l.textContent?.includes("aborted the fix")), "the abort");
    await click(aborted.querySelector("[data-testid=activity-open]")!);
    expect(opened).toEqual(["run_a1_fix"]);
  });

  test("the URL names the attempt only when it is not the current one", async () => {
    const said: Array<[string | undefined, number | undefined, boolean]> = [];
    const onNavigate = (tab: string | undefined, attempt: number | undefined, replace: boolean) => said.push([tab, attempt, replace]);
    const page = await taskPage(restarted(), { onNavigate });
    await until(() => picker(page), "the attempt picker");
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

    const said: Array<[string | undefined, number | undefined, boolean]> = [];
    const current = await taskPage(restarted(), { attempt: 2, onNavigate: (t, a, r) => said.push([t, a, r]) });
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
