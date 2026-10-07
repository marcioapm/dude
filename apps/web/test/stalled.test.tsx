/**
 * A phase Run that made no progress, in the web app: on a plain delivery,
 * a banner on the task with dude's facts and its owner's three ways on —
 * Restart (with a note), Leave it, Stop — and in Sessions a "stalled"
 * badge on that Run until it makes progress. A restart, the conductor's or
 * a person's, is a line in Chat.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { PersistedEvent } from "@dude/domain";
import { act, click, mount, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { RUN_ID, STALLED_TEXT, TASK_ID, run } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen } from "../src/screens/TaskScreen.tsx";
import { conductedLines } from "../src/conducted.ts";

let mounted: Array<() => Promise<void>> = [];
beforeEach(() => localStorage.clear());
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  localStorage.clear();
});

async function stalledPage(tab?: "sessions") {
  localStorage.setItem("dude.fixtures.run", "stalled");
  const client = new FixtureClient("a");
  const calls: string[] = [];
  const restart = client.restart.bind(client);
  client.restart = (id, note) => { calls.push(`restart ${id} ${note}`); return restart(id, note); };
  const leave = client.leaveStalled.bind(client);
  client.leaveStalled = (id) => { calls.push(`leave ${id}`); return leave(id); };
  client.abort = async (id) => { calls.push(`abort ${id}`); return { ok: true as const }; };
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} {...(tab ? { tab } : {})} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  return { page: container, calls };
}

describe("a stalled Run's banner", () => {
  test("says dude's facts and offers Restart, Leave it and Stop", async () => {
    const { page } = await stalledPage();
    const banner = await until(() => page.querySelector<HTMLElement>("[data-testid=stalled]"), "the banner");
    expect(banner.dataset.run).toBe(RUN_ID);
    expect(banner.textContent).toContain("has made no progress.");
    expect(banner.textContent).toContain("runs inside the agent: no separate process is expected");
    expect([...banner.querySelectorAll("button[data-testid^=stalled-]")].map((b) => b.textContent)).toEqual(
      ["Open Implement", "Restart it", "Leave it", "Stop the task"]);
  });

  test("Restart sends the note, and the banner goes with the Run it replaced", async () => {
    const { page, calls } = await stalledPage();
    const note = await until(() => page.querySelector<HTMLTextAreaElement>("[data-testid=stalled-note] textarea, textarea[data-testid=stalled-note]"), "the note");
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
      set.call(note, "Read the files yourself.");
      note.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(page.querySelector<HTMLElement>("[data-testid=stalled-restart]")!);
    await until(() => calls.length > 0 && !page.querySelector("[data-testid=stalled]"), "the restart");
    expect(calls).toEqual([`restart ${RUN_ID} Read the files yourself.`]);
  });

  test("Leave it hides the banner; Stop is the abort", async () => {
    const { page, calls } = await stalledPage();
    await until(() => page.querySelector("[data-testid=stalled-leave]"), "the actions");
    await click(page.querySelector<HTMLElement>("[data-testid=stalled-leave]")!);
    await until(() => !page.querySelector("[data-testid=stalled]"), "the banner gone");
    expect(calls).toEqual([`leave ${RUN_ID}`]);

    const again = await stalledPage();
    await until(() => again.page.querySelector("[data-testid=stalled-stop]"), "the actions");
    await click(again.page.querySelector<HTMLElement>("[data-testid=stalled-stop]")!);
    await until(() => again.calls.length > 0, "the stop");
    expect(again.calls).toEqual([`abort ${RUN_ID}`]);
  });
});

describe("a stalled Run in Sessions", () => {
  test("carries a stalled badge, with dude's facts as its title", async () => {
    const { page } = await stalledPage("sessions");
    const badge = await until(() => page.querySelector<HTMLElement>(`[data-run="${RUN_ID}"] [data-testid=session-stalled]`), "the badge");
    expect(badge.textContent).toBe("stalled");
    expect(badge.title).toBe(STALLED_TEXT);
  });
});

describe("a restart in Chat", () => {
  const at = (m: number) => new Date(Date.UTC(2026, 9, 7, 10, m)).toISOString();
  const ev = (payload: Record<string, unknown>, actor: PersistedEvent["actor"]): PersistedEvent => ({ eventId: "evt_rs", cursor: 9,
    eventType: "run.restarted", occurredAt: at(30), organizationId: "org", projectId: "prj", taskId: TASK_ID, runId: "run_old",
    sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload });
  const old = run({ id: "run_old", phase: "review", role: "reviewer", category: "frontend", status: "aborted", conductorRunId: "run_c" });

  test("is the conductor's action line, naming the Run and its note", () => {
    const lines = conductedLines({ decider: "conductor", runs: [old] },
      [ev({ from: "run_old", to: "run_new", note: "Do not use the task tool.", phase: "review", by: "conductor" }, { type: "agent", id: "run_c" })]);
    const line = lines.find((l) => l.kind === "restart");
    expect(line).toMatchObject({ kind: "restart", by: "conductor", to: "run_new",
      text: "The conductor restarted the frontend Run, starting over: Do not use the task tool." });
  });

  test("a person's restart on another tier says so", () => {
    const lines = conductedLines({ decider: "conductor", runs: [old] },
      [ev({ from: "run_old", to: "run_new", note: "", phase: "review", tier: "mtr_big" }, { type: "human", id: "u_1" })]);
    expect(lines.find((l) => l.kind === "restart")).toMatchObject({ by: "person",
      text: "A person restarted the frontend Run on another tier, starting over." });
  });
});
