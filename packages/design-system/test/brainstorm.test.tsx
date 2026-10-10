/**
 * A brainstorm session's parts: the proposal card files only what the
 * person looking may file, says why the rest stays, and shows who filed
 * what; a reader files nothing; the sidebar lists sessions with the shared
 * marker and the owner's face; the rail marks who has the session open.
 * Mounted in happy-dom: what is a control, whether it can be used, what a
 * click does.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ProposalCard, SessionPeople, foldedWords, nothingLeftToFile, proposalSummary, type ProposalCardItem } from "../src/components/Brainstorm.tsx";
import { SidebarSessions } from "../src/components/Sidebar.tsx";
import { byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: React.ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(el));
  return host;
}

const ITEMS: ProposalCardItem[] = [
  { kind: "task", title: "Dedupe runs", project: { key: "BL", name: "billing" }, canFile: true },
  { kind: "edit", title: "Edit BL-58", taskKey: "BL-58", before: "old", after: "new", canFile: false, why: "Only Márcio can file this: it's his task" },
  { kind: "comment", title: "Comment on WC-214", taskKey: "WC-214", canFile: true },
  { kind: "task", title: "Usage panel", project: { key: "WC", name: "web-console" }, canFile: true, filed: { by: "Ana Nunes", key: "WC-240" } },
];

function Card({ onFile, readOnly }: { readonly onFile: (items: number[]) => void; readonly readOnly?: boolean }) {
  const [selected, setSelected] = useState<Set<number>>(() => new Set([0, 1, 2, 3]));
  return (
    <ProposalCard items={ITEMS} selected={selected} filingAs="Ana" readOnly={readOnly}
      onToggle={(i) => setSelected((s) => { const n = new Set(s); if (n.has(i)) n.delete(i); else n.add(i); return n; })}
      onFile={() => onFile([...selected].filter((i) => ITEMS[i]!.canFile && !ITEMS[i]!.filed))} />
  );
}

describe("ProposalCard", () => {
  test("files only what the person may: another's edit stays, saying whose, and a filed item says who filed it", async () => {
    let filed: number[] = [];
    const el = await mount(<Card onFile={(items) => { filed = items; }} />);
    const boxes = [...el.querySelectorAll('[role="checkbox"]')] as HTMLButtonElement[];
    expect(boxes).toHaveLength(3);
    expect(boxes.map((b) => b.disabled)).toEqual([false, true, false]);
    expect(el.querySelector('[data-testid="cannot-file"]')?.textContent).toBe("Only Márcio can file this: it's his task");
    expect(el.textContent).toContain("Ana Nunes filed WC-240");
    expect(el.textContent).toContain("Filing as Ana: 1 task, 1 comment · 1 stays for whoever can.");
    const file = byRole(el, "button", "File 2");
    await act(async () => file.click());
    expect(filed).toEqual([0, 2]);

    // Unticking the comment files one.
    await act(async () => boxes[2]!.click());
    expect(byRole(el, "button", "File 1")).toBeTruthy();
  });

  test("a reader sees the card and files nothing", async () => {
    // Nothing on it is a reader's to file: it folds, and opens read only.
    const el = await mount(<Card onFile={() => { throw new Error("filed"); }} readOnly />);
    expect(el.querySelector('[data-folded="true"]')).toBeTruthy();
    await act(async () => el.querySelector<HTMLButtonElement>('[data-testid="proposal-fold"]')!.click());
    expect(el.querySelectorAll('[role="checkbox"]').length).toBe(3);
    expect([...el.querySelectorAll('[role="checkbox"]')].every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    expect(el.querySelector('[data-testid="file-proposal"]')).toBeNull();
    expect(el.textContent).toContain("You can read this session");
  });

  test("its summary counts by kind, in the card's order", () => {
    expect(proposalSummary(["task", "edit", "task", "comment", "epic"])).toBe("1 epic, 2 tasks, 1 edit, 1 comment");
  });

  test("with nothing ticked, File stays, disabled, and the footer says to tick", async () => {
    const el = await mount(<ProposalCard items={ITEMS} selected={new Set()} filingAs="Ana" onToggle={() => undefined}
      onFile={() => { throw new Error("filed"); }} />);
    const file = el.querySelector<HTMLButtonElement>('[data-testid="file-proposal"]')!;
    expect(file.textContent).toBe("File");
    expect(file.disabled).toBe(true);
    expect(el.querySelector("footer")!.textContent).toContain("Tick what to file.");
    expect(el.textContent).not.toContain("Nothing to file");
  });
});

const filed = (by: string, key: string) => ({ by, key });
const task = (title: string, over: Partial<ProposalCardItem> = {}): ProposalCardItem =>
  ({ kind: "task", title, project: { key: "BL", name: "billing" }, canFile: true, ...over });

function Folding({ items }: { readonly items: ProposalCardItem[] }) {
  return <ProposalCard items={items} selected={new Set()} filingAs="Ana" onToggle={() => undefined} onFile={() => undefined} />;
}

describe("ProposalCard folds", () => {
  test("exactly when nothing on it is left for the person looking to file", () => {
    const yours = task("Meter runs");
    const theirs = task("Edit BL-58", { kind: "edit", canFile: false, why: "Only Márcio can file this" });
    const done = task("Usage panel", { filed: filed("Ana Nunes", "BL-61") });
    expect(nothingLeftToFile([done])).toBe(true);
    expect(nothingLeftToFile([done, theirs])).toBe(true);
    expect(nothingLeftToFile([theirs])).toBe(true);
    expect(nothingLeftToFile([done, yours])).toBe(false);
    expect(nothingLeftToFile([done, theirs, yours])).toBe(false);
    expect(nothingLeftToFile([yours], true)).toBe(true);
    expect(nothingLeftToFile([])).toBe(false);
  });

  test("all filed: one line naming what each became, an epic by its title, past four as +N more, and who filed it", async () => {
    const items: ProposalCardItem[] = [
      { kind: "epic", title: "Usage metering", canFile: true, filed: filed("Ana Nunes", "") },
      { kind: "epic", title: "Usage panel", canFile: true, filed: filed("Ana Nunes", "") },
      task("a", { child: true, filed: filed("Ana Nunes", "BILL-1") }),
      task("b", { child: true, filed: filed("Márcio Martins", "BILL-2") }),
      task("c", { child: true, filed: filed("Ana Nunes", "DASH-1") }),
    ];
    const el = await mount(<Folding items={items} />);
    const line = el.querySelector('[data-testid="proposal-fold"]')!;
    expect(line.textContent).toBe("Proposed work · 2 epics, 3 tasks · all filed: Usage metering, Usage panel, BILL-1, BILL-2, +1 more · by Ana and Márcio");
    expect(el.querySelector('[data-testid="file-proposal"]')).toBeNull();
    expect(el.querySelectorAll("li").length).toBe(0);

    expect(foldedWords(items.slice(2, 4))).toEqual(["all filed: BILL-1, BILL-2", "by Ana and Márcio"]);
    expect(foldedWords([items[2]!])).toEqual(["all filed: BILL-1", "by Ana"]);
    expect(foldedWords(items.slice(2, 5).map((i, n) => ({ ...i, filed: filed(["Ana", "João", "Lin"][n]!, `K-${n}`) }))))
      .toEqual(["all filed: K-0, K-1, K-2", "by Ana, João and Lin"]);
  });

  test("nothing left for you, some for others: how many are filed and how many wait for others", async () => {
    const theirs = (title: string) => task(title, { canFile: false, why: "Only Márcio can file this" });
    const el = await mount(<Folding items={[
      { kind: "epic", title: "E1", canFile: true, filed: filed("Ana", "") }, { kind: "epic", title: "E2", canFile: false, why: "x" },
      task("a", { filed: filed("Ana", "BILL-1") }), task("b", { filed: filed("Ana", "BILL-2") }), theirs("c"),
    ]} />);
    expect(el.querySelector('[data-testid="proposal-fold"]')!.textContent).toBe("Proposed work · 2 epics, 3 tasks · 3 filed · 2 for others to file");
    expect(foldedWords([theirs("c")])).toEqual(["1 for others to file"]);
  });

  test("the line is a toggle: it opens the whole card read only, and closes it", async () => {
    const el = await mount(<Folding items={[task("a", { filed: filed("Ana", "BILL-1") }),
      task("b", { kind: "edit", canFile: false, why: "Only Márcio can file this" })]} />);
    const line = el.querySelector<HTMLButtonElement>('[data-testid="proposal-fold"]')!;
    expect(line.tagName).toBe("BUTTON");
    expect(line.getAttribute("aria-expanded")).toBe("false");
    expect(el.querySelector('[data-testid="proposal-unfolded"]')).toBeNull();

    await act(async () => line.click());
    expect(line.getAttribute("aria-expanded")).toBe("true");
    const card = el.querySelector('[data-testid="proposal-unfolded"]')!;
    expect(card.querySelectorAll("li").length).toBe(2);
    expect(card.textContent).toContain("Ana filed BILL-1");
    expect(card.querySelector('[data-testid="cannot-file"]')!.textContent).toBe("Only Márcio can file this");
    expect([...card.querySelectorAll('[role="checkbox"]')].every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    expect(card.querySelector('[data-testid="file-proposal"]')).toBeNull();

    await act(async () => line.click());
    expect(line.getAttribute("aria-expanded")).toBe("false");
    expect(el.querySelector('[data-testid="proposal-unfolded"]')).toBeNull();
  });

  test("a card with something still yours to file stays open, filed items and all", async () => {
    const el = await mount(<Folding items={[task("a", { filed: filed("Ana", "BILL-1") }), task("b")]} />);
    expect(el.querySelector('[data-folded]')).toBeNull();
    expect(el.querySelector('[data-testid="proposal-fold"]')).toBeNull();
    expect(el.querySelector('[data-testid="file-proposal"]')).toBeTruthy();
  });
});

describe("SidebarSessions", () => {
  test("only the sessions given, the shared one with its owner's face, and New session", async () => {
    let opened = "";
    let created = false;
    const el = await mount(<SidebarSessions selected="s2" onSelect={(id) => { opened = id; }} onNew={() => { created = true; }}
      sessions={[{ id: "s1", title: "Mine alone" }, { id: "s2", title: "Billing", shared: true, owner: { id: "p_m", name: "Márcio Martins" } }]} />);
    const rows = [...el.querySelectorAll("[data-session]")] as HTMLButtonElement[];
    expect(rows.map((r) => r.textContent?.trim())).toEqual(["Mine alone", "BillingMM"]);
    expect(rows[1]!.getAttribute("aria-current")).toBe("page");
    expect(rows[1]!.querySelector('[aria-label="shared, Márcio Martins\'s"]')).toBeTruthy();
    expect(rows[0]!.querySelector('[aria-label^="shared"]')).toBeNull();
    await act(async () => rows[0]!.click());
    expect(opened).toBe("s1");
    await act(async () => byRole(el, "button", "New session").click());
    expect(created).toBe(true);
  });
});

describe("SessionPeople", () => {
  test("who has the session open is said in words, not colour alone", async () => {
    const el = await mount(<SessionPeople members={[
      { person: { id: "a", name: "Ana" }, role: "owner", open: true },
      { person: { id: "j", name: "João" }, role: "read" },
      { person: { id: "l", name: "Lin" }, role: "chat", invited: true },
    ]} />);
    const rows = [...el.querySelectorAll("li")].map((li) => [...li.children].slice(1).map((c) => c.textContent).join(" | "));
    expect(rows).toEqual(["Ana | Owner · here", "João | Can read", "Lin | Invited"]);
  });
});
