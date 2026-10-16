/**
 * Commenting on a diff and the feedback pieces: the gutter "+", picking a
 * range by press and drag, the editor's keys, the drafts tray, a card's
 * actions by status and how an item off its lines is shown. Mounted in
 * happy-dom and driven by the events a browser sends.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { FeedbackCodeAnchor } from "@dude/domain";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { LiveDiff, type DiffRange, type LiveDiffFile } from "../src/components/LiveDiff.tsx";
import { CommentEditor, DraftsTray, FeedbackCard, type FeedbackCardItem } from "../src/components/FeedbackCard.tsx";
import { accessibleName, allByRole, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  document.body.innerHTML = "";
  root = null;
  host = null;
});

async function mount(node: ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(<TooltipProvider>{node}</TooltipProvider>));
  return host;
}

const mouse = (type: string, init: MouseEventInit = {}) => new MouseEvent(type, { bubbles: true, cancelable: true, button: 0, ...init });
const fire = async (el: EventTarget, ev: Event) => act(async () => void el.dispatchEvent(ev));

// One file, two hunks: 1–4 (a removal and two additions) and 40–41.
const FILE: LiveDiffFile = {
  path: "src/app.ts", status: "M", additions: 3, deletions: 1,
  hunks: [
    { header: "@@ -1,3 +1,4 @@", lines: [
      { kind: " ", old: 1, new: 1, text: "const a = 1;" },
      { kind: "-", old: 2, new: null, text: "const b = 2;" },
      { kind: "+", old: null, new: 2, text: "const b = 3;" },
      { kind: "+", old: null, new: 3, text: "const c = 4;" },
      { kind: " ", old: 3, new: 4, text: "export { a };" },
    ] },
    { header: "@@ -39,2 +40,2 @@", lines: [
      { kind: " ", old: 39, new: 40, text: "// tail" },
      { kind: "+", old: null, new: 41, text: "end();" },
    ] },
  ],
};

/** A LiveDiff whose selection the test keeps, recording each change. */
function Commentable({ max = 200, canStart = true, view, log, after }: {
  max?: number; canStart?: boolean; view?: "unified" | "split"; log: Array<[DiffRange | null, boolean]>;
  after?: (path: string, side: "old" | "new", line: number) => ReactNode;
}) {
  const [selection, setSelection] = useState<DiffRange | null>(null);
  return (
    <LiveDiff base="0fff44b" files={[FILE]} defaultView={view} commenting={{
      canStart, maxLines: max, selection,
      onSelectionChange: (r, done) => { log.push([r, done]); setSelection(r); },
      renderAfterLine: after,
    }} />
  );
}

const plusFor = (el: HTMLElement, name: string) => byRole<HTMLButtonElement>(el, "button", name);
const rowOf = (el: HTMLElement, side: "old" | "new", line: number) =>
  el.querySelector<HTMLElement>(`[data-cside="${side}"][data-cline="${line}"]`)!;

