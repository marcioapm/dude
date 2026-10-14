/**
 * Task relations on the task page: a blocked task's refused start, the
 * notice saying what it waits on, Start anyway sending the override,
 * Start when unblocked, a refusal the server gives in the shared words,
 * the Relations section's groups and remove rule, and the picker's loop.
 * Mounted in happy-dom against the fixture client, whose relations and
 * starts a test sets.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { EventTypes, type AutoStartHow, type PersistedEvent, type RelationSide, type Run, type TaskRelations, type TaskRelationView, type TaskStatus } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { TASK_ID, YOU } from "../src/fixtures/data.ts";
import { PeopleProvider } from "../src/people.tsx";
import { TaskScreen, activityLines } from "../src/screens/TaskScreen.tsx";
import type { TaskTab } from "../src/place.ts";

// Overview is no TaskTab (its URL names no tab), but the screen opens on it when given.
const OVERVIEW = "overview" as TaskTab;
import { conductedLines } from "../src/conducted.ts";
import { taskHistory } from "../src/taskHistory.ts";
import { ApiError, type ChatSent, type StartAnyway, type TaskDetail } from "../src/api/client.ts";
import { proposalOrder } from "../src/screens/SessionScreen.tsx";
import { LitEpicOrder, pickableTasks, useCardStarts, useLitCard } from "../src/screens/Relations.tsx";
import { Board, type BoardCardStartFocus } from "@dude/design-system/components";
import type { BoardCard, NavProject } from "@dude/design-system";
import { useCallback, useState } from "react";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
});

function view(key: string, status: TaskStatus, more: Partial<TaskRelationView["task"]> = {}, rel: Partial<TaskRelationView> = {}): TaskRelationView {
  return { task: { id: `wi_${key}`, key, title: `Task ${key}`, status, project: null, unmerged: false, merged: false,
    owner: { id: "u_tom", name: "Tom Okafor" }, ...more },
  createdBy: { kind: "person", name: "Ana" }, createdAt: "2026-10-01T10:00:00Z", why: null, canRemove: true, removableBy: null, ...rel };
}

/** The fixture task, not started, with the relations a test gives it; what it is asked to do is kept. */
class RelationsClient extends FixtureClient {
  starts: Array<{ how: AutoStartHow; start: StartAnyway }> = [];
  autoSet: AutoStartHow[] = [];
  added: Array<{ side: RelationSide; key: string }> = [];
  removed: Array<{ side: RelationSide; key: string }> = [];
  refuseStart: ApiError | null = null;
  refuseAdd: ApiError | null = null;
  constructor(public rel: TaskRelations, private readonly status: TaskStatus = "received") {
    super("a");
  }
  override async getTask(id: string): Promise<TaskDetail> {
    return { ...(await super.getTask(id)), status: this.status, runs: [], decider: "policy", key: "CP-44", owner: { id: YOU, name: "Márcio Martins" } } as unknown as TaskDetail;
  }
  override async relations(): Promise<TaskRelations> {
    return this.rel;
  }
  override async deliver(_id: string, start: StartAnyway = {}) {
    if (this.refuseStart) throw this.refuseStart;
    this.starts.push({ how: "deliver", start });
    return { workflowRunId: "wf_1", alreadyRunning: false };
  }
  override async talk(taskId: string, start: StartAnyway = {}): Promise<ChatSent> {
    if (this.refuseStart) throw this.refuseStart;
    this.starts.push({ how: "talk", start });
    return { runId: "run_c", taskId, created: true, decider: "conductor" };
  }
  override async setAutoStart(_id: string, how: AutoStartHow): Promise<TaskRelations> {
    this.autoSet.push(how);
    this.rel = { ...this.rel, autoStart: { how, by: { id: YOU, name: "Márcio Martins" }, at: new Date().toISOString() } };
    return this.rel;
  }
  override async cancelAutoStart(_id: string): Promise<void> {
    this.rel = { ...this.rel, autoStart: null };
  }
  override async addRelation(_id: string, side: RelationSide, key: string): Promise<TaskRelations> {
    if (this.refuseAdd) throw this.refuseAdd;
    this.added.push({ side, key });
    return this.rel;
  }
  override async removeRelation(_id: string, side: RelationSide, key: string): Promise<void> {
    this.removed.push({ side, key });
  }
}

