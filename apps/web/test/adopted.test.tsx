/**
 * A task that took over existing work — an open pull request or a branch —
 * as its page shows it. New work is shown as before. Mounted in happy-dom
 * against the fixture client.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PersistedEvent } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PULL_REQUEST, RESTARTED_PULL_REQUESTS, RESTARTED_RUNS, TASK_ID } from "../src/fixtures/data.ts";
import type { PullRequest, TaskDetail, TaskSource } from "../src/api/client.ts";
import { eventAttempts } from "../src/attempts.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

const HEAD = "feature/greet";
const PR_SOURCE: TaskSource = { kind: "pr", repositoryId: "repo_wc", repository: "web-console", number: 12, title: "Greet in Portuguese", headBranch: HEAD, baseBranch: "main" };
/** The adopted pull request as dude records it: no Run opened it. */
const ADOPTED: PullRequest = { ...PULL_REQUEST, id: "pr_12", runId: null, number: 12, title: "Greet in Portuguese", headBranch: HEAD,
  url: "https://github.com/example/web-console/pull/12" };

/** Scenario d (reviewed, simplified) as a task that took over `source`: no implementer, `prs` its pull requests. */
class AdoptedClient extends FixtureClient {
  constructor(protected patch: Partial<TaskDetail>, private readonly prs: PullRequest[], scenario: "a" | "d" = "d") {
    super(scenario);
  }
  override async getTask(id: string): Promise<TaskDetail> {
    const t = await super.getTask(id);
    return { ...t, runs: t.runs.filter((r) => this.patch.source === null || r.phase !== "implement"), ...this.patch } as TaskDetail;
  }
  override listPullRequests(_taskId: string) {
    return Promise.resolve({ pullRequests: this.prs });
  }
}

async function taskPage(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await until(() => container.querySelector("[data-testid=pipeline]"), "the pipeline");
  await settle();
  return container;
}

const text = (el: Element | null) => el?.textContent?.replace(/\s+/g, " ").trim() ?? "";

describe("the adopted pull request across attempts", () => {
  // Picks attempt `n` in the header's picker, as a person would.
  const press = (el: Element) =>
    act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
  async function pick(page: HTMLElement, n: number) {
    await press(await until(() => page.querySelector("[data-testid=attempt-picker]"), "the attempt picker"));
    await click(await until(() => document.querySelector(`[role=option][data-value="${n}"]`), `attempt ${n}`));
    await until(() => page.querySelector("[data-testid=task-screen]")?.getAttribute("data-attempt") === String(n) || null, `attempt ${n} shown`);
  }
  const panels = (page: HTMLElement) => [...page.querySelectorAll("[data-testid=pr-panel]")].map((p) => p.getAttribute("data-pr"));

  test("a task started over works on the one adopted pull request: each attempt shows it, an earlier one only to read", async () => {
    localStorage.setItem("dude.fixtures.run", "restarted");
    const page = await taskPage(new AdoptedClient({ source: PR_SOURCE }, [ADOPTED], "a"));
    expect(panels(page)).toEqual(["pr_12"]);
    expect(page.querySelectorAll("[data-testid=pr-panel] [data-testid=pr-merge]").length).toBe(1);
    expect(text(page.querySelector("[data-testid=pr-step]"))).toContain("#12");
    await pick(page, 1);
    await until(() => panels(page).length === 1 || null, "attempt 1's panel");
    expect(panels(page)).toEqual(["pr_12"]);
    expect(page.querySelectorAll("[data-testid=pr-merge]").length).toBe(0);
    expect(text(page.querySelector("[data-testid=pr-step]"))).toContain("#12");
  });

  test("new work's pull requests stay with the attempt that opened them", async () => {
    localStorage.setItem("dude.fixtures.run", "restarted");
    const page = await taskPage(new AdoptedClient({ source: null }, RESTARTED_PULL_REQUESTS, "a"));
    expect(panels(page)).toEqual(["pr_483"]);
    await pick(page, 1);
    await until(() => panels(page)[0] === "pr_478" || null, "attempt 1's own pull request");
    expect(panels(page)).toEqual(["pr_478"]);
  });

  test("what happened on the adopted pull request is the attempt's under way then, not the one its branch might name", () => {
    // Two attempts; attempt 2 began after the first review of attempt 1.
    const named = { ...ADOPTED, headBranch: "dude/task_wc214/attempt-1" };
    const at = (iso: string) => ({ eventType: "pull_request.commented", occurredAt: iso, runId: null, payload: { number: 12, repo: named.repositoryName } }) as unknown as PersistedEvent;
    const starts = RESTARTED_RUNS.filter((r) => r.phase === "implement").map((r) => [r.attempt, r.createdAt] as const);
    const second = starts.find(([n]) => n === 2)![1];
    const later = new Date(Date.parse(second) + 60_000).toISOString();
    expect(eventAttempts(RESTARTED_RUNS, [named], 2, PR_SOURCE)(at(later))).toBe(2);
    // Without the source it is the attempt its branch names, as for any pull request no Run is known to have opened.
    expect(eventAttempts(RESTARTED_RUNS, [named], 2, null)(at(later))).toBe(1);
  });
});
