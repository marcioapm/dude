/**
 * A stopped task, picked back up: the notice that says how it stopped and
 * offers the ways back, the one dialog they open, and — once it has been
 * started over — the earlier attempt folded under the pipeline and the
 * sessions grouped by attempt. Mounted in happy-dom against the fixture
 * client, its task stopped by `dude.fixtures.run`.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID, TASK_ID, taskFor } from "../src/fixtures/data.ts";
import { stopOf } from "../src/screens/Recovery.tsx";
import type { RecoverAction } from "../src/api/client.ts";
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
    <PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...props} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return container;
}

const stoppedAs = (as: "aborted" | "failed" | "restarted") => {
  localStorage.setItem("dude.fixtures.run", as);
  return new FixtureClient("a");
};

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
  test("shows attempt 2's pipeline, and attempt 1 folded under it, whole when shown", async () => {
    const page = await taskPage(stoppedAs("restarted"));
    const heading = await until(() => [...page.querySelectorAll("h2")].find((h) => h.textContent?.startsWith("Pipeline")), "the pipeline");
    expect(heading.textContent).toBe("Pipeline · attempt 2");
    const folded = await until(() => page.querySelector<HTMLElement>("[data-testid=earlier-attempt]"), "attempt 1");
    expect(folded.textContent).toContain("Attempt 1 stopped at Implement, aborted by Ana");
    expect(page.querySelector("[data-testid=stopped]")).toBeNull();
    await click(folded.querySelector("[data-testid=show-attempt]")!);
    const open = await until(() => page.querySelector<HTMLElement>(".attemptOpen"), "attempt 1, open");
    expect(open.textContent).toContain("Ana Ribeiro aborted the implement");
    expect(open.textContent).toContain("Márcio Martins started over");
    expect(open.textContent).toContain("Keep the routing as it is; split the form only.");
  });

  test("groups the sessions by attempt, the earlier one set aside, and says so on it", async () => {
    const page = await taskPage(stoppedAs("restarted"), { runId: RUN_ID });
    const groups = await until(() => {
      const g = [...page.querySelectorAll<HTMLElement>("[data-testid=attempt-sessions]")];
      return g.length === 2 ? g : null;
    }, "two attempts of sessions");
    expect(groups.map((g) => g.querySelector("h3")?.textContent)).toEqual(["Attempt 2current", "Attempt 1set aside"]);
    const strip = await until(() => page.querySelector<HTMLElement>("[data-testid=run-ended]"), "the old session's end strip");
    expect(strip.textContent).toContain("Set aside when its task was started over.");
    expect(strip.querySelector("[data-testid=run-ended-resume]")).toBeNull();
    await settle();
  });

  test("Activity says who picked it back up, and how", async () => {
    const page = await taskPage(stoppedAs("restarted"), { tab: "activity" });
    const line = await until(() => [...page.querySelectorAll("[data-testid=activity-item]")].find((l) => l.textContent?.includes("picked the task back up")),
      "the pick-up in Activity");
    expect(line.textContent).toContain("started over as attempt 2");
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