const rels = (r: Partial<TaskRelations>): TaskRelations => ({ blockedBy: [], blocks: [], related: [], autoStart: null,
  owner: { id: YOU, name: "Márcio Martins" }, ...r });

async function page(client: RelationsClient) {
  const { container, unmount } = await mount(
    <TooltipProvider>
      <ToastProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab={OVERVIEW} />
        </PeopleProvider>
      </ToastProvider>
    </TooltipProvider>,
  );
  mounted.push(unmount);
  await until(() => container.querySelector("[data-testid=task-screen]"), "the task page");
  await settle();
  return container;
}

const byTestId = (id: string) => document.querySelector<HTMLElement>(`[data-testid=${id}]`);

describe("a blocked task's start", () => {
  test("Deliver and Talk it through are refused with the reason; the notice names what it waits on", async () => {
    const p = await page(new RelationsClient(rels({ blockedBy: [view("CP-41", "review"), view("CP-43", "running")] })));
    await until(() => p.querySelector("[data-testid=start-gate]"), "the gate");
    expect(p.querySelector("[data-testid=deliver]")).toBeNull();
    expect(p.querySelector("[data-testid=start-deliver]")).toBeNull();
    const ways = [...p.querySelectorAll<HTMLButtonElement>("[data-testid=start-gate] [data-testid^=gate-] button")];
    expect(ways.map((b) => [b.textContent, b.disabled])).toEqual([["Talk it through", true], ["Deliver", true]]);
    expect(p.querySelector("[data-testid=blocked-notice]")!.textContent).toContain("Blocked by CP-41 (in review) and CP-43 (in progress).");
    expect(p.querySelector("[data-testid=pipeline-blocked]")!.textContent).toContain("Waiting on its blockers");
  });

  test("merged blockers don't block: the ways are offered as on any task", async () => {
    const p = await page(new RelationsClient(rels({ blockedBy: [view("CP-40", "done", { merged: true })] })));
    // On the Overview the ways are its StartChoice cards; the header does not repeat them.
    await until(() => p.querySelector("[data-testid=start-deliver]"), "Deliver");
    expect(p.querySelector("[data-testid=start-gate]")).toBeNull();
    expect(p.querySelector("[data-testid=blocked-notice]")).toBeNull();
  });

  test("Start anyway sends the override and the note, the way picked", async () => {
    const client = new RelationsClient(rels({ blockedBy: [view("CP-41", "review")] }));
    const p = await page(client);
    await click(await until(() => p.querySelector("[data-testid=start-anyway]"), "Start anyway"));
    const dialog = await until(() => document.querySelector("[role=dialog]"), "the dialog");
    expect(dialog.textContent).toContain("Start CP-44 anyway?");
    // Keep waiting holds the focus: overriding is a choice made, not a default.
    expect(document.activeElement?.getAttribute("data-testid")).toBe("keep-waiting");
    const note = dialog.querySelector<HTMLTextAreaElement>("textarea")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(note, "The meter can land after.");
      note.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(byTestId("confirm-start-anyway")!);
    await until(() => client.starts.length > 0 || null, "the start");
    expect(client.starts).toEqual([{ how: "talk", start: { override: true, note: "The meter can land after." } }]);
  });

  test("Start when unblocked sets nothing until a way is picked, then sets it", async () => {
    const client = new RelationsClient(rels({ blockedBy: [view("CP-41", "review")] }));
    const p = await page(client);
    await click(await until(() => p.querySelector("[data-testid=start-when-unblocked]"), "Start when unblocked"));
    const confirm = await until(() => byTestId("confirm-auto-start"), "the dialog's confirm") as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    const deliver = [...document.querySelectorAll<HTMLElement>("[data-testid=auto-how] [role=radio]")].find((r) => r.textContent?.includes("Deliver"))!;
    await click(deliver);
    expect((byTestId("confirm-auto-start") as HTMLButtonElement).disabled).toBe(false);
    await click(byTestId("confirm-auto-start")!);
    await until(() => client.autoSet.length > 0 || null, "the setting");
    expect(client.autoSet).toEqual(["deliver"]);
  });

  test("not the owner: no Start when unblocked, Start anyway still", async () => {
    const client = new RelationsClient(rels({ blockedBy: [view("CP-41", "review")], owner: { id: "u_tom", name: "Tom Okafor" } }));
    const p = await page(client);
    await until(() => p.querySelector("[data-testid=start-anyway]"), "Start anyway");
    expect(p.querySelector("[data-testid=start-when-unblocked]")).toBeNull();
  });

  test("a blocker done with nothing merged: Start when unblocked is refused, saying it would never fire", async () => {
    const p = await page(new RelationsClient(rels({ blockedBy: [view("CP-37", "done", { unmerged: true })] })));
    await until(() => p.querySelector("[data-testid=start-gate]"), "the gate");
    expect(p.querySelector("[data-testid=start-when-unblocked]")).toBeNull();
    expect(p.querySelector("[data-testid=blocked-notice]")!.textContent).toContain("CP-37 was marked done with nothing merged and will never merge.");
    expect(p.querySelector("[data-testid=blocked-notice]")!.textContent).toContain("Blocked by CP-37 (done, nothing merged).");
  });

  test("a start the server refuses as blocked says so in the shared words", async () => {
    // The page read no blockers (an edge added since): the server's refusal is what it says.
    const client = new RelationsClient(rels({}));
    client.refuseStart = new ApiError(409, "blocked", "CP-44 is blocked by CP-41 (in review): it can start once that's merged.",
      { blockers: [{ key: "CP-41", status: "review" }] });
    const p = await page(client);
    await click(await until(() => p.querySelector<HTMLElement>("[data-testid=start-deliver]"), "Deliver"));
    await until(() => p.textContent?.includes("CP-44 is blocked by CP-41 (in review): it can start once that's merged.") || null, "the refusal");
  });
});

