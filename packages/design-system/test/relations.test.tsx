/**
 * Task relations' components, mounted in happy-dom (docs/design/task-relations.md):
 * what each says, which ways to start a blocked task offers, who may remove
 * a relation, the board's card start beside (never inside) its card, the
 * filter, and the hover that lights a card's relations.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import { Board } from "../src/components/Board.tsx";
import { ProposalCard, type ProposalCardItem } from "../src/components/Brainstorm.tsx";
import { AutoStartMark, DependencyStrip, RelationChip, RelationList, StartGate, StartMenu, relationWords } from "../src/components/Relations.tsx";
import { AutoStartDialog, RelationPicker } from "../src/components/RelationDialogs.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import type { NavProject, NavTask } from "../src/util/navModel.ts";

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

const meter = { key: "CP-41", title: "Hourly meter", status: "review" as const };
const sync = { key: "CP-43", title: "Stripe sync", status: "running" as const };
const spike = { key: "CP-37", title: "Pricing spike", status: "done" as const, unmerged: true };

describe("RelationChip", () => {
  test("names one blocker with its status in words, counts several, and says done with nothing merged", () => {
    expect(relationWords("blocked-by", [meter, sync])).toBe("Blocked by CP-41 (in review) and CP-43 (in progress)");
    expect(relationWords("blocked-by", [spike])).toBe("Blocked by CP-37 (done, nothing merged)");
    expect(relationWords("blocks", [meter, sync])).toBe("Blocks CP-41 and CP-43");
    const one = renderToStaticMarkup(<TooltipProvider><RelationChip kind="blocked-by" tasks={[spike]} /></TooltipProvider>);
    expect(one).toContain("not merged");
    expect(one).toContain('aria-label="Blocked by CP-37 (done, nothing merged)"');
    expect(one).not.toContain('data-status="done"');
    const many = renderToStaticMarkup(<TooltipProvider><RelationChip kind="blocks" tasks={[meter, sync, spike]} /></TooltipProvider>);
    expect(many).toContain(">3<");
    expect(renderToStaticMarkup(<RelationChip kind="blocks" tasks={[]} />)).toBe("");
  });
});

describe("StartGate", () => {
  test("keeps the ways disabled with the reason, then the owner's Start when unblocked and Start anyway", async () => {
    const el = await mount(<StartGate blockers={[meter, sync]} starts={[{ id: "talk", label: "Talk it through", icon: "message" }, { id: "deliver", label: "Deliver", icon: "zap" }]}
      onStartAnyway={() => undefined} onStartWhenUnblocked={() => undefined} />);
    const disabled = [...el.querySelectorAll("button[disabled]")].map((b) => b.textContent);
    expect(disabled).toEqual(["Talk it through", "Deliver"]);
    // The focusable wrapper is the control a screen reader meets: a disabled
    // button named for the way, described once by the reason.
    const deliver = el.querySelector<HTMLElement>('[data-testid="gate-deliver"]')!;
    expect([deliver.getAttribute("role"), deliver.getAttribute("aria-disabled"), deliver.getAttribute("aria-label")]).toEqual(["button", "true", "Deliver"]);
    expect(document.getElementById(deliver.getAttribute("aria-describedby")!)?.textContent)
      .toBe("Blocked by CP-41 (in review) and CP-43 (in progress): it can start once they're merged.");
    expect(el.querySelectorAll("[aria-describedby]").length).toBe(2);
    expect(el.textContent).toContain("Blocked by CP-41 (in review) and CP-43 (in progress): it can start once they're merged.");
    expect(el.querySelector('[data-testid="start-when-unblocked"]')).not.toBeNull();
    expect(el.querySelector('[data-testid="start-anyway"]')).not.toBeNull();
  });
  test("a reader who may not start sees the ways, disabled, and nothing else; a blocker that never merges refuses the owner's item", async () => {
    const el = await mount(<StartGate blockers={[spike]} starts={[{ id: "deliver", label: "Deliver", icon: "zap" }]}
      onStartWhenUnblocked={() => undefined} startWhenUnblockedRefused="CP-37 was marked done with nothing merged and will never merge, so this would never start it." />);
    expect(el.querySelector('[data-testid="start-anyway"]')).toBeNull();
    expect([...el.querySelectorAll("button[disabled]")].map((b) => b.textContent)).toEqual(["Deliver", "Start when unblocked…"]);
    expect(el.textContent).toContain("will never merge");
    // The refused item's focusable wrapper, like the ways': a disabled button named for it, described once.
    const refused = el.querySelector<HTMLElement>('[aria-label="Start when unblocked…"][role="button"]')!;
    expect(refused.getAttribute("aria-disabled")).toBe("true");
    const describedBy = refused.getAttribute("aria-describedby")!;
    expect(document.getElementById(describedBy)?.textContent)
      .toBe("CP-37 was marked done with nothing merged and will never merge, so this would never start it.");
    expect(el.querySelectorAll(`[aria-describedby="${describedBy}"]`).length).toBe(1);
  });
});

describe("StartMenu", () => {
  const ways = (el: HTMLElement) => [...document.querySelectorAll('[role="menuitem"]')].map((i) => `${i.textContent?.replace(/\s+/g, " ").trim()}${i.getAttribute("aria-disabled") === "true" || i.hasAttribute("data-disabled") ? " (disabled)" : ""}`);
  test("unblocked: both ways, neither first by default, both enabled", async () => {
    const el = await mount(<StartMenu taskKey="CP-47" blockers={[]} onStart={() => undefined} ownerName="Ana" open />);
    expect(el.querySelector('[data-testid="start-menu-CP-47"]')?.getAttribute("aria-label")).toBe("Start CP-47");
    const items = ways(el);
    expect(items.some((i) => i.startsWith("Talk it through"))).toBe(true);
    expect(items.some((i) => i.startsWith("Deliver"))).toBe(true);
    expect(items.every((i) => !i.endsWith("(disabled)"))).toBe(true);
  });
  test("blocked: the ways are disabled with the reason; the owner's Start when unblocked and Start anyway stay live", async () => {
    const el = await mount(<StartMenu taskKey="CP-44" blockers={[meter]} onStart={() => undefined} onStartAnyway={() => undefined}
      onStartWhenUnblocked={() => undefined} ownerName="Ana" open />);
    expect(el.querySelector('[data-testid="start-menu-CP-44"]')?.getAttribute("aria-label"))
      .toBe("Start CP-44: Blocked by CP-41 (in review): it can start once that's merged.");
    const items = ways(el);
    expect(items.filter((i) => i.endsWith("(disabled)")).length).toBe(2);
    expect(items).toContain("Start when unblocked…");
    expect(items).toContain("Start anyway…");
  });
  test("someone else's task: Start when unblocked is disabled saying whose", async () => {
    await mount(<StartMenu taskKey="CP-44" blockers={[meter]} onStart={() => undefined} onStartAnyway={() => undefined} ownerName="Ana" open />);
    expect(document.body.textContent).toContain("Only Ana, its owner, can set this.");
  });
});

describe("AutoStartMark", () => {
  test("says when it starts, how, and who set it; Cancel is the owner's", async () => {
    const el = await mount(<AutoStartMark blockers={[meter, sync]} auto={{ how: "deliver", by: "Ana", ago: "2d", onCancel: () => undefined }} />);
    expect(el.textContent).toContain("Starts when CP-41 and CP-43 merge · Deliver");
    expect(el.textContent).toContain("Ana set it 2d ago");
    expect(el.querySelector('[data-testid="cancel-auto-start"]')).not.toBeNull();
    const ro = renderToStaticMarkup(<TooltipProvider><AutoStartMark blockers={[meter]} auto={{ how: "talk", by: "Ana", ago: "2d" }} /></TooltipProvider>);
    expect(ro).not.toContain("Cancel");
  });
});

describe("AutoStartDialog", () => {
  test("nothing is preselected: Set stays disabled, saying why, until a way is picked", async () => {
    let set = null as string | null;
    await mount(<AutoStartDialog open onOpenChange={() => undefined} taskKey="CP-44" blockers={[meter]} onConfirm={(h) => { set = h; }} />);
    const confirm = document.querySelector<HTMLButtonElement>('[data-testid="confirm-auto-start"]')!;
    expect(confirm.disabled).toBe(true);
    expect(confirm.textContent).toBe("Set");
    expect(document.getElementById(confirm.getAttribute("aria-describedby")!)?.textContent).toBe("Pick how it starts.");
    const deliver = [...document.querySelectorAll<HTMLElement>('[role="radio"]')].find((r) => r.textContent?.includes("Deliver"))!;
    await act(async () => deliver.click());
    expect(confirm.disabled).toBe(false);
    expect(confirm.textContent).toBe("Deliver when it merges");
    await act(async () => confirm.click());
    expect(set).toBe("deliver");
  });
});

describe("RelationList", () => {
  const owner = { id: "per_ana", name: "Ana Nunes" };
  test("groups with counts; a row the reader may not remove has no remove, and says whose it is", async () => {
    const removed: string[] = [];
    const el = await mount(<RelationList onRemove={(g, k) => removed.push(`${g}:${k}`)} groups={[
      { kind: "blocked-by", rows: [{ ...meter, owner }, { ...sync, owner, settled: true, status: "done" }] },
      { kind: "blocks", rows: [{ key: "CP-45", title: "Invoice email", status: "received", owner, removeHint: "Only Márcio, CP-45's owner, can remove this" }] },
      { kind: "related", rows: [] },
    ]} />);
    expect([...el.querySelectorAll("[data-group]")].map((g) => g.getAttribute("data-group"))).toEqual(["blocked-by", "blocks"]);
    expect(el.textContent).toContain("1 of 2 open");
    // A settled blocker says it merged.
    expect(el.querySelector('[data-key="CP-43"] button')?.getAttribute("aria-label")).toBe("CP-43 Stripe sync, merged");
    expect(el.querySelector('[aria-label="Remove blocked by CP-41"]')).not.toBeNull();
    expect(el.querySelector('[aria-label="Remove blocks CP-45"]')).toBeNull();
    const row = el.querySelector<HTMLElement>('[data-key="CP-45"] button')!;
    expect(document.getElementById(row.getAttribute("aria-describedby")!)?.textContent).toBe("Only Márcio, CP-45's owner, can remove this");
    await act(async () => row.parentElement!.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    await act(async () => el.querySelector<HTMLElement>('[aria-label="Remove blocked by CP-41"]')!.click());
    expect(removed).toEqual(["blocked-by:CP-41"]);
  });
  test("with no relations, one line says what they are for", async () => {
    const el = await mount(<RelationList groups={[]} onAdd={() => undefined} />);
    expect(el.textContent).toContain("No relations.");
    expect(el.querySelector('[data-testid="add-relation"]')).not.toBeNull();
  });
});

describe("RelationPicker", () => {
  test("a loop is named, with Make it Related instead", async () => {
    const added: string[] = [];
    await mount(<RelationPicker here={{ key: "CP-40" }} related={new Set()} find={async () => [meter]} onClose={() => undefined}
      onAdd={async (side, key) => { added.push(`${side}:${key}`); return side === "blocked_by" ? { loop: ["CP-40", "CP-41", "CP-44", "CP-40"] } : undefined; }}
      initialLoop={["CP-40", "CP-41", "CP-44", "CP-40"]} />);
    const alert = document.querySelector('[data-testid="relation-loop"]')!;
    expect(alert.getAttribute("role")).toBe("alert");
    expect(alert.querySelector("[aria-label]")?.getAttribute("aria-label")).toBe("CP-40 blocks CP-41, which blocks CP-44, which blocks CP-40");
    await act(async () => document.querySelector<HTMLElement>('[data-testid="make-related"]')!.click());
    expect(added).toEqual(["related:CP-41"]);
  });
});

describe("ProposalCard with relations", () => {
  const items: ProposalCardItem[] = [
    { kind: "task", n: 1, title: "Record crossings", canFile: true, tickedWith: [2, 3], relations: [{ kind: "blocked-by", task: { key: "CP-41", status: "review" } }] },
    { kind: "task", n: 2, title: "Email owners", canFile: true, relations: [{ kind: "blocked-by", item: 1 }] },
    { kind: "task", n: 3, title: "Chart marker", canFile: true, relations: [{ kind: "blocked-by", item: 1 }, { kind: "related", task: { key: "CP-44", status: "received" } }] },
  ];
  test("numbers items, shows the order, lists each item's relations, and ticks a blocker with what waits on it", async () => {
    const el = await mount(<ProposalCard items={items} selected={new Set([1, 2])} onToggle={() => undefined} onFile={() => undefined} filingAs="Márcio"
      order={[[1], [2, 3]]} />);
    expect(el.textContent).toContain("3 tasks, 4 relations");
    expect(el.querySelector('[data-testid="proposal-order"]')?.getAttribute("aria-label")).toBe("Order: item 1, then items 2 and 3");
    expect(el.querySelector('[data-item="1"] [data-testid="ticked-with"]')?.textContent).toBe("Ticked with 2 and 3: they wait on it. Untick them to leave it out.");
    expect(el.querySelector('[aria-label="Blocked by CP-41, in review"]')).not.toBeNull();
    expect(el.querySelector('[aria-label="Blocked by item 1"]')).not.toBeNull();
    expect(el.textContent).toContain("Filing as Márcio: 3 tasks, 1 first, with their 4 relations. Nothing starts.");
  });
  test("an item waiting on one that can't be filed says which", async () => {
    const refused = items.map((it, i) => (i === 0 ? { ...it, canFile: false, why: "data-pipeline is no longer linked to this session." }
      : { ...it, canFile: false, why: "Waits on item 1, which can't be filed: link data-pipeline again, or ask the brainstorm to propose it without item 1." }));
    const el = await mount(<ProposalCard items={refused} selected={new Set()} onToggle={() => undefined} onFile={() => undefined} filingAs="Márcio" />);
    expect([...el.querySelectorAll('[data-testid="cannot-file"]')].map((n) => n.textContent?.slice(0, 22))).toEqual(["data-pipeline is no lo", "Waits on item 1, which", "Waits on item 1, which"]);
    expect(el.textContent).toContain("Nothing here is yours to file now");
  });
});

describe("DependencyStrip", () => {
  test("steps with counts in words; one step is no strip", () => {
    const s = (key: string, status: "received" | "review" | "done", startable: boolean, blockedBy: string[] = []) => ({ key, title: key, status, startable, blockedBy });
    const html = renderToStaticMarkup(<TooltipProvider><DependencyStrip steps={[[s("CP-40", "done", false)], [s("CP-41", "review", false)], [s("CP-44", "received", false, ["CP-41"]), s("CP-47", "received", true)]]}
      counts={{ startable: 1, blocked: 1, working: 1, merged: 1, doneUnmerged: 0, failed: 0 }} /></TooltipProvider>);
    expect(html).toContain("can start");
    expect(html).toContain('aria-label="CP-44 CP-44, not started, blocked by CP-41"');
    expect(html).not.toContain("done, nothing merged");
    expect(renderToStaticMarkup(<DependencyStrip steps={[[s("CP-1", "received", true)]]} counts={{ startable: 1, blocked: 0, working: 0, merged: 0, doneUnmerged: 0, failed: 0 }} />)).toBe("");
  });
});

// ---------------------------------------------------------------------------

const t = (key: string, status: NavTask["status"], more: Partial<NavTask> = {}): NavTask => ({ id: key, key, title: `${key} title`, status, ...more });
const board: NavProject = { id: "p", name: "CP", tasks: [
  t("CP-41", "review", { blocks: [{ key: "CP-44", title: "Billing", status: "received" }], edges: { blockedBy: [], blocks: ["CP-44"], related: [] } }),
  t("CP-44", "received", { blockedBy: [{ key: "CP-41", title: "Meter", status: "review" }], startable: false, canStart: true,
    autoStart: { how: "deliver", by: { id: "per_ana", name: "Ana" }, at: Date.now() - 2 * 86_400_000 },
    edges: { blockedBy: ["CP-41"], blocks: [], related: [] } }),
  t("CP-47", "received", { startable: true, canStart: true }),
  t("CP-37", "done", { merged: false }),
  t("CP-30", "done", { merged: true }),
] };

describe("Board", () => {
  test("a done card says merged only when a pull request merged", () => {
    const html = renderToStaticMarkup(<TooltipProvider><Board project={board} /></TooltipProvider>);
    const card = (k: string) => html.slice(html.indexOf(`data-board-key="task:${k}"`), html.indexOf("</li>", html.indexOf(`data-board-key="task:${k}"`)));
    expect(card("CP-37")).toContain("done, nothing merged");
    expect(card("CP-30")).toContain("merged");
    expect(card("CP-30")).not.toContain("nothing merged");
  });

  test("a blocked card carries its chip and start when unblocked in its name; a blocking card its quieter chip", () => {
    const html = renderToStaticMarkup(<TooltipProvider><Board project={board} /></TooltipProvider>);
    expect(html).toContain('aria-label="CP-44 CP-44 title, blocked by CP-41 (in review); starts when cp-41 merges, Deliver"');
    expect(html).toContain('data-relation="blocks"');
    expect(html).toContain("Starts when CP-41 merges · Deliver");
  });

  test("a Backlog card's start is the card's sibling, tabbable only beside the card with the board's focus", async () => {
    const el = await mount(<Board project={board} cardStart={(card, focus) => (card.task.canStart
      ? <button type="button" tabIndex={focus.tabIndex} data-start={card.task.key}>Start</button> : null)} />);
    const start = el.querySelector<HTMLElement>('[data-start="CP-44"]')!;
    const card = el.querySelector<HTMLElement>('[data-board-key="task:CP-44"]')!;
    expect(card.contains(start)).toBe(false);
    expect(start.closest("li")).toBe(card.closest("li"));
    // Only Backlog cards have one.
    expect(el.querySelector('[data-start="CP-41"]')).toBeNull();
    const tabbable = [...el.querySelectorAll<HTMLElement>("[data-start]")].filter((s) => s.tabIndex === 0).map((s) => s.dataset["start"]);
    const stop = el.querySelector<HTMLElement>('[data-board-key][tabindex="0"]')!;
    expect(tabbable).toEqual(stop.closest("li")!.querySelector("[data-start]") ? [stop.closest("li")!.querySelector<HTMLElement>("[data-start]")!.dataset["start"]] : []);
    await act(async () => card.focus());
    expect([...el.querySelectorAll<HTMLElement>("[data-start]")].filter((s) => s.tabIndex === 0).map((s) => s.dataset["start"])).toEqual(["CP-44"]);
  });

  test("the filter keeps what can start, or what is blocked, with counts in its labels", async () => {
    let filter = "startable" as "all" | "startable" | "blocked";
    const el = await mount(<Board project={board} filter={filter} onFilterChange={(f) => { filter = f; }} />);
    expect(el.querySelector('[data-testid="board-filter"]')?.textContent).toBe("All 5Can start 1Blocked 1");
    expect([...el.querySelectorAll("[data-board-key]")].map((c) => c.getAttribute("data-board-key"))).toEqual(["task:CP-47"]);
    const blocked = [...el.querySelectorAll<HTMLButtonElement>('[data-testid="board-filter"] button')].find((b) => b.textContent?.startsWith("Blocked"))!;
    await act(async () => blocked.click());
    expect(filter).toBe("blocked");
  });

  test("hovering a card lights its relations, dims the rest, and says so in a live region", async () => {
    const lit: Array<string | null> = [];
    const el = await mount(<Board project={board} onHighlightChange={(k) => lit.push(k)} />);
    const item = el.querySelector('[data-board-key="task:CP-41"]')!.closest("li")!;
    await act(async () => item.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    const at = (k: string) => el.querySelector(`[data-board-key="task:${k}"]`)!.getAttribute("data-lit");
    expect(at("CP-41")).toBe("self");
    expect(at("CP-44")).toBe("related");
    expect(at("CP-47")).toBe("dim");
    expect(el.querySelector('[data-testid="board-lit"]')?.textContent).toBe("CP-41's relations: blocks CP-44.");
    await act(async () => el.querySelector("section")!.dispatchEvent(new MouseEvent("mouseout", { bubbles: true, relatedTarget: document.body })));
    expect(lit[0]).toBe("CP-41");
  });

  test("hovering one card re-renders only the cards whose lit state changes", async () => {
    // cardMarks is called once per card render: a render count by card.
    const renders = new Map<string, number>();
    const marks = (card: { task: NavTask }) => {
      renders.set(card.task.key!, (renders.get(card.task.key!) ?? 0) + 1);
      return null;
    };
    const el = await mount(<Board project={board} cardMarks={marks} />);
    const hover = async (k: string) => {
      const item = el.querySelector(`[data-board-key="task:${k}"]`)!.closest("li")!;
      await act(async () => item.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })));
    };
    await hover("CP-41");
    renders.clear();
    // CP-41 goes from self to related, CP-44 from related to self; the rest stay dim.
    await hover("CP-44");
    expect(Object.fromEntries(renders)).toEqual({ "CP-41": 1, "CP-44": 1 });
  });
});
