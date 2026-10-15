/**
 * A task that took over existing work — an open pull request or a branch —
 * as its page shows it. New work is shown as before. Mounted in happy-dom
 * against the fixture client.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { Decider, PersistedEvent } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { PULL_REQUEST, RESTARTED_PULL_REQUESTS, RESTARTED_RUNS, TASK_ID } from "../src/fixtures/data.ts";
import { ApiClient, ApiError, type EscalationAction, type PullRequest, type TaskDetail, type TaskSource } from "../src/api/client.ts";
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
const PR_SOURCE: TaskSource = { kind: "pr", repositoryId: "repo_wc", repository: "web-console", number: 12, title: "Greet in Portuguese", headBranch: HEAD, baseBranch: "main", mode: "review" };
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
const BRANCH_SOURCE: TaskSource = { kind: "branch", repositoryId: "repo_wc", repository: "web-console", headBranch: "stack/top", baseBranch: "stack/base", mode: "review" };

describe("where the task came from, on Overview", () => {
  test("an adopted pull request: #N title linking to GitHub, and head → base in mono", async () => {
    const page = await taskPage(new AdoptedClient({ source: PR_SOURCE }, [ADOPTED]));
    const fact = page.querySelector<HTMLElement>("[data-testid=task-source-fact]")!;
    expect(text(fact.closest("div"))).toBe("Starts from#12 Greet in Portuguese · feature/greet → main");
    expect(fact.querySelector<HTMLAnchorElement>("[data-testid=task-source-link]")!.href).toBe("https://github.com/example/web-console/pull/12");
    expect([...fact.querySelectorAll(".ds-mono")].map((m) => m.textContent)).toEqual(["feature/greet", "main"]);
  });

  test("an adopted branch: head → base, no link", async () => {
    const page = await taskPage(new AdoptedClient({ source: BRANCH_SOURCE }, []));
    const fact = page.querySelector<HTMLElement>("[data-testid=task-source-fact]")!;
    expect(text(fact)).toBe("stack/top → stack/base");
    expect(fact.querySelectorAll("a").length).toBe(0);
  });

  test("new work says nothing about a source", async () => {
    const page = await taskPage(new AdoptedClient({ source: null }, [PULL_REQUEST]));
    expect(page.querySelectorAll("[data-testid=task-source-fact]").length).toBe(0);
    expect(text(page.querySelector("[data-testid=task-facts]"))).not.toContain("Starts from");
  });
});

const phases = (page: HTMLElement) => [...page.querySelectorAll("[data-testid=phase]")].map((p) => p.getAttribute("data-phase"));

describe("the pipeline of adopted work", () => {
  test("an adopted pull request starts at review, with no Implement step, and its pull request is already open", async () => {
    const page = await taskPage(new AdoptedClient({ source: PR_SOURCE }, [ADOPTED]));
    expect(phases(page)).toEqual(["review", "fix", "review", "simplify"]);
    expect(page.querySelectorAll("[data-phase=implement]").length).toBe(0);
    expect(page.textContent).not.toContain("skipped");
    const step = page.querySelector("[data-testid=pr-step]")!;
    expect(step.getAttribute("data-adopted")).toBe("true");
    expect(text(step)).toStartWith("Pull request · #12 already open");
  });

  test("an adopted branch's pull request, once opened, is the ordinary step", async () => {
    const opened = { ...ADOPTED, id: "pr_13", runId: "run_d5", number: 13, headBranch: "stack/top", baseBranch: "stack/base" };
    const page = await taskPage(new AdoptedClient({ source: BRANCH_SOURCE }, [opened]));
    expect(phases(page)).toEqual(["review", "fix", "review", "simplify"]);
    const step = page.querySelector("[data-testid=pr-step]")!;
    expect(step.getAttribute("data-adopted")).toBeNull();
    expect(text(step)).toStartWith("Pull request #13");
    expect(text(step)).not.toContain("already open");
  });

  test("new work keeps its Implement step and its pull request step", async () => {
    const page = await taskPage(new AdoptedClient({ source: null }, [PULL_REQUEST]));
    expect(phases(page)).toEqual(["implement", "review", "fix", "review", "simplify"]);
    expect(text(page.querySelector("[data-testid=pr-step]"))).toStartWith("Pull request #482");
  });

  test("not started, Deliver promises no implementer for existing work", async () => {
    const said = async (source: TaskSource | null) => {
      const client = new AdoptedClient({ source, status: "received", runs: [] }, []);
      const { container, unmount } = await mount(
        <TooltipProvider><ToastProvider><PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
        </PeopleProvider></ToastProvider></TooltipProvider>,
      );
      mounted.push(unmount);
      const card = await until(() => container.querySelector("[data-testid=start-choice] [data-option=deliver]"), "the Deliver card");
      return text(card);
    };
    expect(await said(PR_SOURCE)).toContain("The automatic pipeline: reviewers, a fixer if they find problems, a simplifier, then pull request #12's feedback.");
    expect(await said(BRANCH_SOURCE)).toContain("The automatic pipeline: reviewers, a fixer if they find problems, a simplifier, then a pull request once you say Open or Draft.");
    expect(await said(null)).toContain("The automatic pipeline: an implementer, reviewers, a fixer if they find problems, a simplifier, then a pull request.");
  });
});

describe("the pull request panel", () => {
  test("an adopted pull request says, muted, that it is existing and dude opened none", async () => {
    const page = await taskPage(new AdoptedClient({ source: PR_SOURCE }, [ADOPTED]));
    const panel = page.querySelector("[data-testid=pr-panel][data-pr=pr_12]")!;
    expect(text(panel.querySelector("[data-testid=pr-adopted-note]"))).toBe("Existing pull request: dude pushes to it and opened none");
  });

  test("the note stays beside why merge waits", async () => {
    const failing: PullRequest = { ...ADOPTED, checks: [], review: "approved", reviews: [], mergeable: "clean", unresolvedThreads: 0,
      checkState: "failing", display: "ci_red" };
    const page = await taskPage(new AdoptedClient({ source: PR_SOURCE }, [failing]));
    const panel = page.querySelector("[data-testid=pr-panel][data-pr=pr_12]")!;
    expect(text(panel.querySelector("[data-testid=pr-adopted-note]"))).toBe("Existing pull request: dude pushes to it and opened none");
    expect(text(panel.querySelector("[data-testid=pr-blocked]"))).toBe("Blocked: checks failing");
  });

  test("a pull request dude opened has no such note, adopted branch or new work", async () => {
    const opened = { ...ADOPTED, id: "pr_13", runId: "run_d5", number: 13, headBranch: "stack/top" };
    for (const [source, prs] of [[BRANCH_SOURCE, [opened]], [null, [PULL_REQUEST]]] as const) {
      const page = await taskPage(new AdoptedClient({ source }, [...prs]));
      expect(page.querySelectorAll("[data-testid=pr-adopted-note]").length).toBe(0);
      expect(page.textContent).not.toContain("Existing pull request");
    }
  });
});

describe("an adopted branch waiting for Open or Draft under Deliver", () => {
  class Held extends AdoptedClient {
    deciders: Array<{ decider: Decider; openPullRequest: boolean; draft: boolean | undefined }> = [];
    override async setDecider(taskId: string, decider: Decider, openPullRequest = false, draft?: boolean) {
      this.deciders.push({ decider, openPullRequest, draft });
      this.patch = { ...this.patch, awaitingDecision: null, status: "running" };
      return { taskId, decider };
    }
  }
  const held = (owner?: TaskDetail["owner"]) => new Held({ source: BRANCH_SOURCE, status: "awaiting_input", decider: "policy",
    awaitingDecision: { point: "before_pull_request" }, ...(owner ? { owner } : {}) }, []);

  for (const [button, draft] of [["Open", false], ["Draft", true]] as const) {
    test(`${button} hands it to Deliver with openPullRequest and draft ${draft}`, async () => {
      const client = held();
      const page = await taskPage(client);
      const card = page.querySelector<HTMLElement>("[data-testid=open-or-draft]")!;
      expect(text(card.querySelector("p"))).toBe("Open the pull request? Review is done on stack/top. dude opens its pull request into stack/base once you say how.");
      expect([...card.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Open", "Draft"]);
      await click(card.querySelector(`[data-testid=open-or-draft-${draft ? "draft" : "open"}]`)!);
      await until(() => !page.querySelector("[data-testid=open-or-draft]") || null, "the card to go once answered");
      expect(client.deciders).toEqual([{ decider: "policy", openPullRequest: true, draft }]);
    });
  }

  test("only its owner is offered Open and Draft; anyone else reads whom it waits on", async () => {
    const client = held({ id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: false });
    const page = await taskPage(client);
    const card = page.querySelector<HTMLElement>("[data-testid=open-or-draft]")!;
    expect(card.querySelectorAll("button").length).toBe(0);
    expect(text(card.querySelector(".escalationWaiting"))).toBe("Waiting for Ana Ribeiro to decide.");
    expect(client.deciders).toEqual([]);
  });

  const buttons = (page: HTMLElement) => [...page.querySelectorAll<HTMLButtonElement>("[data-testid=open-or-draft] button")];

  test("refused, it says why, keeps the notice, and Open and Draft work again", async () => {
    const client = held();
    const succeed = client.setDecider.bind(client);
    client.setDecider = async () => { throw new ApiError(502, "github_unavailable", "GitHub unavailable"); };
    const page = await taskPage(client);
    await click(page.querySelector("[data-testid=open-or-draft-open]")!);
    const alert = await until(() => page.querySelector("[data-testid=open-or-draft] [role=alert]"), "the refusal");
    expect(text(alert)).toBe("Could not open the pull request: GitHub unavailable");
    expect(page.querySelectorAll("[data-testid=open-or-draft]").length).toBe(1);
    expect(buttons(page).map((b) => [b.textContent, b.disabled])).toEqual([["Open", false], ["Draft", false]]);
    client.setDecider = succeed;
    await click(page.querySelector("[data-testid=open-or-draft-draft]")!);
    await until(() => !page.querySelector("[data-testid=open-or-draft]") || null, "the card to go after the retry");
    expect(client.deciders).toEqual([{ decider: "policy", openPullRequest: true, draft: true }]);
  });

  test("while the answer is on its way both buttons are disabled and a second click sends nothing", async () => {
    const client = held();
    const succeed = client.setDecider.bind(client);
    let calls = 0;
    let land!: () => void;
    const landed = new Promise<void>((resolve) => { land = resolve; });
    client.setDecider = async (...args: Parameters<Held["setDecider"]>) => {
      calls++;
      await landed;
      return succeed(...args);
    };
    const page = await taskPage(client);
    await click(page.querySelector("[data-testid=open-or-draft-open]")!);
    expect(buttons(page).map((b) => b.disabled)).toEqual([true, true]);
    await click(page.querySelector("[data-testid=open-or-draft-open]")!);
    await click(page.querySelector("[data-testid=open-or-draft-draft]")!);
    await settle();
    expect(calls).toBe(1);
    expect(client.deciders).toEqual([]);
    await act(async () => land());
    await until(() => !page.querySelector("[data-testid=open-or-draft]") || null, "the card to go once answered");
    expect(calls).toBe(1);
    expect(client.deciders).toEqual([{ decider: "policy", openPullRequest: true, draft: false }]);
  });

  test("an adopted pull request, new work, or a conductor deciding gets no Open or Draft card", async () => {
    for (const patch of [{ source: PR_SOURCE }, { source: null }, { source: BRANCH_SOURCE, decider: "conductor" as const }]) {
      const page = await taskPage(new AdoptedClient({ decider: "policy", awaitingDecision: { point: "before_pull_request" }, ...patch }, []));
      expect(page.querySelectorAll("[data-testid=open-or-draft]").length).toBe(0);
    }
  });

  test("the request carries openPullRequest and draft as the person chose", async () => {
    const sent: unknown[] = [];
    const original = globalThis.fetch;
    globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
      sent.push([new URL(String(input), "http://localhost").pathname, JSON.parse(String(init?.body))]);
      return Response.json({ taskId: "wi_1", decider: "policy" });
    }) as typeof fetch;
    try {
      const api = new ApiClient({ apiKey: "k" });
      await api.setDecider("wi_1", "policy", true, true);
      await api.setDecider("wi_1", "policy", true, false);
      await api.setDecider("wi_1", "policy", true);
      await api.setDecider("wi_1", "policy");
    } finally {
      globalThis.fetch = original;
    }
    expect(sent).toEqual([
      ["/v1/tasks/wi_1/decider", { decider: "policy", openPullRequest: true, draft: true }],
      ["/v1/tasks/wi_1/decider", { decider: "policy", openPullRequest: true, draft: false }],
      ["/v1/tasks/wi_1/decider", { decider: "policy", openPullRequest: true }],
      ["/v1/tasks/wi_1/decider", { decider: "policy" }],
    ]);
  });
});

describe("a source that changed on GitHub", () => {
  // The orchestrator's reason, as delivery/adopt.go's sourceChanged writes it.
  const REASON = "GitHub's pull request (or branch) no longer matches what this task adopted: it was feature/greet → main, and is now feature/greet → stack/base. Put it back on GitHub and Try again, or stop this task and adopt it in a new one.";
  class Escalated extends AdoptedClient {
    decided: EscalationAction[] = [];
    override async decide(_taskId: string, action: EscalationAction, _note: string) {
      this.decided.push(action);
      return { action };
    }
  }

  for (const action of ["retry", "stop"] as const) {
    test(`is an escalation like any other: its reason in full, then Try again and Stop (${action})`, async () => {
      const client = new Escalated({ source: PR_SOURCE, status: "awaiting_input",
        escalation: { reason: "source_changed", detail: { reason: REASON }, actions: ["retry", "stop"], at: new Date().toISOString() } }, [ADOPTED]);
      const page = await taskPage(client);
      const panel = page.querySelector<HTMLElement>("[data-testid=escalation]")!;
      expect(panel.getAttribute("data-reason")).toBe("source_changed");
      expect(text(panel.querySelector("p"))).toBe(`Adopted work changed on GitHub. ${REASON}`);
      expect([...panel.querySelectorAll<HTMLButtonElement>("button[data-testid^=escalation-]")].map((b) => b.textContent)).toEqual(["Try again", "Stop"]);
      await click(panel.querySelector(`[data-testid=escalation-${action}]`)!);
      await settle();
      expect(client.decided).toEqual([action]);
    });
  }
});

describe("Start over on adopted work", () => {
  async function restartDialog(source: TaskSource | null) {
    localStorage.setItem("dude.fixtures.run", "aborted");
    const client = new AdoptedClient({ source, status: "aborted" }, source?.kind === "pr" ? [ADOPTED] : [], "a");
    client.recoveryOptions = async () => ({ taskId: TASK_ID, actions: ["resume", "retry", "restart"], attempt: 1, keptUntil: null });
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider>,
    );
    mounted.push(unmount);
    await click(await until(() => container.querySelector("[data-testid=recover-restart]"), "Start over…"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the dialog");
    const way = dialog.querySelector<HTMLElement>(".pickUpWay[data-shown=true] ul")!;
    const lines = [...way.querySelectorAll("li")].map((li) => [...li.children].map((c) => text(c)));
    const option = [...dialog.querySelectorAll<HTMLElement>("[role=radio]")].find((o) => o.textContent?.startsWith("Start over"))!;
    return { lines, said: text(way), option: text(option) };
  }

  test("an adopted pull request: the same branch and pull request, kept, never a new dude/… branch or pull request", async () => {
    const { lines, said, option } = await restartDialog(PR_SOURCE);
    expect(option).toBe("Start over as attempt 2The same pull request and branch, reviewed again from the start.");
    expect(lines).toEqual([
      ["Conversation", "new", "Fresh agents, told the task as it is now — edit it first if what it asks for was the problem."],
      ["Workspace", "new", "A clean checkout of feature/greet as GitHub has it."],
      ["Branch", "kept", "feature/greet, and pull request #12 on it, which stays open."],
      ["Session", "new", "Attempt 2, from review again. Attempt 1's sessions stay, marked as such."],
    ]);
    for (const words of [said, option]) {
      expect(words).not.toContain("new branch");
      expect(words).not.toContain("dude/");
      expect(words).not.toContain("closed");
      expect(words).not.toContain("default branch");
    }
  });

  test("an adopted branch: the same branch into its base; a pull request dude opened from it is closed", async () => {
    const { lines, option } = await restartDialog(BRANCH_SOURCE);
    expect(option).toBe("Start over as attempt 2The same branch, reviewed again from the start.");
    expect(lines[2]).toEqual(["Branch", "kept", "stack/top, into stack/base. A pull request dude opened from it is closed."]);
    expect(lines[1]).toEqual(["Workspace", "new", "A clean checkout of stack/top as GitHub has it."]);
  });

  test("new work keeps its words: a new branch, and the last pull request closed", async () => {
    const { lines, option } = await restartDialog(null);
    expect(option).toBe("Start over as attempt 2A new branch from the default branch, and the whole pipeline again.");
    expect(lines[2]).toEqual(["Branch", "new", "A new branch for attempt 2. Attempt 1's is kept; a pull request on it is closed."]);
  });
});

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
    expect(text(page.querySelector("[data-testid=pr-adopted-note]"))).toBe("Existing pull request: dude pushes to it and opened none");
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

  // Attempt 1's pull request, closed after the restart by the GitHub sync (system/forge), seen from attempt 1.
  async function closedAfterRestart(source: TaskSource | null, pr: PullRequest) {
    localStorage.setItem("dude.fixtures.run", "restarted");
    const restartAt = RESTARTED_RUNS.find((r) => r.attempt === 2 && r.phase === "implement")!.createdAt;
    const closedAt = new Date(Date.parse(restartAt) + 60_000).toISOString();
    class Closed extends AdoptedClient {
      override events() {
        const events = [
          { eventId: "restart", cursor: 1, eventType: "task.recovered", occurredAt: restartAt, runId: null,
            taskId: TASK_ID, actor: { type: "system" }, payload: { action: "restart", attempt: 2 } },
          { eventId: "closed", cursor: 2, eventType: "pull_request.closed", occurredAt: closedAt, runId: null,
            taskId: TASK_ID, actor: { type: "system", id: "forge" }, payload: { number: pr.number, repo: pr.repositoryName } },
        ] as unknown as PersistedEvent[];
        return Promise.resolve({ events, nextCursor: 2 });
      }
    }
    const page = await taskPage(new Closed({ source }, [pr], "a"));
    await pick(page, 1);
    return await until(() => page.querySelector<HTMLElement>(`[data-testid=pr-panel][data-pr=${pr.id}]`), "attempt 1's panel");
  }

  test("an adopted pull request closed on GitHub after a start over keeps its note: recovery never closes it", async () => {
    const panel = await closedAfterRestart(PR_SOURCE, { ...ADOPTED, state: "closed", display: "closed" });
    expect(text(panel.querySelector("[data-testid=pr-adopted-note]"))).toBe("Existing pull request: dude pushes to it and opened none");
    expect(text(panel)).not.toContain("Closed by");
    expect(text(panel)).not.toContain("started over");
  });

  test("new work's pull request closed after a start over is still said to be closed by it", async () => {
    const panel = await closedAfterRestart(null, RESTARTED_PULL_REQUESTS[0]!);
    expect(text(panel)).toMatch(/Closed by \S+ .* ago, when someone started over\./);
    expect(panel.querySelectorAll("[data-testid=pr-adopted-note]").length).toBe(0);
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