describe("the Relations section", () => {
  test("groups with counts; a settled blocker counts as not open; a row the reader may not remove has no ✕", async () => {
    const client = new RelationsClient(rels({
      blockedBy: [view("CP-41", "review"), view("CP-40", "done", { merged: true })],
      blocks: [view("CP-45", "queued", {}, { canRemove: false, removableBy: "Only Tom, CP-45's owner, can remove this" })],
      related: [view("WEB-9", "received", { project: "web-console" })],
    }), "running");
    const p = await page(client);
    const section = await until(() => p.querySelector("[data-testid=relations]"), "Relations");
    const heads = [...section.querySelectorAll("[role=group]")].map((g) => [g.getAttribute("data-group"), g.querySelectorAll("li").length]);
    expect(heads).toEqual([["blocked-by", 2], ["blocks", 1], ["related", 1]]);
    expect(section.querySelector("[data-group=blocked-by]")!.textContent).toContain("1 of 2 open");
    const removes = [...section.querySelectorAll<HTMLButtonElement>("button[aria-label^=Remove]")].map((b) => b.getAttribute("aria-label"));
    expect(removes).toEqual(["Remove blocked by CP-41", "Remove blocked by CP-40", "Remove related WEB-9"]);
    await click(section.querySelector<HTMLElement>("button[aria-label='Remove blocked by CP-41']")!);
    await until(() => client.removed.length > 0 || null, "the removal");
    expect(client.removed).toEqual([{ side: "blocked_by", key: "CP-41" }]);
  });

  test("a loop is refused naming it, with Make it Related instead", async () => {
    const client = new RelationsClient(rels({}), "running");
    client.refuseAdd = new ApiError(409, "cycle", "That would make a loop: CP-44 → CP-41 → CP-44. A task can't wait on itself.",
      { loop: ["CP-44", "CP-41", "CP-44"] });
    const tree = [{ id: "prj_cp", name: "control-plane", tasks: [
      { id: "wi_41", key: "CP-41", title: "Hourly usage meter", status: "review" as const },
      { id: TASK_ID, key: "CP-44", title: "Billing page", status: "running" as const },
    ] }];
    const { container: p, unmount } = await mount(
      <TooltipProvider>
        <PeopleProvider client={client}>
          <TaskScreen client={client} taskId={TASK_ID} onOpenRun={() => {}} onBack={() => {}} tab={OVERVIEW} tree={tree} />
        </PeopleProvider>
      </TooltipProvider>,
    );
    mounted.push(unmount);
    await click(await until(() => p.querySelector("[data-testid=add-relation]"), "Add relation"));
    await until(() => p.querySelector("[data-testid=relation-picker] input[role=combobox]"), "the picker");
    await settle(250);
    const options = [...p.querySelectorAll<HTMLElement>("[role=option]")];
    // This task is offered, not pickable.
    expect(options.map((o) => (o.textContent?.includes("CP-41") ? "CP-41" : o.textContent?.includes("CP-44") ? "CP-44" : "?"))).toEqual(["CP-41", "CP-44"]);
    expect(options[1]!.getAttribute("aria-disabled")).toBe("true");
    // Enter picks the active option, the first.
    await act(async () => void p.querySelector("[data-testid=relation-picker] input[role=combobox]")!.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    const loop = await until(() => p.querySelector("[data-testid=relation-loop]"), "the loop");
    expect(loop.textContent).toContain("That would make a loop:");
    expect(loop.textContent).toContain("A task can't wait on itself.");
    client.refuseAdd = null;
    await click(loop.querySelector<HTMLElement>("[data-testid=make-related]")!);
    await until(() => client.added.length > 0 || null, "the related edge");
    expect(client.added).toEqual([{ side: "related", key: "CP-41" }]);
  });

  test("the picker offers this epic first, then the project, then other projects", () => {
    const t = (key: string) => ({ id: key, key, title: key, status: "received" as const });
    const out = pickableTasks([
      { id: "web", name: "web-console", tasks: [t("WEB-1")] },
      { id: "cp", name: "control-plane", epics: [{ id: "e1", title: "Billing", tasks: [t("CP-1")] }], tasks: [t("CP-2")] },
    ], { projectId: "cp", epicId: "e1" });
    expect(out.map((x) => [x.key, x.group])).toEqual([["CP-1", "This epic"], ["CP-2", "control-plane"], ["WEB-1", "Other projects"]]);
  });
});

describe("a Backlog card's start", () => {
  const owner = { id: YOU, name: "Márcio Martins" };
  const project: NavProject = { id: "prj_cp", name: "control-plane", tasks: [
    { id: "wi_47", key: "CP-47", title: "Plan picker copy", status: "received", people: [owner], canStart: true, startable: true },
    { id: "wi_44", key: "CP-44", title: "Billing page", status: "received", people: [{ id: "u_tom", name: "Tom Okafor" }], canStart: true,
      blockedBy: [{ key: "CP-41", title: "Hourly usage meter", status: "review" }] },
    { id: "wi_41", key: "CP-41", title: "Hourly usage meter", status: "review", people: [owner] },
  ] };
  function BoardWithStarts({ client, started }: { client: RelationsClient; started: string[] }) {
    const starts = useCardStarts(client, YOU, (id) => started.push(id), () => undefined);
    return (
      <>
        <Board project={project} you={YOU} cardStart={(card, focus) => starts.start(card.task, focus)} />
        {starts.dialogs}
      </>
    );
  }
  const menuItem = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=menuitem]")].find((i) => i.textContent?.startsWith(label));
  const openMenu = async (el: HTMLElement) => act(async () => {
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" }));
    el.click();
  });

  test("a card that can start starts the way picked, with no override; one that can't has no start", async () => {
    const client = new RelationsClient(rels({}));
    const started: string[] = [];
    const { container, unmount } = await mount(<TooltipProvider><BoardWithStarts client={client} started={started} /></TooltipProvider>);
    mounted.push(unmount);
    expect(container.querySelector("[data-testid=start-menu-CP-41]")).toBeNull();
    await openMenu(await until(() => container.querySelector<HTMLElement>("[data-testid=start-menu-CP-47]"), "CP-47's start"));
    await click(await until(() => menuItem("Deliver"), "Deliver"));
    await until(() => started.length > 0 || null, "the start");
    expect(client.starts).toEqual([{ how: "deliver", start: {} }]);
    expect(started).toEqual(["wi_47"]);
  });

  test("a blocked card's ways are refused; Start anyway confirms, then starts it past its blockers", async () => {
    const client = new RelationsClient(rels({}));
    const { container, unmount } = await mount(<TooltipProvider><BoardWithStarts client={client} started={[]} /></TooltipProvider>);
    mounted.push(unmount);
    await openMenu(await until(() => container.querySelector<HTMLElement>("[data-testid=start-menu-CP-44]"), "CP-44's start"));
    const deliver = await until(() => menuItem("Deliver"), "Deliver");
    expect(deliver.getAttribute("aria-disabled") === "true" || deliver.hasAttribute("data-disabled")).toBe(true);
    // Not the owner's: Start when unblocked is Tom's to set.
    expect(document.body.textContent).toContain("Only Tom, its owner, can set this.");
    await click(menuItem("Start anyway")!);
    await until(() => document.querySelector("[data-testid=confirm-start-anyway]"), "the dialog");
    await click(byTestId("confirm-start-anyway")!);
    await until(() => client.starts.length > 0 || null, "the start");
    expect(client.starts).toEqual([{ how: "talk", start: { override: true } }]);
  });

  for (const action of ["Deliver", "Talk it through", "Cancel start when unblocked"]) {
    test(`a render redraws no cards and ${action} uses the latest callback`, async () => {
      const client = new RelationsClient(rels({}));
      const started: Array<{ generation: number; id: string; how: string }> = [];
      const changed: number[] = [];
      const boardProject: NavProject = action.startsWith("Cancel") ? { ...project, tasks: project.tasks!.map((task) => task.id === "wi_47"
        ? { ...task, blockedBy: [{ key: "CP-41", title: "Hourly usage meter", status: "review" as const }], autoStart: { how: "deliver", by: owner, at: new Date().toISOString() } } : task) } : project;
      const renders = new Map<string, number>();
      const marks = (card: BoardCard) => {
        renders.set(card.task.key!, (renders.get(card.task.key!) ?? 0) + 1);
        return null;
      };
      let rerender = () => {};
      // As App holds it: new onStarted and onChanged closures on every render.
      function Shell() {
        const [n, setN] = useState(0);
        rerender = () => setN((x) => x + 1);
        const starts = useCardStarts(client, YOU, (id, how) => started.push({ generation: n, id, how }), () => changed.push(n));
        const startCard = starts.start;
        const cardStart = useCallback((card: BoardCard, focus: BoardCardStartFocus) => startCard(card.task, focus), [startCard]);
        return <Board project={boardProject} you={YOU} cardMarks={marks} cardStart={cardStart} />;
      }
      const { container, unmount } = await mount(<TooltipProvider><Shell /></TooltipProvider>);
      mounted.push(unmount);
      await until(() => container.querySelector("[data-testid=start-menu-CP-47]"), "CP-47's start");
      renders.clear();
      await act(async () => rerender());
      expect(Object.fromEntries(renders)).toEqual({});
      await openMenu(container.querySelector<HTMLElement>("[data-testid=start-menu-CP-47]")!);
      await click(await until(() => menuItem(action), action));
      if (action.startsWith("Cancel")) {
        await until(() => changed.length > 0 || null, "the latest changed callback");
        expect(changed).toEqual([1]);
      } else {
        await until(() => started.length > 0 || null, "the latest started callback");
        expect(started).toEqual([{ generation: 1, id: "wi_47", how: action === "Deliver" ? "deliver" : "talk" }]);
      }
    });
  }
});