describe("LiveDiff commenting", () => {
  test("each commentable line has a real, named \"+\" button; hovering a line marks it so its \"+\" shows", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} />);
    const names = allByRole(el, "button").map(accessibleName).filter((n) => n.startsWith("Comment on"));
    expect(names).toEqual(["Comment on line 1", "Comment on line 2 (removed)", "Comment on line 2", "Comment on line 3", "Comment on line 4",
      "Comment on line 40", "Comment on line 41"]);
    const row = rowOf(el, "new", 3);
    expect(row.hasAttribute("data-hover")).toBe(false);
    await fire(row.querySelector(".\\_text, span:last-of-type") ?? row, mouse("mouseover"));
    expect(row.hasAttribute("data-hover")).toBe(true);
    await fire(rowOf(el, "new", 4), mouse("mouseover"));
    expect(row.hasAttribute("data-hover")).toBe(false);
  });

  test("the \"+\" is keyboard reachable: focused and pressed, it picks its line, and Shift stretches the range", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} />);
    const plus = plusFor(el, "Comment on line 2");
    await act(async () => plus.focus());
    expect(document.activeElement).toBe(plus);
    // A keyboard press is a click with no pointer detail.
    await fire(plus, mouse("click", { detail: 0 }));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 2 }, true]);
    await fire(plusFor(el, "Comment on line 4"), mouse("click", { detail: 0, shiftKey: true }));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 4 }, true]);
    // The picked lines say so.
    expect(rowOf(el, "new", 3).hasAttribute("data-picked")).toBe(true);
    expect(el.querySelectorAll("[data-picked]").length).toBe(3);
  });

  test("a press then a drag picks a range on one side, within one hunk, done once let go", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} />);
    await fire(plusFor(el, "Comment on line 2"), mouse("mousedown"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 2 }, false]);
    await fire(rowOf(el, "new", 4), mouse("mouseover"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 4 }, false]);
    expect(el.querySelectorAll("[data-testid=drag-hint]").length).toBe(1);
    expect(el.querySelector("[data-testid=drag-hint]")!.textContent).toContain("3 lines · let go to comment");
    // The removed side, and another hunk, are not this range's.
    await fire(rowOf(el, "old", 2), mouse("mouseover"));
    await fire(rowOf(el, "new", 41), mouse("mouseover"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 4 }, false]);
    await fire(window, mouse("mouseup"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 2, endLine: 4 }, true]);
    expect(el.querySelectorAll("[data-testid=drag-hint]").length).toBe(0);
  });

  test("on the removed side a drag runs over context lines by their old numbers", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} />);
    await fire(plusFor(el, "Comment on line 2 (removed)"), mouse("mousedown"));
    await fire(rowOf(el, "new", 4), mouse("mouseover")); // context: old 3
    await fire(window, mouse("mouseup"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "old", startLine: 2, endLine: 3 }, true]);
  });

  test("a range is capped at maxLines, counted from where the press started", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} max={2} />);
    await fire(plusFor(el, "Comment on line 1"), mouse("mousedown"));
    await fire(rowOf(el, "new", 4), mouse("mouseover"));
    await fire(window, mouse("mouseup"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 1, endLine: 2 }, true]);
    // Upwards, it keeps the end pressed.
    await fire(plusFor(el, "Comment on line 4"), mouse("mousedown"));
    await fire(rowOf(el, "new", 1), mouse("mouseover"));
    await fire(window, mouse("mouseup"));
    expect(log.at(-1)).toEqual([{ path: "src/app.ts", side: "new", startLine: 3, endLine: 4 }, true]);
  });

  test("without canStart there is no \"+\", and a press picks nothing", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const el = await mount(<Commentable log={log} canStart={false} />);
    expect(allByRole(el, "button").filter((b) => accessibleName(b).startsWith("Comment on")).length).toBe(0);
    await fire(rowOf(el, "new", 2).querySelector("span")!, mouse("mousedown"));
    expect(log).toEqual([]);
  });

  test("what goes under a line is drawn under that side's line, in unified and in split", async () => {
    const log: Array<[DiffRange | null, boolean]> = [];
    const after = (path: string, side: "old" | "new", line: number) =>
      (side === "new" && line === 3) || (side === "old" && line === 2) ? <span data-x={`${side}${line}`}>{path}</span> : null;
    const el = await mount(<Commentable log={log} after={after} />);
    const rows = [...el.querySelectorAll("[data-testid=after-line]")].map((r) => `${r.getAttribute("data-side")}${r.getAttribute("data-line")}`);
    expect(rows).toEqual(["old2", "new3"]);
    // In unified the slot follows its line.
    expect(el.querySelector("[data-x=new3]")!.closest("[data-testid=after-line]")!.previousElementSibling).toBe(rowOf(el, "new", 3));
    await act(async () => root!.unmount());
    root = createRoot(host!);
    await act(async () => root!.render(<TooltipProvider><Commentable log={log} after={after} view="split" /></TooltipProvider>));
    const split = host!.querySelectorAll("[data-testid=after-line]");
    expect(split.length).toBe(2);
    // Split: each under its own column of the row it follows.
    const cols = [...split].map((s) => [...s.parentElement!.parentElement!.children].indexOf(s.parentElement!));
    expect(cols.sort()).toEqual([0, 1]);
  });
});

