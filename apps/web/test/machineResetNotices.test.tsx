/**
 * A person's machine Reset in the task's Chat and its Activity, as the
 * orchestrator records it: `machine.reset` on the task, with the Run it
 * moved named when one was live (`runId`), or none when only the layer was
 * cleared. Its notice reads "Márcio moved the implementer back to Standard:
 * “<note>”", whoever decides the task.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { EventTypes, type Machine, type MachineResetPayload, type PersistedEvent, type Run } from "@dude/domain";
import { act, mount, settle, until } from "./dom.ts";
import { FixtureClient, type LedgerQuery } from "../src/fixtures/client.ts";
import { RUN_ID, TASK_ID } from "../src/fixtures/data.ts";
import { dudeName } from "../src/DudeMark.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen, activityLines } from "../src/screens/TaskScreen.tsx";
import { conductedLines } from "../src/conducted.ts";
import type { RunDetail, TaskDetail } from "../src/api/client.ts";

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
/** A note-less Reset that moved the conductor's Run. */
function conductorReset(runId: string, s: number): PersistedEvent {
  const e = reset("", runId, s);
  return { ...e, payload: { ...e.payload, role: "conductor" } };
}

describe("a Reset in the task's Chat", () => {
  test("a live Run's Reset and a layer-only one are notices, whoever decides", () => {
    for (const decider of ["policy", "conductor"]) {
      const lines = conductedLines({ decider, runs: [IMPL] }, [reset("fits now", IMPL.id, 1), reset("", null, 2)]);
      expect(notices(lines)).toEqual(["Márcio moved the implementer back to Standard: “fits now”", "Márcio moved the implementer back to Standard."]);
    }
  });

  test("a Reset that moved a conductor, live or ended, is said only in that conductor's own transcript", () => {
    const conductor = run({ id: "run_cond_r", phase: null, role: "conductor" });
    const moved = conductorReset(conductor.id, 1);
    expect(notices(conductedLines({ decider: "conductor", runs: [conductor] }, [moved]))).toEqual([]);
    expect(notices(conductedLines({ decider: "conductor", runs: [{ ...conductor, status: "completed" }] }, [moved]))).toEqual([]);
  });
});

describe("a Reset in the task's Activity", () => {
  const people = { names: new Map([[PERSON, "Márcio"]]), byId: new Map(), you: null } as unknown as Parameters<typeof activityLines>[1];
  const textOf = (node: React.ReactNode) => {
    const div = document.createElement("div");
    div.innerHTML = renderToStaticMarkup(<>{node}</>);
    return div.textContent;
  };
  const plain = (lines: ReturnType<typeof activityLines>) => lines.map((l) => [textOf(l.text), l.quote ?? null, l.open?.runId ?? null]);

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
  override async getRun(id: string): Promise<RunDetail> {
    const added = this.patch.runs?.find((r) => r.id === id);
    if (!added) return super.getRun(id);
    return { ...(await super.getRun(RUN_ID)), ...added } as RunDetail;
  }
  protected override ledgerFor(params: LedgerQuery): PersistedEvent[] {
    const extra = this.extra.filter((e) => e.cursor > (params.after ?? 0) && (!params.runId || e.runId === params.runId));
    return [...super.ledgerFor(params), ...extra];
  }
}

async function chatOf(client: ResetClient): Promise<HTMLElement> {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
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
  return await until(() => container.querySelector<HTMLElement>("[data-testid=task-chat]"), "the task's Chat");
}

/** Each dude notice's line in the Chat as a person reads it: "<dude's name>: <text>". */
const noticeTexts = (chat: HTMLElement) =>
  [...chat.querySelectorAll<HTMLElement>("[role=note]")].map((n) => n.querySelector("b")?.parentElement?.textContent ?? "");

test("a task Deliver runs, with no conductor, shows a layer-only Reset's notice in its Chat", async () => {
  const client = new ResetClient({ status: "running", decider: "policy", runs: [run({ id: "run_done", status: "completed" })] }, [reset("use 2 workers", null, 5)]);
  const chat = await chatOf(client);
  const want = `${dudeName(TASK_ID)}: Márcio moved the implementer back to Standard: “use 2 workers”`;
  await until(() => (noticeTexts(chat).includes(want) ? true : null), "the Reset's notice");
  expect(noticeTexts(chat).filter((t) => t === want)).toEqual([want]);
});

describe("a Reset that moved the conductor, in the task's Chat", () => {
  const want = `${dudeName(TASK_ID)}: Márcio moved the conductor back to Standard.`;
  const cases: Array<[string, Run[]]> = [
    ["live", [run({ id: "run_cond_live", phase: null, role: "conductor" })]],
    ["ended, the latest", [run({ id: "run_cond_done", phase: null, role: "conductor", status: "completed", endedAt: at(9) })]],
    ["ended, before a later one", [
      run({ id: "run_cond_old", phase: null, role: "conductor", status: "completed", createdAt: at(0), endedAt: at(9) }),
      run({ id: "run_cond_new", phase: null, role: "conductor", createdAt: at(10) }),
    ]],
  ];
  for (const [name, runs] of cases) {
    test(`is said once when it is ${name}`, async () => {
      const client = new ResetClient({ status: "running", decider: "conductor", runs }, [conductorReset(runs[0]!.id, 5)]);
      const chat = await chatOf(client);
      await until(() => (noticeTexts(chat).includes(want) ? true : null), "the Reset's notice");
      await settle();
      expect(noticeTexts(chat).filter((t) => t === want)).toEqual([want]);
    });
  }
});
