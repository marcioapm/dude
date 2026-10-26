/**
 * The held panel for a task on Ask me first: the owner's answers and Done
 * without a pull request with its confirmation; someone else's wait; the
 * feedback sent back (Open refused with how many items are open); round 2;
 * the header's words; and the preview the panel offers. Mounted in
 * happy-dom against the fixture client.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { FeedbackItem, TaskServers } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import type { TaskDetail } from "../src/api/client.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const HOLD = { round: 1, commitsSince: null, since: new Date(Date.now() - 22 * 60_000).toISOString(),
  repos: [{ name: "web", head: "dude/web-142-saved-filters", base: "main", additions: 412, deletions: 38 },
    { name: "api", head: "dude/web-142-saved-filters", base: "main", additions: 96, deletions: 4 }] };

class Held extends FixtureClient {
  finished = 0;
  constructor(private patch: Partial<TaskDetail>, private servers?: TaskServers) {
    super("d");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), ...this.patch } as TaskDetail;
  }
  override listPullRequests() {
    return Promise.resolve({ pullRequests: [] });
  }
  override async doneWithoutPullRequest(taskId: string) {
    this.finished++;
    this.patch = { ...this.patch, status: "done", awaitingDecision: null, hold: null };
    return { taskId, status: "done" as const };
  }
  override taskServers(): Promise<TaskServers> {
    return this.servers ? Promise.resolve(this.servers) : super.taskServers();
  }
}

const atHold = (extra: Partial<TaskDetail> = {}) => ({ status: "awaiting_input" as const, decider: "policy" as const, source: null,
  awaitingDecision: { point: "before_pull_request" as const }, hold: HOLD, pullRequestOpened: false, ...extra });

async function taskPage(client: FixtureClient) {
  const { container, unmount } = await mount(
    <TooltipProvider><ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider></TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle();
  return container;
}

const text = (el: Element | null) => el?.textContent?.replace(/\s+/g, " ").trim() ?? "";
const panel = (page: HTMLElement) => page.querySelector<HTMLElement>("[data-testid=ready-for-review]");
const named = (root: ParentNode, name: string) =>
  [...root.querySelectorAll<HTMLElement>("button, [role=button]")].find((b) => (b.getAttribute("aria-label") ?? text(b)) === name);

describe("a task held on Ask me first", () => {
  test("its owner reads it ready for review, per repository, and may finish without a pull request after confirming", async () => {
    const client = new Held(atHold());
    const page = await taskPage(client);
    const card = await until(() => panel(page), "the panel");
    expect(card.getAttribute("data-state")).toBe("yours");
    expect(text(page.querySelector("[data-testid=header-status]"))).toContain("Ready for your review");
    const repos = [...card.querySelectorAll('[aria-label="What would be opened"] li')];
    expect(repos.map((r) => r.querySelector("b")?.textContent)).toEqual(["web", "api"]);
    expect(text(repos[0]!)).toContain("+412");
    expect(named(card, "Changes in web")).toBeDefined();
    expect(text(card)).toContain("held 22m");

    await click(named(card, "Done without a pull request")!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the confirmation");
    expect(text(dialog)).toContain("Done without a pull request?");
    expect(text(dialog)).toContain("The branch dude/web-142-saved-filters stays on GitHub, in web and api.");
    expect(text(dialog)).toContain("No pull request is opened.");
    await click(named(dialog, "Cancel")!);
    await settle();
    expect(client.finished).toBe(0);

    await click(named(card, "Done without a pull request")!);
    const again = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the confirmation again");
    const confirm = [...again.querySelectorAll<HTMLButtonElement>("button")].find((b) => text(b) === "Done without a pull request")!;
    await click(confirm);
    await until(() => (client.finished === 1 ? true : null), "the request");
    await until(() => !panel(page) || null, "the panel to go");
  });

  test("anyone else waits on the owner, with Write feedback and no answer of theirs", async () => {
    const client = new Held(atHold({ owner: { id: "u_sam", name: "Sam Lee", photoUrl: null, online: false } }));
    const page = await taskPage(client);
    const card = await until(() => panel(page), "the panel");
    expect(card.getAttribute("data-state")).toBe("waiting");
    expect(text(card)).toContain("Ready for Sam's review.");
    expect(text(card)).toContain("Waiting for Sam…");
    expect(named(card, "Write feedback")).toBeDefined();
    for (const n of ["Open pull request", "Open as draft", "Done without a pull request"]) expect(named(card, n)).toBeUndefined();
  });

  test("sent back, a fixer works on the items and Open is refused with how many are open", async () => {
    const client = new Held(atHold({ status: "running", awaitingDecision: null }));
    const item = (id: string): FeedbackItem => ({ id, taskId: TASK_ID, attempt: 1, batchId: "b", author: null, body: "Per project", anchor: { kind: "general" },
      status: "delivered", deliveredRunId: null, resolvedCommit: null, resolutionPending: false, outdated: false, routedTo: "workflow",
      submittedAt: new Date().toISOString(), createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(), history: [] });
    client.feedbackItems = [item("f1"), item("f2")];
    const page = await taskPage(client);
    const card = await until(() => panel(page), "the panel");
    expect(card.getAttribute("data-state")).toBe("sent-back");
    expect(text(page.querySelector("[data-testid=header-status]"))).toContain("Fixing your feedback");
    const open = named(card, "Open pull request")!;
    expect(open.getAttribute("aria-disabled")).toBe("true");
    expect(document.getElementById(open.getAttribute("aria-describedby")!)?.textContent).toBe("2 feedback items still open");
    expect(named(card, "Done without a pull request")).toBeUndefined();
    const chip = await until(() => page.querySelector<HTMLElement>("[data-testid=merge-hold]"), "the header's chip");
    expect(text(chip)).toBe("2 feedback items hold the pull request");
  });

  test("round 2 says how many commits since the owner last looked, and what it resolved", async () => {
    const client = new Held(atHold({ hold: { ...HOLD, round: 2, commitsSince: 3 } }));
    client.feedbackItems = [{ id: "f1", taskId: TASK_ID, attempt: 1, batchId: "b", author: null, body: "Saved filters should be per project.",
      anchor: { kind: "general" }, status: "resolved", deliveredRunId: null, resolvedCommit: "9a1c07e55", resolutionPending: false, outdated: false,
      routedTo: "workflow", submittedAt: new Date().toISOString(), createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(), history: [] }];
    const page = await taskPage(client);
    const card = await until(() => panel(page), "the panel");
    expect(text(card)).toContain("Round 2 · after your feedback");
    expect(text(card)).toContain("+3 commits since you last looked");
    const settled = card.querySelector('[aria-label="Your feedback, resolved"] li')!;
    expect([...settled.children].map((c) => text(c))).toEqual(["", "Saved filters should be per project.", "resolved in 9a1c07e"]);
  });

  test("the Feedback tab, while held, says nothing goes to GitHub and that items hold the pull request's opening", async () => {
    const client = new Held(atHold());
    const { container, unmount } = await mount(
      <TooltipProvider><ToastProvider><PeopleProvider client={client}>
        <TaskScreen client={client} taskId={TASK_ID} tab="feedback" onOpenRun={() => {}} onBack={() => {}} />
      </PeopleProvider></ToastProvider></TooltipProvider>,
    );
    mounted.push(unmount);
    const tab = await until(() => container.querySelector<HTMLElement>("[data-testid=feedback-tab]"), "the Feedback tab");
    expect(text(tab.querySelector("[data-testid=feedback-before-pr]"))).toBe(
      "Feedback before the pull request: nothing goes to GitHub. GitHub's reviewers and CI see none of it; it stays on the task.");
    expect(container.textContent).toContain("What holds the pull request");
    expect(container.textContent).not.toContain("What holds the merge");
  });

  test("the fixture world's held states render the panel each names", async () => {
    for (const [run, state] of [["held", "yours"], ["held-other", "waiting"], ["held-feedback", "sent-back"], ["held-round-2", "yours"]] as const) {
      localStorage.setItem("dude.fixtures.run", run);
      const page = await taskPage(new FixtureClient("d"));
      const card = await until(() => panel(page), `the panel for ${run}`);
      expect([run, card.getAttribute("data-state")]).toEqual([run, state]);
      for (const unmount of mounted.splice(0)) await unmount();
    }
  });

  test("a project whose preview is configured but not running offers Start preview", async () => {
    const servers = { run: null, servers: [], recipes: [{ name: "web", port: 3000, command: "bun dev", workdir: "", setup: null, autostartInPreviews: true }] } as unknown as TaskServers;
    const client = new Held(atHold(), servers);
    const page = await taskPage(client);
    const card = await until(() => panel(page), "the panel");
    await until(() => named(card, "Start preview"), "Start preview");
    const none = new Held(atHold(), { run: null, servers: [], recipes: [] } as unknown as TaskServers);
    const other = await taskPage(none);
    const plain = await until(() => panel(other), "the panel");
    await act(async () => settle());
    expect(named(plain, "Start preview")).toBeUndefined();
  });
});