describe("CommentEditor", () => {
  function Editor({ log, full }: { log: string[]; full?: string }) {
    const [value, setValue] = useState("");
    return <CommentEditor where="Comment on line 3" value={value} onChange={setValue} full={full}
      onSave={() => log.push(`save:${value}`)} onCancel={() => log.push("cancel")} />;
  }
  const type = async (el: HTMLTextAreaElement, text: string) => act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!;
    set.call(el, text);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
  const key = (k: string, init: KeyboardEventInit = {}) => new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true, ...init });

  test("⌘/Ctrl+Enter saves what is written, never an empty draft; Escape cancels", async () => {
    const log: string[] = [];
    const el = await mount(<Editor log={log} />);
    const field = byRole<HTMLTextAreaElement>(el, "textbox", "Comment");
    expect(document.activeElement).toBe(field);
    await fire(field, key("Enter", { ctrlKey: true }));
    expect(log).toEqual([]);
    await type(field, "Name the person");
    await fire(field, key("Enter"));
    expect(log).toEqual([]);
    await fire(field, key("Enter", { ctrlKey: true }));
    await fire(field, key("Enter", { metaKey: true }));
    await fire(field, key("Escape"));
    expect(log).toEqual(["save:Name the person", "save:Name the person", "cancel"]);
    expect(el.textContent).toContain("Private draft until you submit");
  });

  test("full: the field is off and says why, and nothing saves", async () => {
    const log: string[] = [];
    const el = await mount(<Editor log={log} full="You have 50 drafts on this task, the most kept." />);
    const field = byRole<HTMLTextAreaElement>(el, "textbox", "Comment");
    expect(field.disabled).toBe(true);
    expect(el.querySelector("[role=note]")!.textContent).toBe("You have 50 drafts on this task, the most kept.");
    expect(byRole<HTMLButtonElement>(el, "button", "Save draft").disabled).toBe(true);
  });
});

describe("DraftsTray", () => {
  const drafts = [
    { id: "d1", anchor: { kind: "general" as const }, body: "Add a **test** for the cap" },
    { id: "d2", anchor: code(11, 12), body: "Name the person" },
  ];

  test("counts the drafts, says they are private and where they go, and lists them with their actions", async () => {
    const log: string[] = [];
    function Tray() {
      const [open, setOpen] = useState(false);
      return <DraftsTray drafts={drafts} max={50} destination="the fixer at work" open={open} onOpenChange={setOpen}
        onSubmit={() => log.push("submit")} onJump={(id) => log.push(`jump:${id}`)} onEdit={(id) => log.push(`edit:${id}`)} onDelete={(id) => log.push(`delete:${id}`)} />;
    }
    const el = await mount(<Tray />);
    const tray = el.querySelector<HTMLElement>("[data-testid=fb-tray]")!;
    expect(tray.getAttribute("aria-label")).toBe("Your drafts");
    expect(tray.textContent).toContain("2 drafts · only you see them until you submit");
    expect(tray.textContent).toContain("Goes to the fixer at work");
    expect(el.querySelectorAll("[data-testid=fb-tray-row]").length).toBe(0);
    const toggle = byRole<HTMLButtonElement>(el, "button", "Review");
    expect(toggle.getAttribute("aria-expanded")).toBe("false");
    await fire(toggle, mouse("click"));
    const rows = [...el.querySelectorAll<HTMLElement>("[data-testid=fb-tray-row]")];
    expect(rows.map((r) => r.textContent)).toEqual(["General· the task as a wholeAdd a test for the cap", "app.tslines 11–12Name the person"]);
    await fire(byRole(rows[1]!, "button", "Show it in the diff"), mouse("click"));
    await fire(byRole(rows[0]!, "button", "Show it on the Feedback tab"), mouse("click"));
    await fire(byRole(rows[1]!, "button", "Edit"), mouse("click"));
    await fire(byRole(rows[0]!, "button", "Delete"), mouse("click"));
    await fire(byRole(el, "button", "Submit 2…"), mouse("click"));
    expect(log).toEqual(["jump:d2", "jump:d1", "edit:d2", "delete:d1", "submit"]);
  });

  test("none: no tray. At the limit it says so; refused, Submit is off with the reason", async () => {
    let el = await mount(<DraftsTray drafts={[]} max={50} open onOpenChange={() => {}} onSubmit={() => {}} />);
    expect(el.querySelectorAll("[data-testid=fb-tray]").length).toBe(0);
    await act(async () => root!.render(<TooltipProvider><DraftsTray drafts={drafts} max={2} open onOpenChange={() => {}} onSubmit={() => {}} /></TooltipProvider>));
    el = host!;
    expect(el.querySelector("[data-testid=fb-tray]")!.textContent).toContain("2 of 2 drafts · the most you can keep");
    expect(el.querySelector("[role=note]")!.textContent).toContain("2 drafts is the most kept on a task");
    await act(async () => root!.render(<TooltipProvider><DraftsTray drafts={drafts} max={50} open={false} onOpenChange={() => {}} onSubmit={() => {}}
      submitRefused="This task is done: feedback on it goes in a follow-up task." /></TooltipProvider>));
    expect(byRole<HTMLButtonElement>(host!, "button", "Submit 2").disabled).toBe(true);
    expect(allByRole(host!, "button", "Submit 2…").length).toBe(0);
  });
});