describe("an epic's order beside its board", () => {
  test("hovering a card lights it and its relations in the order, through the lit card alone", async () => {
    const epic = { id: "epc_1", title: "Billing", tasks: [
      { id: "wi_41", key: "CP-41", title: "Meter", status: "review" as const, blocks: [{ key: "CP-44", title: "Billing page", status: "received" as const }],
        edges: { blockedBy: [], blocks: ["CP-44"], related: [] } },
      { id: "wi_44", key: "CP-44", title: "Billing page", status: "received" as const, blockedBy: [{ key: "CP-41", title: "Meter", status: "review" as const }],
        edges: { blockedBy: ["CP-41"], blocks: [], related: [] } },
      { id: "wi_47", key: "CP-47", title: "Copy", status: "received" as const, startable: true, edges: { blockedBy: [], blocks: [], related: [] } },
    ] };
    const project: NavProject = { id: "prj_cp", name: "control-plane", epics: [epic] };
    let outerRenders = 0;
    function Shell() {
      outerRenders++;
      const lit = useLitCard();
      return <Board project={project} epic={epic} onHighlightChange={lit.set} overview={<LitEpicOrder lit={lit} project={project} epic={epic} />} />;
    }
    const { container, unmount } = await mount(<TooltipProvider><Shell /></TooltipProvider>);
    mounted.push(unmount);
    const before = outerRenders;
    const item = container.querySelector('[data-board-key="task:wi_41"]')!.closest("li")!;
    const strip = (k: string) => container.querySelector<HTMLElement>(`[data-testid=dependency-strip] [data-key="${k}"]`)!;
    await act(async () => item.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    // Lit: CP-41 and CP-44; CP-47 dimmed.
    expect(["CP-41", "CP-44", "CP-47"].map((k) => strip(k).hasAttribute("data-dim"))).toEqual([false, false, true]);
    // The shell that holds the lit card did not render again for the hover.
    expect(outerRenders).toBe(before);
  });
});

describe("a proposal's order", () => {
  test("items step after what they wait on, by either side's word", () => {
    expect(proposalOrder({ items: [
      { kind: "task", title: "a" },
      { kind: "task", title: "b", blockedBy: [{ item: 1 }] },
      { kind: "task", title: "c", blockedBy: [{ item: 2 }] },
      { kind: "task", title: "d", blocks: [{ item: 3 }] },
    ] })).toEqual([[1, 4], [2], [3]]);
  });
});

let cursor = 0;
function ev(eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = { type: "system", id: "dude" }, runId: string | null = null): PersistedEvent {
  cursor++;
  return { eventId: `evt_r${cursor}`, cursor, eventType, occurredAt: new Date(Date.UTC(2026, 9, 3, 10, 0, cursor)).toISOString(), organizationId: "org",
    projectId: "prj", taskId: TASK_ID, runId, sessionId: null, workflowRunId: null, actor, source: "orchestrator", correlationId: null, causationId: null, payload };
}
const people = { names: new Map([[YOU, "Márcio Martins"], ["u_tom", "Tom Okafor"]]), byId: new Map(), you: YOU } as unknown as Parameters<typeof activityLines>[1];
const said = (lines: ReturnType<typeof activityLines>) => lines.map((l) => renderToStaticMarkup(<>{l.text}</>).replace(/<[^>]+>/g, "").replace(/&#x27;/g, "'"));

describe("the task's history", () => {
  test("Activity says each relation event from this task's side, by name", () => {
    const me = { type: "human" as const, id: YOU };
    const lines = activityLines([
      ev(EventTypes.TaskRelationAdded, { kind: "blocks", from: "CP-41", to: "CP-44" }, me),
      ev(EventTypes.TaskRelationRemoved, { kind: "related", from: "CP-44", to: "WEB-9" }, me),
      ev(EventTypes.TaskAutoStartSet, { how: "deliver" }, me),
      ev(EventTypes.TaskUnblocked, { by: "CP-41", pr: 431 }),
      ev(EventTypes.TaskAutoStarted, { by: "CP-41", pr: 431, how: "deliver", setBy: YOU, setByName: "Márcio Martins" }),
      ev(EventTypes.TaskBlockerUnmerged, { by: "CP-37", person: "u_tom" }),
      ev(EventTypes.TaskStartOverridden, { how: "talk", blockers: [{ key: "CP-38", status: "failed" }], note: "Plan limits can wait." }, me),
      ev(EventTypes.TaskAutoStartCancelled, { how: "deliver", why: "owner_changed" }, me),
    ], people, [], [], 1, "CP-44");
    expect(said(lines).reverse()).toEqual([
      "Márcio Martins added blocked by CP-41",
      "Márcio Martins removed related WEB-9",
      "Márcio Martins set it to start when unblocked — Deliver",
      "CP-41 merged (#431) — it can start",
      "CP-41 merged (#431) — it started (Deliver), as Márcio asked. Every blocker was checked again: none open.",
      "CP-37 was marked done by Tom Okafor with nothing merged. It still blocks this: only merged work releases what waits on it.",
      "Márcio Martins started it anyway, past CP-38 (failed) — Talk it through",
      "Márcio Martins handed it to someone else, which ends start when unblocked: it was the old owner's to set",
    ]);
    expect(lines.find((l) => l.quote === "Plan limits can wait.")).toBeDefined();
  });

  test("an agent's relation is its agent's, with its reason quoted", () => {
    const run = { id: "run_c", role: "conductor", phase: null } as unknown as Run;
    const lines = activityLines([ev(EventTypes.TaskRelationAdded, { kind: "blocks", from: "CP-40", to: "CP-44", why: "Needs the schema." },
      { type: "agent", id: "run_c" }, "run_c")], people, [run], [], 1, "CP-44");
    expect(said(lines)).toEqual(["The conductor added blocked by CP-40"]);
    expect(lines[0]!.quote).toBe("Needs the schema.");
  });

  test("Chat shows dude's relation notices on a task with no conductor", () => {
    const lines = conductedLines({ decider: "policy", runs: [] }, [
      ev(EventTypes.ChatNotice, { about: "unblocked", text: "CP-41 merged — CP-44 can start. Nothing started it: Deliver or Talk it through when you're ready." }),
      ev(EventTypes.ChatNotice, { about: "ready_to_merge", text: "Ready to merge." }),
    ]);
    expect(lines.map((l) => l.kind === "notice" && l.text)).toEqual(["CP-41 merged — CP-44 can start. Nothing started it: Deliver or Talk it through when you're ready."]);
  });

  test("a start when unblocked that could not start is said in Chat and Activity, and is no longer set", () => {
    const text = "CP-41 merged, but CP-44 could not start: policy: bad. Start it from its page.";
    const chat = conductedLines({ decider: "policy", runs: [] }, [ev(EventTypes.ChatNotice, { about: "auto_start_failed", text })]);
    expect(chat.map((l) => l.kind === "notice" && l.text)).toEqual([text]);
    const lines = activityLines([ev(EventTypes.TaskAutoStartCancelled, { how: "deliver", why: "failed", error: "policy: bad" })], people, [], [], 1, "CP-44");
    expect(said(lines)).toEqual(["Start when unblocked could not start it: policy: bad. It is no longer set: start it from here."]);
  });

  test("Chat's head says it was released by a merge: ready to start, or started when unblocked", () => {
    const fmt = (n: number) => `$${n}`;
    const waiting = taskHistory({ status: "received", runs: [] }, [], [], null, fmt, [ev(EventTypes.TaskUnblocked, { by: "CP-41", pr: 431 })]);
    expect(waiting).toEqual({ lead: "Not started", steps: ["ready to start"], facts: ["was blocked by CP-41"] });
    const impl = { id: "r1", phase: "implement", attempt: 1, status: "running", createdAt: "2026-10-03T10:00:00Z" } as Run;
    const started = taskHistory({ status: "running", runs: [impl] }, [], [], null, fmt,
      [ev(EventTypes.TaskAutoStarted, { by: "CP-41", how: "deliver" })]);
    expect(started).toEqual({ lead: "Started when unblocked", steps: ["Deliver", "implementer"], facts: ["was blocked by CP-41"] });
  });
});
