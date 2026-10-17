/**
 * Lifecycle's components, mounted in happy-dom (docs/design/tracking-work.md,
 * Epic 1): the marks, the menu's reasons in their order, the Close dialog
 * and Close and archive, the archive confirmations, the duplicate groups in
 * Relations, and a closed card on the board.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Board } from "../src/components/Board.tsx";
import { ArchiveDialog, ArchiveEpicDialog, ArchivedMark, CloseDialog, ResolutionMark, lifecycleMenuItems, type CloseDecision } from "../src/components/Lifecycle.tsx";
import { RelationList } from "../src/components/Relations.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { RowMenuAction } from "../src/primitives/RowMenu.tsx";
import type { NavProject } from "../src/util/navModel.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

async function mount(node: ReactNode): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const NOT_YOURS = "Only Ana, the owner, or an admin can do this";
const items = (...a: Parameters<typeof lifecycleMenuItems>) =>
  lifecycleMenuItems(...a).map((i) => {
    const it = i as RowMenuAction;
    return `${it.label}${it.disabled ? ` — ${it.disabledReason}` : ""}`;
  });

describe("ResolutionMark and ArchivedMark", () => {
  test("say the resolution, a duplicate's canonical key, and Archived, in no tone", async () => {
    const el = await mount(<><ResolutionMark resolution="wont_do" /><ResolutionMark resolution="duplicate" of="CP-62" /><ArchivedMark /></>);
    expect([...el.querySelectorAll('[data-testid="resolution-mark"]')].map((m) => m.textContent)).toEqual(["Won't do", "Duplicate of CP-62"]);
    expect(el.querySelector('[data-testid="archived-mark"]')!.textContent).toBe("Archived");
    expect(el.querySelector('[data-testid="archived-mark"] svg[data-icon="archive"]')).not.toBeNull();
  });
});

describe("the task menu's lifecycle items", () => {
  test("each disabled with its reason, in the order they are checked", () => {
    expect(items({ status: "received" }, {})).toEqual(["Close…", "Archive…"]);
    expect(items({ status: "running" }, {})).toEqual(["Close… — Stop its agents first", "Archive… — Stop its agents first"]);
    expect(items({ status: "awaiting_input" }, {})).toEqual(["Close… — Stop its agents first", "Archive… — Stop its agents first"]);
    expect(items({ status: "review" }, {})).toEqual(["Close… — It has a pull request open", "Archive… — It has a pull request open"]);
    expect(items({ status: "ready_to_merge" }, {})).toEqual(["Close… — It has a pull request open", "Archive… — It has a pull request open"]);
    expect(items({ status: "done", resolution: "nothing_to_merge" }, {})).toEqual(["Closed — Already ended", "Archive…"]);
    // A stopped task with no resolution hasn't ended: Close is offered.
    expect(items({ status: "aborted" }, {})).toEqual(["Close…", "Archive…"]);
    expect(items({ status: "failed" }, {})).toEqual(["Close…", "Archive…"]);
    expect(items({ status: "aborted", resolution: "wont_do" }, {})).toEqual(["Reopen", "Archive…"]);
    expect(items({ status: "aborted", resolution: "wont_do", archived: true }, {})).toEqual(["Reopen — Unarchive it first, then reopen it", "Unarchive"]);
    // Who may not comes first, whatever the state.
    expect(items({ status: "running" }, { notYours: NOT_YOURS })).toEqual([`Close… — ${NOT_YOURS}`, `Archive… — ${NOT_YOURS}`]);
    expect(items({ status: "aborted", resolution: "wont_do", archived: true }, { notYours: NOT_YOURS }))
      .toEqual([`Reopen — ${NOT_YOURS}`, `Unarchive — ${NOT_YOURS}`]);
  });
});

const CP63 = { key: "CP-63", title: "Export usage as CSV (again)", status: "received" as const };
const CP62 = { key: "CP-62", title: "CSV export drops rows", status: "received" as const };
const CP47 = { key: "CP-47", title: "Export usage as CSV", status: "received" as const };
const CP64 = { key: "CP-64", title: "Duplicate of CP-62", status: "aborted" as const, duplicateOf: "CP-62" };

describe("CloseDialog", () => {
  test("Won't do with something waiting: it keeps waiting, unless the relation is removed in the same step", async () => {
    const got: CloseDecision[] = [];
    await mount(<CloseDialog open onOpenChange={() => undefined} task={CP63} waiting={[CP47]} find={async () => []} onConfirm={(d) => { got.push(d); }} />);
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect(confirm.disabled).toBe(true);
    // Nothing to merge is Mark done's: offered with why not.
    const radios = [...document.querySelectorAll<HTMLButtonElement>('[role="radio"]')];
    const nothing = radios.find((r) => r.textContent?.includes("Nothing to merge"))!;
    expect(nothing.getAttribute("aria-disabled") ?? String(nothing.disabled)).toBe("true");
    expect(nothing.textContent).toContain("Mark done gives this");
    await act(async () => radios.find((r) => r.textContent?.startsWith("Won't do"))!.click());
    const callout = document.querySelector('[data-testid="waits-on"]')!;
    expect(callout.textContent).toContain("CP-47 waits on this. It will keep waiting: a task closed without merging releases nothing.");
    await act(async () => document.querySelector<HTMLElement>('[data-testid="remove-relations"] [role="checkbox"]')!.click());
    expect(document.querySelector('[data-testid="waits-on"]')!.textContent).toContain("Closing also removes the relation, so it can start without CP-63.");
    expect(confirm.textContent).toBe("Close as Won't do");
    await act(async () => confirm.click());
    expect(got).toEqual([{ resolution: "wont_do", removeRelations: true }]);
  });

  test("Duplicate: the picker refuses this task and a duplicate, and the handover is said", async () => {
    const got: CloseDecision[] = [];
    await mount(<CloseDialog open onOpenChange={() => undefined} task={CP63} waiting={[CP47]} initial="duplicate"
      find={async () => [CP62, CP63, CP64]} onConfirm={(d) => { got.push(d); }} />);
    await act(async () => new Promise((r) => setTimeout(r, 300)));
    const options = [...document.querySelectorAll('[role="option"]')];
    const disabled = options.filter((o) => o.getAttribute("aria-disabled") === "true").map((o) => o.textContent ?? "");
    expect(disabled.some((t) => t.includes("CP-63"))).toBe(true);
    expect(disabled.some((t) => t.includes("CP-64") && t.includes("pick that one"))).toBe(true);
    await act(async () => (options.find((o) => o.textContent?.includes("CP-62")) as HTMLElement).dispatchEvent(new MouseEvent("mousedown", { bubbles: true })));
    expect(document.querySelector('[data-testid="duplicate-handover"]')!.textContent)
      .toBe("CP-47 waits on CP-63. Once closed, it waits on CP-62 instead, and starts when CP-62 is merged. CP-62 gets “Duplicated by CP-63” in its Relations.");
    expect(document.querySelector('[data-testid="waits-on"]')).toBeNull();
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect(confirm.textContent).toBe("Close as duplicate of CP-62");
    await act(async () => confirm.click());
    expect(got).toEqual([{ resolution: "duplicate", duplicateOf: "CP-62" }]);
  });

  test("Close and archive starts with nothing chosen; a member sees the primary disabled with whose decision it is", async () => {
    await mount(<CloseDialog open onOpenChange={() => undefined} task={CP63} andArchive find={async () => []} onConfirm={() => undefined}
      cantReproduceReason="Only for a bug: there is nothing to reproduce in a feature" refused={NOT_YOURS} initial="wont_do" />);
    expect(document.querySelector('[role="dialog"] h2, [role="dialog"] [id]')!.textContent).toContain("Close and archive CP-63");
    expect(document.querySelector('[role="radio"][aria-checked="true"]')).toBeNull();
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="close-confirm"]')!;
    expect([confirm.textContent, confirm.disabled]).toEqual(["Close and archive", true]);
    expect(document.getElementById(confirm.getAttribute("aria-describedby")!)!.textContent).toBe(NOT_YOURS);
    const cant = [...document.querySelectorAll('[role="radio"]')].find((r) => r.textContent?.startsWith("Can't reproduce"))!;
    expect(cant.textContent).toContain("Only for a bug");
  });
});

describe("Archive confirmations", () => {
  test("an ended task: Keep it holds the focus, Archive confirms", async () => {
    let done = 0;
    await mount(<ArchiveDialog open onOpenChange={() => undefined} task={{ key: "CP-46", title: "Usage CSV", status: "aborted" }} onConfirm={() => { done++; }} />);
    expect(document.activeElement?.textContent).toBe("Keep it");
    await act(async () => document.querySelector<HTMLButtonElement>('[data-testid="archive-confirm"]')!.click());
    expect(done).toBe(1);
  });

  test("an epic with unended tasks is refused, listing them, with Archive its N ended tasks", async () => {
    let done = 0;
    await mount(<ArchiveEpicDialog open onOpenChange={() => undefined} epic="Webhook reliability" onConfirm={() => { done++; }}
      unfinished={[{ ...CP47, owner: "Ana" }, CP62]} ended={[{ key: "CP-30", title: "Retry webhooks", status: "done" }, { key: "CP-31", title: "Sign them", status: "done" }]} />);
    expect(document.body.textContent).toContain("Webhook reliability can't be archived yet");
    expect([...document.querySelectorAll('[data-testid="unfinished"] li .ds-mono')].map((l) => l.textContent)).toEqual(["CP-47", "CP-62"]);
    expect(document.querySelector('[data-testid="unfinished"] li')!.textContent).toContain("Ana");
    const ended = document.querySelector<HTMLButtonElement>('[data-testid="archive-ended"]')!;
    expect(ended.textContent).toBe("Archive its 2 ended tasks");
    await act(async () => ended.click());
    expect(done).toBe(1);
  });
});

describe("RelationList's duplicate groups", () => {
  test("Duplicate of and Duplicated by take the copy glyph, are never removable, and carry the handover line", async () => {
    const removed: string[] = [];
    const el = await mount(<RelationList onRemove={(g, k) => removed.push(`${g}:${k}`)} groups={[
      { kind: "blocked-by", rows: [{ key: "CP-41", title: "Meter", status: "review" }] },
      { kind: "duplicated-by", rows: [{ key: "CP-63", title: "Export again", status: "aborted" }], note: "CP-63's blocking came here with it: CP-47 now waits on CP-62." },
      { kind: "duplicate-of", rows: [{ key: "CP-62", title: "CSV", status: "received" }] },
    ]} />);
    const group = (kind: string) => el.querySelector(`[data-group="${kind}"]`)!;
    expect(group("duplicated-by").querySelector('svg[data-icon="copy"]')).not.toBeNull();
    expect(group("duplicate-of").querySelector('svg[data-icon="copy"]')).not.toBeNull();
    expect(group("duplicated-by").getAttribute("aria-label")).toBe("Duplicated by");
    expect(group("duplicated-by").querySelector('[data-testid="relation-note"]')!.textContent).toBe("CP-63's blocking came here with it: CP-47 now waits on CP-62.");
    expect(group("duplicated-by").querySelector('[aria-label^="Remove"]')).toBeNull();
    expect(group("duplicate-of").querySelector('[aria-label^="Remove"]')).toBeNull();
    expect(group("blocked-by").querySelector('[aria-label="Remove blocked by CP-41"]')).not.toBeNull();
  });
});

describe("a closed card on the board", () => {
  test("says its resolution where a stopped one says stopped, and the archived hint sits in the header", async () => {
    const project: NavProject = { id: "p", name: "control-plane", tasks: [
      { id: "a", key: "CP-46", title: "Closed", status: "aborted", resolution: "wont_do" },
      { id: "b", key: "CP-63", title: "Duplicate", status: "aborted", resolution: "duplicate", duplicateOf: "CP-62" },
      { id: "c", key: "CP-38", title: "Stopped", status: "aborted" },
      { id: "d", key: "CP-37", title: "Spike", status: "done", resolution: "nothing_to_merge" },
    ] };
    const el = await mount(<Board project={project} archivedHint={<a href="#archived">3 archived</a>} />);
    const card = (key: string) => el.querySelector(`[aria-label^="${key}"]`) ?? [...el.querySelectorAll("[data-board-key]")].find((c) => c.textContent?.includes(key))!;
    expect(card("CP-46").querySelector('[data-testid="resolution-mark"]')!.textContent).toBe("Won't do");
    expect(card("CP-46").textContent).toContain("closed");
    expect(card("CP-63").querySelector('[data-testid="resolution-mark"]')!.textContent).toBe("Duplicate of CP-62");
    expect(card("CP-38").querySelector('[data-testid="resolution-mark"]')).toBeNull();
    expect(card("CP-38").textContent).toContain("stopped");
    expect(card("CP-37").querySelector('[data-testid="resolution-mark"]')).toBeNull();
    expect(el.querySelector('[data-testid="board-archived"]')!.textContent).toBe("3 archived");
  });
});