function code(start: number, end: number): FeedbackCodeAnchor {
  return { kind: "code", runId: "run_1", repo: "web", path: "src/app.ts", side: "new", startLine: start, endLine: end, baseSha: "b", headSha: null,
    diffChecksum: "c", excerpt: [{ kind: "+", old: null, new: start, text: "const b = 3;" }, { kind: "+", old: null, new: end, text: "const c = 4;" }] };
}

const ana = { id: "p_ana", name: "Ana Ribeiro" };
const item = (over: Partial<FeedbackCardItem>): FeedbackCardItem => ({
  id: "f1", status: "open", resolutionPending: false, resolvedCommit: null, body: "Name the person", anchor: code(11, 12),
  author: ana, at: new Date(Date.now() - 60_000).toISOString(), ...over,
});
const resolved = Promise.resolve(true);

describe("FeedbackCard", () => {
  const actionsOf = (el: HTMLElement) => allByRole(el.querySelector("footer")!, "button").map(accessibleName);
  const all = { onReply: () => resolved, onResolve: () => resolved, onReopen: () => resolved, onSaveDraft: () => resolved, onDelete: () => resolved, onJump: () => {} };

  test("a draft says only you see it, with Edit and Delete", async () => {
    const el = await mount(<FeedbackCard item={item({ status: "draft" })} {...all} />);
    expect(el.textContent).toContain("Only you see this until you submit.");
    expect(el.querySelector("[data-testid=fb-status]")!.textContent).toBe("Draft · only you");
    expect(actionsOf(el)).toEqual(["Edit", "Delete"]);
  });

  test("open or delivered: Reply and Resolve; answered or resolved: Reopen; resolved pending: Reply off and Confirm resolved", async () => {
    let el = await mount(<FeedbackCard item={item({ status: "open" })} {...all} />);
    expect(actionsOf(el)).toEqual(["Show in diff", "Reply", "Resolve"]);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({ status: "delivered", withWhom: "Fix" })} {...all} /></TooltipProvider>));
    el = host!;
    expect(el.querySelector("[data-testid=fb-status]")!.textContent).toBe("With Fix");
    expect(actionsOf(el)).toEqual(["Show in diff", "Reply", "Resolve"]);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({ status: "replied" })} {...all} /></TooltipProvider>));
    expect(actionsOf(host!)).toEqual(["Show in diff", "Reply", "Reopen"]);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({ status: "resolved" })} {...all} /></TooltipProvider>));
    expect(actionsOf(host!)).toEqual(["Show in diff", "Reply", "Reopen"]);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({ status: "resolved", resolutionPending: true, resolvedCommit: "9c1e4a7f00" })} {...all} /></TooltipProvider>));
    el = host!;
    expect(actionsOf(el)).toEqual(["Show in diff", "Reply", "Confirm resolved"]);
    expect(byRole<HTMLButtonElement>(el, "button", "Reply").disabled).toBe(true);
    expect(el.querySelector("[data-testid=fb-pending]")!.textContent).toContain("9c1e4a7");
  });

  test("read only: no actions at all", async () => {
    const el = await mount(<FeedbackCard item={item({ status: "open" })} readOnly {...all} />);
    expect(el.querySelectorAll("footer").length).toBe(0);
  });

  test("a reply is sent with ⌘/Ctrl+Enter and the box closes once it is taken", async () => {
    const sent: string[] = [];
    const el = await mount(<FeedbackCard item={item({ status: "open" })} onReply={async (b) => void sent.push(b)} />);
    await fire(byRole(el, "button", "Reply"), mouse("click"));
    const box = byRole<HTMLTextAreaElement>(el, "textbox", "Reply");
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(box, "Kept: see criterion 1");
      box.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await fire(box, new KeyboardEvent("keydown", { key: "Enter", ctrlKey: true, bubbles: true }));
    expect(sent).toEqual(["Kept: see criterion 1"]);
    expect(allByRole(el, "textbox", "Reply").length).toBe(0);
  });

  test("a refused action keeps the card and says why", async () => {
    const el = await mount(<FeedbackCard item={item({ status: "open" })} onResolve={async () => { throw new Error("this task is done"); }} />);
    await fire(byRole(el, "button", "Resolve"), mouse("click"));
    expect(el.querySelector("[role=alert]")!.textContent).toBe("this task is done");
  });

  test("inline on its lines, the excerpt is not repeated; outdated or unchanged, it is, with the words that say why", async () => {
    let el = await mount(<FeedbackCard item={item({})} inline />);
    expect(el.querySelectorAll("[data-testid=fb-excerpt]").length).toBe(0);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({})} inline placement="outdated" /></TooltipProvider>));
    el = host!;
    expect(el.querySelector("[data-testid=fb-excerpt]")!.textContent).toContain("The lines it was on have changed");
    expect(el.querySelector("[data-testid=fb-excerpt]")!.textContent).toContain("const c = 4;");
    expect(el.querySelector("[data-testid=fb-item]")!.getAttribute("data-placement")).toBe("outdated");
    expect(el.textContent).toContain("Lines changed since");
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({})} inline placement="unchanged" /></TooltipProvider>));
    expect(host!.querySelector("[data-testid=fb-excerpt]")!.textContent).toContain("These lines are unchanged in this diff");
    expect(host!.textContent).not.toContain("Lines changed since");
  });

  test("a preview item names its page, element and component; a picture only when given", async () => {
    const preview = { kind: "preview" as const, url: "https://wi.preview.dev/runs/1", viewport: { width: 1440, height: 900 }, selector: "button[data-testid=pause]",
      text: "Pause", component: { name: "SessionHeader", file: "src/Header.tsx", line: 31 } };
    const el = await mount(<FeedbackCard item={item({ anchor: preview })} />);
    const p = el.querySelector("[data-testid=fb-preview]")!;
    expect(p.textContent).toContain("wi.preview.dev/runs/1 · 1440×900");
    expect(p.textContent).toContain("button[data-testid=pause]");
    expect(p.textContent).toContain("SessionHeader src/Header.tsx:31");
    expect(p.querySelectorAll("img").length).toBe(0);
    await act(async () => root!.render(<TooltipProvider><FeedbackCard item={item({ anchor: preview })} imageUrl="blob:x" /></TooltipProvider>));
    expect(host!.querySelectorAll("[data-testid=fb-preview] img").length).toBe(1);
  });
});
