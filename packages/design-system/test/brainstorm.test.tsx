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
import { ProposalCard, SessionPeople, proposalSummary, type ProposalCardItem } from "../src/components/Brainstorm.tsx";
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
    const el = await mount(<Card onFile={() => { throw new Error("filed"); }} readOnly />);
    expect([...el.querySelectorAll('[role="checkbox"]')].every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    expect(el.querySelector('[data-testid="file-proposal"]')).toBeNull();
    expect(el.textContent).toContain("You can read this session");
  });

  test("its summary counts by kind, in the card's order", () => {
    expect(proposalSummary(["task", "edit", "task", "comment", "epic"])).toBe("1 epic, 2 tasks, 1 edit, 1 comment");
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
