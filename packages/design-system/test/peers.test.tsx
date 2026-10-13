/**
 * Agents on an epic: each piece names itself as assistive technology reads
 * it, says what it says in words (no tone carries a meaning alone), offers
 * a person's actions only where they apply, and folds and opens with the
 * state a screen reader hears. Mounted in happy-dom.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, type ReactElement } from "react";
import { createRoot, type Root } from "react-dom/client";
import {
  EpicNote, EpicNoteCount, EpicNoteHistory, EpicNoteNotice, EpicNoteRow, PeerList, PeerMessage, PeerThread, PeerWaitLine, PeerWaitMark,
  SinceDigest, TaskChip, noteWordChange, type EpicNoteData, type PeerMessageData,
} from "../src/components/Peer.tsx";
import { EpicCard } from "../src/components/EpicCard.tsx";
import { ProposalCard, proposalSummary } from "../src/components/Brainstorm.tsx";
import { Section } from "../src/primitives/Layout.tsx";
import { ICON_NAMES } from "../src/icons/index.tsx";
import { allByRole, byRole } from "./queries.ts";

let root: Root | null = null;
let host: HTMLElement | null = null;
afterEach(async () => {
  await act(async () => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(el: ReactElement): Promise<HTMLElement> {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(el));
  return host;
}

const t = (hm: string) => new Date(2026, 9, 12, Number(hm.slice(0, 2)), Number(hm.slice(3, 5)), Number(hm.slice(6, 8) || 0));
const words = (el: Element) => (el.textContent ?? "").replace(/\s+/g, " ").trim();

const ABS4 = { key: "ABS-4", title: "Frontend: preview panel", status: "running" as const, role: "implementer" as const, live: true };
const ABS2 = { key: "ABS-2", title: "Backend: preview endpoints", status: "running" as const, role: "implementer" as const, live: true };

const ASK: PeerMessageData = {
  id: "pm_1", from: ABS4, to: ABS2, text: "What body will `/v1/metrics/preview/*` take?", sentAt: t("13:43:20"),
  delivery: { kind: "read", at: t("13:43:52"), after: "Bash" },
};

describe("TaskChip", () => {
  test("is a button that says what it opens, or an image with the same name", async () => {
    let opened = 0;
    const el = await mount(<><TaskChip task={ABS2} onOpen={() => opened++} /><TaskChip task={ABS4} showRole /></>);
    const button = byRole(el, "button", "Open ABS-2, Backend: preview endpoints: its implementer");
    await act(async () => button.click());
    expect(opened).toBe(1);
    const img = byRole(el, "img", "ABS-4, Frontend: preview panel: its implementer");
    expect(words(img)).toContain("Implementer");
  });
});

describe("PeerMessage", () => {
  test("a read message says when it was sent and read, and after what", async () => {
    const el = await mount(<PeerMessage msg={ASK} viewer="ABS-2" />);
    const turn = el.querySelector("article")!;
    expect(words(turn)).toContain("sent 13:43 · read 13:43:52, after Bash");
    expect(words(turn)).toContain("Peer");
    expect(words(turn)).toContain("you");
    expect(turn.getAttribute("data-unread")).toBeNull();
    expect(el.querySelector('[data-testid="peer-lands"]')).toBeNull();
  });

  test("queued, it lands at the peer's next step; held, it says the orchestrator's notice", async () => {
    const queued = await mount(<PeerMessage msg={{ ...ASK, delivery: { kind: "queued" } }} />);
    expect(words(queued.querySelector('[data-testid="peer-lands"]')!)).toBe("Lands at ABS-2's next step. Nothing is interrupted.");
    expect(queued.querySelector("article")!.getAttribute("data-unread")).toBe("true");
    await act(async () => root!.render(<PeerMessage msg={{ ...ASK, delivery: { kind: "inbox", notice: "Notice from the payload." } }} />));
    expect(words(queued.querySelector('[data-testid="peer-lands"]')!)).toBe("Notice from the payload.");
    await act(async () => root!.render(<PeerMessage msg={{ ...ASK, delivery: { kind: "inbox" } }} />));
    expect(words(queued.querySelector('[data-testid="peer-lands"]')!)).toBe("Held for ABS-2's next Run.");
  });

  test("in the sender's transcript it carries on, and names the wait it does not open", async () => {
    const el = await mount(<PeerMessage msg={ASK} viewer="ABS-4" carryingOn />);
    expect(words(el.querySelector('[data-testid="peer-carrying-on"]')!)).toContain("Sent · carrying on.");
    expect(words(el)).toContain("dude wait pm_1");
    expect(words(el.querySelector("header")!)).toMatch(/^You/);
  });

  test("a reply is tagged Reply and quotes what it answers; a refused one says why instead of where it lands", async () => {
    const el = await mount(<PeerMessage msg={{ ...ASK, id: "pm_2", from: ABS2, to: ABS4, replyTo: "pm_1" }} replyToText={ASK.text} />);
    expect(words(el)).toContain("Reply");
    expect(words(el)).toContain("Replying toWhat body will /v1/metrics/preview/* take?");
    await act(async () => root!.render(<PeerMessage msg={{ ...ASK, delivery: { kind: "queued" } }} carryingOn refused="5 messages in the last hour" />));
    expect(byRole(el, "status").textContent).toContain("Not sent: 5 messages in the last hour");
    expect(el.querySelector('[data-testid="peer-lands"]')).toBeNull();
    expect(el.querySelector('[data-testid="peer-carrying-on"]')).toBeNull();
  });
});

describe("PeerWaitLine", () => {
  test("waiting, it says why and when the Run parks; answered, the span it waited", async () => {
    const el = await mount(<PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={t("13:47:02")} parksAt={t("13:57:02")} now={t("13:53:02")} />);
    const line = byRole(el, "status", "Waiting on ABS-2 since 13:47; parks at 13:57 if no reply");
    expect(words(line)).toContain("ran dude wait pm_1");
    // The time left at `now`, not the whole grace.
    expect(words(line)).toContain("parks in 4 min, at 13:57, if no reply");
    await act(async () => root!.render(<PeerWaitLine on="ABS-2" msg="pm_1" how="turn-ended" since={t("13:47:02")} parksAt={t("13:57:02")}
      ended={{ at: t("13:49:06"), by: "reply" }} />));
    const done = byRole(el, "status", "Waited on ABS-2 from 13:47 to 13:49: answered; the reply resumed the turn");
    expect(words(done)).toContain("turn ended with its question to ABS-2 unanswered");
  });

  test("a peer that ended without replying says the orchestrator's line", async () => {
    const line = "ABS-2 ended without replying (status: aborted). Carry on with your assumption, or ask a person.";
    const el = await mount(<PeerWaitLine on="ABS-2" msg="pm_1" how="wait" since={t("13:47:02")}
      ended={{ at: t("13:50:00"), by: "peer_ended", status: "aborted", line }} />);
    expect(words(el)).toContain(line);
  });
});

const PERSON = { id: "u_m", name: "Márcio Martins" };
const NOTE: EpicNoteData = {
  id: "en_4", n: 4, text: "**Preview API.** Columns `_mean`, `_variance`.", by: { kind: "agent", task: "ABS-2", role: "implementer", for: PERSON },
  at: t("13:51:20"), readBy: 3, replaces: 3,
};

describe("EpicNote", () => {
  test("a current note says who, when, what it replaces and how many have read it, with a person's two actions", async () => {
    const acts: string[] = [];
    const el = await mount(<ul><EpicNote note={NOTE} live={4} isNew onReplace={() => acts.push("replace")} onRetire={() => acts.push("retire")} /></ul>);
    const note = el.querySelector("li")!;
    expect(words(note)).toContain("Note 4");
    expect(words(note)).toContain("Implementer on ABS-2");
    expect(words(note)).toContain("replaces note 3");
    expect(words(note)).toContain("read by 3 of 4 agents");
    expect(words(note)).toContain("New");
    await act(async () => byRole(el, "button", "Replace note 4").click());
    await act(async () => byRole(el, "button", "Retire note 4").click());
    expect(acts).toEqual(["replace", "retire"]);
  });

  test("a note from a brainstorm says who filed it; replaced or retired, it is history with no actions", async () => {
    const filed: EpicNoteData = { ...NOTE, n: 1, by: { kind: "person", person: PERSON }, fromSession: true, replaces: null };
    const el = await mount(<ul>
      <EpicNote note={filed} live={4} />
      <EpicNote note={{ ...NOTE, n: 3, replacedBy: { n: 4, at: t("13:51:20") } }} live={4} onReplace={() => undefined} onRetire={() => undefined} />
      <EpicNote note={{ ...filed, n: 2, retired: { by: "Márcio Martins", at: t("15:26:00") } }} live={4} onRetire={() => undefined} />
    </ul>);
    const [one, three, two] = [...el.querySelectorAll("li")];
    expect(words(one!)).toContain("From a brainstorm session, filed by Márcio");
    expect(words(three!)).toContain("Replaced by note 4 at 13:51");
    expect(words(two!)).toContain("Retired by Márcio at 15:26");
    expect(allByRole(el, "button")).toHaveLength(0);
  });

  test("the count names its bound", async () => {
    const el = await mount(<EpicNoteCount count={3} limit={10} />);
    expect(el.firstElementChild!.getAttribute("aria-label")).toBe("3 of at most 10 notes");
    expect(words(el)).toBe("3 of 10");
  });

  test("the history folds to one line that controls the list it opens", async () => {
    const el = await mount(<EpicNoteHistory live={4} notes={[
      { ...NOTE, id: "en_3", n: 3, replacedBy: { n: 4, at: t("13:51:20") } },
      { ...NOTE, id: "en_2", n: 2, retired: { by: "Márcio Martins", at: t("15:26:00") } },
    ]} />);
    const line = byRole(el, "button");
    expect(words(line)).toBe("1 replaced note, 1 retired note· no agent is briefed with these any more");
    expect(line.getAttribute("aria-expanded")).toBe("false");
    expect(el.querySelectorAll("li")).toHaveLength(0);
    await act(async () => line.click());
    expect(line.getAttribute("aria-expanded")).toBe("true");
    expect(document.getElementById(line.getAttribute("aria-controls")!)!.querySelectorAll("li")).toHaveLength(2);
  });

  test("no history draws nothing", async () => {
    const el = await mount(<EpicNoteHistory notes={[]} live={4} />);
    expect(el.innerHTML).toBe("");
  });

  test("an agent's own note call says what it pinned and replaced", async () => {
    const el = await mount(<EpicNoteRow note={NOTE} />);
    expect(byRole(el, "note", "Epic note 4 pinned, replacing note 3").textContent).toContain("Note 4 pinned for every agent on the epic · replaces note 3");
  });
});

describe("EpicNoteNotice", () => {
  test("added, replaced and retired each say what changed and who did it", async () => {
    const el = await mount(<>
      <EpicNoteNotice change="added" note={{ ...NOTE, n: 3, replaces: null }} read={{ at: t("13:49:06") }} />
      <EpicNoteNotice change="replaced" note={NOTE} was={{ n: 3, text: "**Preview API.** Columns `_mean`, `_var`." }}
        changed={{ now: "_variance", was: "_var" }} read={{ at: t("13:51:24"), after: "Edit" }} />
      <EpicNoteNotice change="retired" note={{ ...NOTE, n: 2, retired: { by: "Márcio Martins", at: t("15:26:00") } }} read={{ at: t("15:26:04") }} />
    </>);
    expect(words(byRole(el, "note", "Epic note 3 added by ABS-2's implementer"))).toContain("Epic note added: 3");
    const replaced = byRole(el, "note", "Epic note 4 replaced note 3, by ABS-2's implementer");
    expect(words(replaced)).toContain("Epic note replaced: 3 → 4");
    expect(words(replaced)).toContain("Preview API … _variance (was: _var)");
    expect(words(replaced)).toContain("read 13:51:24, after Edit");
    expect(words(byRole(el, "note", "Epic note 2 retired by Márcio Martins"))).toContain("no longer in force: Preview API. Columns _mean, _variance.");
  });

  test("the words that changed are found from the two texts", () => {
    expect(noteWordChange("Columns `_mean`, `_var`, `_min`.", "Columns `_mean`, `_variance`, `_min`.")).toEqual({ now: "_variance", was: "_var" });
    expect(noteWordChange("same", "same")).toBeNull();
    expect(noteWordChange("a b c d e f g h i j", "z y x w v u t s r q")).toBeNull();
  });
});

describe("PeerThread", () => {
  test("says the pair, the subject and its state, and has nothing to write with", async () => {
    const el = await mount(
      <PeerThread a={ABS4} b={ABS2} subject="Preview API body" state={{ kind: "waiting", on: "ABS-2", since: t("13:47:02") }}
        foot="Everyone on the epic can read it; only the two agents write in it.">
        <PeerMessage compact msg={ASK} />
      </PeerThread>,
    );
    const thread = el.querySelector("section")!;
    expect(thread.getAttribute("aria-label")).toBe("ABS-4 and ABS-2: Preview API body");
    expect(words(thread)).toContain("Waiting for ABS-2's reply since 13:47");
    expect(words(thread)).toContain("only the two agents write in it");
    expect(el.querySelectorAll("textarea, input")).toHaveLength(0);
    expect(allByRole(el, "button")).toHaveLength(0);
    await act(async () => root!.render(<PeerThread a={ABS4} b={ABS2} subject="s" state={{ kind: "open", on: "ABS-2" }}>x</PeerThread>));
    expect(words(el)).toContain("No reply from ABS-2 yet · nobody waits");
    await act(async () => root!.render(<PeerThread a={ABS4} b={ABS2} subject="s" state={{ kind: "answered", at: t("13:49:05") }}>x</PeerThread>));
    expect(words(el)).toContain("Answered 13:49");
  });
});

describe("SinceDigest", () => {
  test("one line that opens to its groups", async () => {
    const el = await mount(<SinceDigest since={t("13:44")} woke={t("14:20")} counts="2 threads · 1 note change"
      groups={[{ title: "Messages with ABS-2", items: [{ id: "a", lead: "ABS-4", text: "asked for the body", at: t("13:43") }] }]} />);
    const line = byRole(el, "button");
    expect(words(line)).toContain("Since the conductor last looked, on this epic");
    expect(words(line)).toContain("13:44 → 14:20 · in its briefing");
    expect(line.getAttribute("aria-expanded")).toBe("false");
    expect(words(el)).not.toContain("asked for the body");
    await act(async () => line.click());
    expect(words(document.getElementById(line.getAttribute("aria-controls")!)!)).toContain("Messages with ABS-2ABS-4asked for the body13:43");
  });
});

describe("PeerWaitMark", () => {
  test("names every pair; several are counted; none draws nothing", async () => {
    const el = await mount(<PeerWaitMark pairs={[["ABS-4", "ABS-2"]]} />);
    expect(byRole(el, "img", "Agents waiting on each other: ABS-4 waits on ABS-2").textContent).toBe("ABS-4 waits on ABS-2");
    await act(async () => root!.render(<PeerWaitMark pairs={[["ABS-4", "ABS-2"], ["ABS-2", "ABS-1"]]} />));
    expect(byRole(el, "img", "Agents waiting on each other: ABS-4 waits on ABS-2, ABS-2 waits on ABS-1").textContent).toBe("2 agents waiting on peers");
    await act(async () => root!.render(<PeerWaitMark pairs={[]} />));
    expect(el.innerHTML).toBe("");
  });
});

describe("PeerList", () => {
  const list = (el: ParentNode, label: string) => el.querySelector<HTMLElement>(`ul[aria-label="${label}"]`)!;
  const PEERS = [
    { ...ABS4, phase: "implement", run: "live" as const },
    { ...ABS2, phase: "implement", run: "parked" as const, branch: "dude/abs2/attempt-1", head: "b71e04d9f", files: [{ path: "backend/src/preview.ts", add: 188, del: 0 }],
      lastSaid: "Pinned the `body`.", waitingOn: "ABS-1", forYou: { count: 1, waits: true } },
    { key: "JERV-2", title: "Setup preview", status: "running" as const, role: "implementer" as const, phase: "implement", run: "none" as const,
      link: { dependsOn: "ABS-2", project: "jervasion" } },
  ];
  test("tool: the caller's own row says you; a peer's facts, its message for you, its wait and its link", async () => {
    const el = await mount(<PeerList variant="tool" label="Peers of ABS-4" self="ABS-4" peers={PEERS} />);
    const ul = list(el, "Peers of ABS-4");
    const [self, two, jerv] = [...ul.querySelectorAll("li")];
    expect(words(self!)).toBe("RunningABS-4Frontend: preview panelyou");
    expect(words(two!)).toContain("implement · implementer · parked");
    expect(words(two!)).toContain("dude/abs2/attempt-1 @ b71e04d");
    expect(words(two!)).toContain("preview.ts +188");
    expect(words(two!)).toContain("1 message for you, waits on your reply");
    expect(words(two!)).toContain("ABS-2 waits on ABS-1");
    expect(words(two!)).toContain("“Pinned the body.”");
    expect(words(jerv!)).toContain("depends on ABS-2 · project jervasion");
    expect(words(jerv!)).toContain("no Run going");
  });
  test("rail: chip, title and phase only; page: what it last said, and opening a peer", async () => {
    const opened: string[] = [];
    const el = await mount(<><PeerList variant="rail" label="Peers" peers={PEERS} /><PeerList label="Tasks on this epic" peers={PEERS} onOpen={(k) => opened.push(k)} /></>);
    const rail = list(el, "Peers");
    expect(words(rail)).not.toContain("dude/abs2");
    expect(words(rail)).not.toContain("message for you");
    const page = list(el, "Tasks on this epic");
    expect(words(page)).toContain("Pinned the body.");
    await act(async () => byRole(page, "button", "Open ABS-2, Backend: preview endpoints: its implementer").click());
    expect(opened).toEqual(["ABS-2"]);
  });
});

describe("the changed pieces", () => {
  test("the pin glyph is in the icon set", () => {
    expect(ICON_NAMES).toContain("pin");
  });

  test("Section's limit says the count against its bound", async () => {
    const el = await mount(<Section title="Epic notes" count={3} limit={10} countNoun="notes">x</Section>);
    const limit = el.querySelector('[data-testid="section-limit"]')!;
    expect(words(limit)).toBe("3 of 10");
    expect(limit.getAttribute("aria-label")).toBe("3 of at most 10 notes");
    await act(async () => root!.render(<Section title="Tasks" count={4}>x</Section>));
    expect(el.querySelector('[data-testid="section-limit"]')).toBeNull();
    expect(words(el.querySelector("h2")!)).toBe("Tasks4");
  });

  test("EpicCard's waiting slot sits in its head, apart from its actions", async () => {
    const el = await mount(<EpicCard epic={{ id: "e", title: "Preview", lanes: { done: 0, review: 0, progress: 1, backlog: 0 }, tasks: 1, prs: {}, people: [], costUsd: 0 }}
      waiting={<PeerWaitMark pairs={[["ABS-4", "ABS-2"]]} />} actions={<button type="button">menu</button>} />);
    const head = byRole(el, "img", "Agents waiting on each other: ABS-4 waits on ABS-2").parentElement!;
    expect(head.contains(byRole(el, "button", "Preview"))).toBe(true);
    expect(head.lastElementChild!.textContent).toBe("menu");
  });

  test("ProposalCard: a note item, and a proposed epic's notes pinned when it is filed", async () => {
    expect(proposalSummary(["epic", "note", "task", "note"])).toBe("1 epic, 1 task, 2 notes");
    const el = await mount(<ProposalCard filingAs="Márcio" selected={new Set([0, 1])} onToggle={() => undefined} onFile={() => undefined} items={[
      { kind: "epic", title: "Metric preview", detail: "Preview a metric.", canFile: true, project: { key: "ABS", name: "abs" },
        notes: ["**Preview API.** ABS-2 serves it.", "Read `INDEX.md`."] },
      { kind: "note", title: "Replace note 3 on Metric preview", before: "old", after: "new", canFile: true, project: { key: "ABS", name: "abs" } },
    ]} />);
    const block = el.querySelector('[data-testid="proposed-notes"]')!;
    expect(words(block)).toContain("2 epic notes, pinned when it is filed");
    expect(block.querySelectorAll("li")).toHaveLength(2);
    expect(el.querySelector('[data-kind="note"]')!.textContent).toContain("Note");
    expect(words(el)).toContain("Filing as Márcio: 1 epic, 1 note. Nothing starts.");
  });
});
