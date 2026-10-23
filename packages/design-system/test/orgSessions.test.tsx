/**
 * Organisation sessions and @-mentions in the design system: the General
 * access row is one way and asks first; the composer's "@" opens the
 * mention menu as a combobox, picks without sending, and sends only the
 * people still named; an outside pick is no mention; the share confirm
 * offers Share and send to the owner alone; the session marks and the
 * sidebar's Open to everyone row read without colour.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useRef, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ChatComposer, mentionQueryAt, nameMatches, type ComposerSubmission } from "../src/components/ChatComposer.tsx";
import { AdminOwned, SessionPeople, SessionRow, SharedMark } from "../src/components/Brainstorm.tsx";
import { ChatMessage } from "../src/components/ChatMessage.tsx";
import { GeneralAccessRow, MentionChip, MentionShareConfirm, type MentionItem } from "../src/components/SessionAccess.tsx";
import { Sidebar, SidebarSessions } from "../src/components/Sidebar.tsx";
import { Section } from "../src/primitives/Layout.tsx";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { accessibleName, byRole } from "./queries.ts";

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

const type = async (area: HTMLTextAreaElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, value);
    area.setSelectionRange(value.length, value.length);
    area.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const key = (area: HTMLTextAreaElement, k: string) =>
  act(async () => void area.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const frame = () => act(async () => await new Promise((r) => requestAnimationFrame(() => r(undefined))));

const GUS = { id: "per_gus", name: "Gus Lindqvist" };
const ELI = { id: "per_eli", name: "Eli Haddad" };
const PEOPLE: MentionItem[] = [
  { person: ELI, detail: "In this session · can chat" },
  { person: GUS, detail: "Not in it yet · can open it and chat" },
];

function Composer({ items = PEOPLE, sent, picked }: {
  readonly items?: MentionItem[]; readonly sent: ComposerSubmission[]; readonly picked?: MentionItem[];
}) {
  return <ChatComposer mode="chat" mentionables={items} onMention={(m) => picked?.push(m)}
    onSubmit={(s) => { sent.push(s); return true; }} />;
}

describe("the mention query", () => {
  test("an @ at a word start opens it; one inside a word, or after a line break, does not", () => {
    expect(mentionQueryAt("hi @gu", 6)).toEqual({ at: 3, query: "gu" });
    expect(mentionQueryAt("@", 1)).toEqual({ at: 0, query: "" });
    expect(mentionQueryAt("mail a@b", 8)).toBe(null);
    expect(mentionQueryAt("@gus\nx", 6)).toBe(null);
  });
  test("any word of the name matches, ignoring case and accents", () => {
    expect(nameMatches("Márcia Costa", "cos")).toBe(true);
    expect(nameMatches("Márcia Costa", "marc")).toBe(true);
    expect(nameMatches("Márcia Costa", "gus")).toBe(false);
  });
});

describe("ChatComposer with mentionables", () => {
  test("@ at a word start opens the menu as a combobox; Enter picks and does not send", async () => {
    const sent: ComposerSubmission[] = [];
    const el = await mount(<Composer sent={sent} />);
    const area = el.querySelector("textarea")!;
    expect(area.getAttribute("role")).toBe("combobox");
    expect(area.getAttribute("aria-expanded")).toBe("false");
    await type(area, "ask @g");
    expect(area.getAttribute("aria-expanded")).toBe("true");
    const list = byRole(el, "listbox", "People to mention");
    expect(area.getAttribute("aria-controls")).toBe(list.id);
    const options = [...list.querySelectorAll('[role="option"]')];
    expect(options.length).toBe(1);
    expect(options[0]!.textContent).toContain("Gus Lindqvist");
    expect(options[0]!.textContent).toContain("Not in it yet · can open it and chat");
    expect(area.getAttribute("aria-activedescendant")).toBe(options[0]!.id);
    await key(area, "Enter");
    await frame();
    expect(sent).toEqual([]);
    expect(area.value).toBe("ask @Gus Lindqvist ");
    expect(area.getAttribute("aria-expanded")).toBe("false");
    await key(area, "Enter");
    expect(sent).toEqual([{ mode: "chat", text: "ask @Gus Lindqvist", attachmentIds: [], mentions: ["per_gus"] }]);
  });

  test("arrows move the active option; Escape closes without picking", async () => {
    const sent: ComposerSubmission[] = [];
    const el = await mount(<Composer sent={sent} />);
    const area = el.querySelector("textarea")!;
    await type(area, "@");
    const options = () => [...el.querySelectorAll('[role="option"]')];
    expect(options().map((o) => o.getAttribute("aria-selected"))).toEqual(["true", "false"]);
    await key(area, "ArrowDown");
    expect(options().map((o) => o.getAttribute("aria-selected"))).toEqual(["false", "true"]);
    await key(area, "Escape");
    expect(el.querySelectorAll('[role="listbox"]').length).toBe(0);
    expect(area.value).toBe("@");
  });

  test("a deleted mention is not in mentions", async () => {
    const sent: ComposerSubmission[] = [];
    const el = await mount(<Composer sent={sent} />);
    const area = el.querySelector("textarea")!;
    await type(area, "@eli");
    await key(area, "Enter");
    await frame();
    await type(area, "never mind");
    await key(area, "Enter");
    expect(sent).toEqual([{ mode: "chat", text: "never mind", attachmentIds: [] }]);
  });

  test("an outside pick is not in mentions and calls onMention", async () => {
    const sent: ComposerSubmission[] = [];
    const picked: MentionItem[] = [];
    const items: MentionItem[] = [PEOPLE[0]!, { ...PEOPLE[1]!, outside: true }];
    const el = await mount(<Composer sent={sent} items={items} picked={picked} />);
    const area = el.querySelector("textarea")!;
    await type(area, "@gu");
    expect(el.querySelector('[role="option"]')!.textContent).toContain("Not in this session");
    await key(area, "Tab");
    await frame();
    expect(picked.map((p) => p.person.id)).toEqual(["per_gus"]);
    await key(area, "Enter");
    expect(sent).toEqual([{ mode: "chat", text: "@Gus Lindqvist", attachmentIds: [] }]);
  });

  test("Share and send through submitRef sends shareWith, and mentions the person shared with", async () => {
    const sent: ComposerSubmission[] = [];
    function Sharing() {
      const submitRef = useRef<((extra?: { readonly shareWith?: ReadonlyArray<string> }) => void) | null>(null);
      const [pending, setPending] = useState<MentionItem | null>(null);
      return <ChatComposer mode="chat" mentionables={[PEOPLE[0]!, { ...PEOPLE[1]!, outside: true }]} submitRef={submitRef}
        onMention={(m) => m.outside && setPending(m)} onSubmit={(s) => { sent.push(s); return true; }}
        below={pending ? <MentionShareConfirm person={pending.person} owner ownerName="Márcia"
          onShare={() => submitRef.current?.({ shareWith: [pending.person.id!] })} onPlain={() => submitRef.current?.()}
          onCancel={() => setPending(null)} /> : null} />;
    }
    const el = await mount(<Sharing />);
    const area = el.querySelector("textarea")!;
    await type(area, "@gus");
    await key(area, "Enter");
    await frame();
    await act(async () => byRole(el, "button", "Share and send").click());
    expect(sent).toEqual([{ mode: "chat", text: "@Gus Lindqvist", attachmentIds: [], mentions: ["per_gus"], shareWith: ["per_gus"] }]);
  });

  test("without mentionables an @ is plain text: no combobox, no menu", async () => {
    const el = await mount(<ChatComposer mode="chat" onSubmit={() => true} />);
    const area = el.querySelector("textarea")!;
    expect(area.getAttribute("role")).toBe(null);
    await type(area, "@g");
    expect(el.querySelectorAll('[role="listbox"]').length).toBe(0);
  });
});

describe("MentionShareConfirm", () => {
  test("the owner may share and send, send without sharing, or cancel; anyone else only sends plain text", async () => {
    const calls: string[] = [];
    const el = await mount(<>
      <MentionShareConfirm person={GUS} owner ownerName="Márcia Costa" onShare={() => calls.push("share")}
        onPlain={() => calls.push("plain")} onCancel={() => calls.push("cancel")} />
      <MentionShareConfirm person={GUS} owner={false} ownerName="Márcia Costa" onPlain={() => calls.push("plain-other")}
        onCancel={() => calls.push("cancel-other")} />
    </>);
    const [mine, theirs] = [...el.querySelectorAll('[role="group"]')];
    expect(accessibleName(mine!)).toBe("Mention Gus Lindqvist");
    expect([...mine!.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Share and send", "Send without sharing", "Cancel"]);
    expect(mine!.textContent).toContain("Share this session with Gus so they can see the mention?");
    expect([...theirs!.querySelectorAll("button")].map((b) => b.textContent)).toEqual(["Send as plain text", "Cancel"]);
    expect(theirs!.textContent).toContain("Ask Márcia to share it");
    for (const b of [...mine!.querySelectorAll("button"), ...theirs!.querySelectorAll("button")]) await act(async () => (b as HTMLButtonElement).click());
    expect(calls).toEqual(["share", "plain", "cancel", "plain-other", "cancel-other"]);
  });
});

describe("GeneralAccessRow", () => {
  test("open to the organisation, the row is read-only and says it can't be undone", async () => {
    const el = await mount(<GeneralAccessRow org="Acme" value="organisation" editable />);
    expect(el.querySelectorAll("button").length).toBe(0);
    expect(el.textContent).toContain("Everyone in Acme");
    expect(el.textContent).toContain("This can't be undone.");
    expect(el.textContent).toContain("Can chat");
  });

  test("private for someone else: words, no control", async () => {
    const el = await mount(<GeneralAccessRow org="Acme" value="private" />);
    expect(el.querySelectorAll("button").length).toBe(0);
    expect(el.textContent).toContain("Private");
  });

  test("the owner's confirm opens it or keeps it private", async () => {
    const calls: string[] = [];
    const el = await mount(<GeneralAccessRow org="Acme" value="private" editable confirming
      onConfirm={() => calls.push("open")} onCancel={() => calls.push("keep")} />);
    expect(el.textContent).toContain("Everyone in Acme will be able to read and chat. This can't be undone.");
    await act(async () => byRole(el, "button", "Open to everyone in Acme").click());
    await act(async () => byRole(el, "button", "Keep it private").click());
    expect(calls).toEqual(["open", "keep"]);
  });
});

describe("session marks", () => {
  test("the organisation mark and the admin-owned mark are named in words", async () => {
    const el = await mount(<>
      <SharedMark scope="organisation" org="Acme" showLabel />
      <SharedMark owner={GUS} />
      <AdminOwned org="Acme" />
      <MentionChip person={GUS} you />
    </>);
    const imgs = [...el.querySelectorAll('[role="img"]')].map(accessibleName)
      .filter((n) => n !== "Gus Lindqvist" && n !== "GL"); // the faces' own names
    expect(imgs).toEqual(["Everyone in Acme", "Owner left Acme — owned by Acme's admins", "Mention: Gus Lindqvist (you)"]);
    expect(el.textContent).toContain("Everyone in Acme");
    expect(el.textContent).toContain("@Gus");
  });

  test("a session row open to the organisation shows its mark, not the shared one, and its origin line", async () => {
    const el = await mount(<ul><SessionRow title="CSV export" summary="Nothing filed yet" projects={[]} state="Talking" onOpen={() => {}}
      access="organisation" org="Acme" shared={{ owner: GUS }} origin="From somewhere" /></ul>);
    expect(el.querySelectorAll('[data-scope="organisation"]').length).toBe(1);
    expect(el.querySelectorAll('[aria-label="Shared, Gus Lindqvist\'s"]').length).toBe(0);
    expect(el.textContent).toContain("From somewhere");
  });
});

describe("SidebarSessions' Open to everyone row", () => {
  test("is the last row with its count, stays when the list is folded, and opens its list", async () => {
    let opened = 0;
    const el = await mount(<SidebarSessions sessions={[{ id: "s1", title: "Billing", access: "organisation" }]} onSelect={() => {}} onNew={() => {}}
      folded organisation={{ count: 2, onOpen: () => opened++ }} />);
    const row = el.querySelector<HTMLButtonElement>("[data-testid=sidebar-open-to-everyone]")!;
    expect(accessibleName(row)).toBe("Open to everyone, 2 joined");
    expect(row.textContent).toBe("Open to everyone2");
    expect(el.querySelectorAll("[data-session]").length).toBe(0);
    await act(async () => row.click());
    expect(opened).toBe(1);
  });

  test("an organisation session in the list carries the building mark", async () => {
    const el = await mount(<SidebarSessions sessions={[{ id: "s1", title: "Billing", access: "organisation", shared: true },
      { id: "s2", title: "Pricing", shared: true }]}
      onSelect={() => {}} onNew={() => {}} />);
    expect(el.querySelectorAll('[role="img"][aria-label="open to everyone"]').length).toBe(1);
    expect(el.querySelectorAll('[role="img"][aria-label="shared"]').length).toBe(1);
    expect(el.querySelectorAll('[data-session="s1"] [aria-label="shared"]').length).toBe(0);
  });

  test("the rail's Sessions tooltip counts the organisation sessions you joined", async () => {
    const el = await mount(<TooltipProvider><Sidebar projects={[]} title="dude" collapsed onCollapsedChange={() => {}}
      railSessions={{ onNew: () => {}, onOpenList: () => {}, recent: ["Billing"], organisationCount: 2 }} /></TooltipProvider>);
    await act(async () => byRole(el, "button", "Sessions").focus());
    const tip = await (async () => {
      for (let i = 0; i < 20; i++) {
        const t = document.querySelector("[data-testid=rail-sessions-organisation]");
        if (t) return t;
        await act(async () => await new Promise((r) => setTimeout(r, 10)));
      }
      return null;
    })();
    expect(tip?.textContent).toBe("2 open to everyone you joined");
  });
});

describe("Section's note", () => {
  test("sits beside the label, out of its heading and its capitals", async () => {
    const el = await mount(<Section title="Joined · active" count={2} note="the sidebar's count"><p>rows</p></Section>);
    const heading = el.querySelector("h2")!;
    expect(heading.textContent).toBe("Joined · active2");
    expect(heading.classList.contains("ds-label")).toBe(true);
    const note = [...el.querySelectorAll("span")].find((s) => s.textContent === "the sidebar's count")!;
    expect(heading.contains(note)).toBe(false);
  });
});

describe("SharedMark for an organisation session", () => {
  test("carries the owner's face when given, named with it", async () => {
    const el = await mount(<>
      <SharedMark scope="organisation" org="Acme" owner={GUS} />
      <ul><SessionRow title="CSV" summary="x" projects={[]} state="Talking" onOpen={() => {}} access="organisation" org="Acme" shared={{ owner: ELI }} /></ul>
    </>);
    const marks = [...el.querySelectorAll('[data-scope="organisation"]')];
    expect(marks.map(accessibleName)).toEqual(["Everyone in Acme, Gus Lindqvist's", "Everyone in Acme, Eli Haddad's"]);
    // The face itself, by its initials, in each mark; none without an owner.
    expect(marks.map((m) => m.querySelector("[data-identity]")?.textContent)).toEqual(["GL", "EH"]);
    const bare = await mount(<SharedMark scope="organisation" org="Acme" />);
    expect(bare.querySelectorAll("[data-identity]").length).toBe(0);
  });
});

describe("mentions in a message's text", () => {
  test("each @Name of a mentioned person is its chip, in place; the longest name wins; code and links stay text", async () => {
    const chip = (p: { id: string; name: string }) => <MentionChip person={p} />;
    const ANA = { id: "per_ana", name: "Ana" };
    const ANA_N = { id: "per_ana_n", name: "Ana Nunes" };
    const el = await mount(<ChatMessage role="human" content={"@Ana Nunes and @Ana, not `@Ana` nor [@Ana](https://example.com) nor me@Ana"}
      mentions={[{ name: ANA.name, chip: chip(ANA) }, { name: ANA_N.name, chip: chip(ANA_N) }]} />);
    const chips = [...el.querySelectorAll("[data-testid=mention-chip]")];
    expect(chips.map(accessibleName)).toEqual(["Mention: Ana Nunes", "Mention: Ana"]);
    expect(el.querySelector("p")!.textContent).toContain(", not @Ana nor @Ana nor me@Ana");
    expect(el.querySelector("code")!.textContent).toBe("@Ana");
    expect(el.querySelector("a")!.querySelectorAll("[data-testid=mention-chip]").length).toBe(0);
  });

  test("a name must end where the mention does: @Anabel is not Ana, @Ana. and @Ana's are", async () => {
    const ANA = { id: "per_ana", name: "Ana" };
    const el = await mount(<ChatMessage role="human" content={"@Anabel, @Ana2 and @Ana. Then @Ana's"}
      mentions={[{ name: ANA.name, chip: <MentionChip person={ANA} /> }]} />);
    expect(el.querySelectorAll("[data-testid=mention-chip]").length).toBe(2);
    expect(el.querySelector("p")!.textContent).toContain("@Anabel, @Ana2 and");
  });
});

describe("SessionPeople", () => {
  test("an owner who left reads Left <org> · No access, never Owner", async () => {
    const el = await mount(<SessionPeople members={[{ person: GUS, role: "owner", left: "Acme", open: true }, { person: ELI, role: "chat" }]} />);
    const rows = [...el.querySelectorAll("li")].map((li) => li.textContent);
    expect(rows[0]).toContain("Left Acme · No access");
    expect(rows[0]).not.toContain("Owner");
    expect(rows[0]).not.toContain("here");
    expect(rows[1]).toContain("Can chat");
  });
});
