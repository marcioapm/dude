/**
 * A person's machine Reset in the task's Chat and its Activity, as the
 * orchestrator records it: `machine.reset` on the task, with the Run it
 * moved named when one was live (`runId`), or none when only the layer was
 * cleared. Its notice reads "Márcio moved the implementer back to Standard:
 * “<note>”", whoever decides the task.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { EventTypes, type Machine, type MachineResetPayload, type PersistedEvent, type Run } from "@dude/domain";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { TASK_ID } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen, activityLines } from "../src/screens/TaskScreen.tsx";
import { conductedLines } from "../src/conducted.ts";
import type { TaskDetail } from "../src/api/client.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

const at = (s: number) => new Date(Date.UTC(2026, 9, 13, 10, 0, s)).toISOString();
const STANDARD: Machine = { sizeId: "msz_std", name: "Standard", cpus: 2, memoryMiB: 8192, diskGiB: 20, poolId: null, pool: "default", from: "organization" };
const LARGE: Machine = { ...STANDARD, sizeId: "msz_large", name: "Large", cpus: 4, memoryMiB: 16384, diskGiB: 40, from: "task" };
const PERSON = "per_marcio";

function run(p: Partial<Run> & { id: string }): Run {
  return { organizationId: "org", projectId: "prj", taskId: TASK_ID, attempt: 1, status: "running", workerId: null,
    workspacePath: null, error: null, kind: "agent", phase: "implement", role: "implementer", category: null, parentRunId: null,
    conductorRunId: null, baseRefs: {}, heads: {}, branch: null, harness: "scripted", model: "fake/scripted", modelTier: null, dudePause: null,
    tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, context: 0 }, machine: null, image: null, preparingImage: null,
    createdAt: at(0), startedAt: at(0), endedAt: null, ...p } as Run;
}

let cursor = 0;
/** machine.reset as api.resetMachine writes it: on the task, by the person, the moved Run named when there was one. */
function reset(note: string, runId: string | null, s: number): PersistedEvent {
  cursor++;
  const payload: MachineResetPayload = { moveId: runId ? "mmv_r" : "", role: "implementer", from: LARGE, to: STANDARD,
    by: { id: PERSON, name: "Márcio" }, note };
  return { eventId: `evt_r${cursor}`, cursor: 20_000 + cursor, eventType: EventTypes.MachineReset, occurredAt: at(s), organizationId: "org",
    projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null, actor: { type: "person", id: PERSON }, source: "api",
    correlationId: null, causationId: null, payload: payload as unknown as Record<string, unknown> } as PersistedEvent;
}

const IMPL = run({ id: "run_impl_r" });
const notices = (lines: ReturnType<typeof conductedLines>) => lines.flatMap((l) => (l.kind === "notice" ? [l.text] : []));

describe("a Reset in the task's Chat", () => {
  test("a live Run's Reset and a layer-only one are notices, whoever decides", () => {
    for (const decider of ["policy", "conductor"]) {
      const lines = conductedLines({ decider, runs: [IMPL] }, [reset("fits now", IMPL.id, 1), reset("", null, 2)]);
      expect(notices(lines)).toEqual(["Márcio moved the implementer back to Standard: “fits now”", "Márcio moved the implementer back to Standard."]);
    }
  });

  test("a Reset that moved the live conductor is said once, in the conductor's own transcript", () => {
    const conductor = run({ id: "run_cond_r", phase: null, role: "conductor" });
    const moved: PersistedEvent = { ...reset("", conductor.id, 1), payload: { ...reset("", conductor.id, 1).payload, role: "conductor" } };
    expect(notices(conductedLines({ decider: "conductor", runs: [conductor] }, [moved]))).toEqual([]);
    expect(notices(conductedLines({ decider: "conductor", runs: [{ ...conductor, status: "completed" }] }, [moved])))
      .toEqual(["Márcio moved the conductor back to Standard."]);
  });
});

describe("a Reset in the task's Activity", () => {
  const people = { names: new Map([[PERSON, "Márcio"]]), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
  const plain = (lines: ReturnType<typeof activityLines>) =>
    lines.map((l) => [renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, ""), l.quote ?? null, l.open?.runId ?? null]);

  test("a live Run's Reset opens that Run; a layer-only one names no Run; each with its note", () => {
    const lines = activityLines([reset("fits now", IMPL.id, 1), reset("", null, 2)], people, [IMPL], [], 1);
    expect(plain(lines)).toEqual([
      ["Márcio moved the implementer back to Standard", null, null],
      ["Márcio moved the implementer back to Standard", "fits now", IMPL.id],
    ]);
  });
});

/** The fixture task with its Runs and a Reset in its ledger. */
class ResetClient extends FixtureClient {
  constructor(private readonly patch: Partial<TaskDetail>, private readonly extra: PersistedEvent[]) {
    super("d");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), ...this.patch } as TaskDetail;
  }
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    if (params.runId) return super.ledgerFor(params);
    return [...super.ledgerFor(params), ...this.extra.filter((e) => e.cursor > (params.after ?? 0))];
  }
}

test("a task Deliver runs, with no conductor, shows a layer-only Reset's notice in its Chat", async () => {
  const client = new ResetClient({ status: "running", decider: "policy", runs: [run({ id: "run_done", status: "completed" })] }, [reset("use 2 workers", null, 5)]);
  const { container, unmount } = await mount(
    <PeopleProvider client={client}>
      <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
    </PeopleProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle();
  const tab = [...container.querySelectorAll<HTMLElement>("[role=tab]")].find((t) => t.textContent?.startsWith("Chat"))!;
  await act(async () => {
    tab.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, button: 0 }));
    tab.click();
  });
  await settle();
  const notice = await until(() => container.querySelector<HTMLElement>("[data-testid=task-chat] [data-testid=chat-dude-notice]"), "the Reset's notice");
  expect(notice.textContent).toContain("Márcio moved the implementer back to Standard: “use 2 workers”");
});
