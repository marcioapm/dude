/**
 * The task page's pull request card for a conflict, wired from the task:
 * while dude resolves it (its latest integration line, the task running),
 * and for someone who is not the task's owner.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { PersistedEvent } from "@dude/domain";
import { mount, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { PULL_REQUEST, PROJECT, ORG, TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import type { PullRequest, TaskDetail } from "../src/api/client.ts";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { ToastProvider } from "@dude/design-system/primitives";

const CONFLICTING: PullRequest = { ...PULL_REQUEST, mergeable: "conflicting" };

/** Scenario d with its pull request conflicting, owned by `owner`, at `status`, with `events` on its ledger. */
class ConflictClient extends FixtureClient {
  constructor(private readonly owner: string, private readonly status: TaskDetail["status"], private readonly extra: PersistedEvent[]) {
    super("d");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    const t = await super.getTask(id);
    return { ...t, status: this.status, owner: { ...t.owner!, id: this.owner, name: this.owner === "u_ana" ? "Ana Ribeiro" : t.owner!.name } } as TaskDetail;
  }
  override listPullRequests(_taskId: string) {
    return Promise.resolve({ pullRequests: [CONFLICTING] });
  }
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    return [...super.ledgerFor(params), ...this.extra.filter((e) => e.cursor > (params.after ?? 0))];
  }
}

const started: PersistedEvent = {
  cursor: 9_000_000, id: "ev_int", eventType: "pull_request.action", occurredAt: new Date().toISOString(),
  organizationId: ORG.id, projectId: PROJECT.id, taskId: TASK_ID, runId: null, sessionId: null, correlationId: TASK_ID,
  actor: { type: "system", id: "workflow" }, source: "orchestrator",
  payload: { action: "integration-started", repo: PULL_REQUEST.repositoryName, number: PULL_REQUEST.number, base: "main", runId: "run_int" },
} as unknown as PersistedEvent;

const mounted: (() => void)[] = [];
afterEach(() => { while (mounted.length) mounted.pop()!(); });

async function card(client: FixtureClient) {
  const { container, unmount } = await mount(
    <ToastProvider><PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider></ToastProvider>,
  );
  mounted.push(unmount);
  return await until(() => container.querySelector<HTMLElement>("[data-testid=pr-panel]"), "the pull request card");
}

describe("a conflicting pull request's card on the task page", () => {
  test("for its owner, Resolve conflicts", async () => {
    const pr = await card(new ConflictClient("u_marcio", "review", []));
    await until(() => pr.querySelectorAll("[data-testid=pr-resolve-conflicts]").length === 1 ? true : null, "the action");
  });

  test("while dude resolves it, says so and offers nothing", async () => {
    const pr = await card(new ConflictClient("u_marcio", "running", [started]));
    await until(() => pr.textContent!.includes("dude is resolving it") ? true : null, "the resolving line");
    expect(pr.querySelectorAll("[data-testid=pr-resolve-conflicts]").length).toBe(0);
  });

  test("for someone else, whom it waits on, and no action", async () => {
    const pr = await card(new ConflictClient("u_ana", "review", []));
    const waiting = await until(() => pr.querySelector<HTMLElement>("[data-testid=pr-conflict-waiting]"), "the waiting words");
    expect(waiting.textContent).toBe("Waiting for Ana Ribeiro");
    expect(pr.querySelectorAll("[data-testid=pr-resolve-conflicts]").length).toBe(0);
  });
});
