/**
 * The accounts components, mounted in happy-dom: the organisation switcher
 * (what its trigger is called, what its menu lists and does, by keyboard
 * too), the email chips (paste parsing, every chip state, Backspace and ✕,
 * the summary), the invitation links (Copy and Copy all's lines), the key
 * shown once (Copy and the usage lines) and the organisation's face.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";
import { OrgFace } from "../src/components/OrgFace.tsx";
import { OrgSwitcher, type OrgInvitation, type OrgMembership } from "../src/components/OrgSwitcher.tsx";
import { EmailChipsInput } from "../src/components/EmailChipsInput.tsx";
import { InvitationLinkList, invitationLinesText, type InvitationLink } from "../src/components/InvitationLinkList.tsx";
import { KeyShownOnce } from "../src/components/KeyShownOnce.tsx";
import { FrontDoor, FrontDoorLink, FrontDoorMessage, HowList } from "../src/components/FrontDoor.tsx";
import { emailChipSummary, emailChipViews, parseEmailEntries, type EmailChipView, type EmailEntry } from "../src/util/emails.ts";
import { TooltipProvider } from "../src/primitives/Tooltip.tsx";
import { accessibleName, allByRole, byRole } from "./queries.ts";

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
  await act(async () => root!.render(<TooltipProvider>{el}</TooltipProvider>));
  return host;
}

const ACME = { id: "org_acme", name: "Acme", slug: "acme" };
const NORTHWIND = { id: "org_nw", name: "Northwind", slug: "northwind" };
const GLOBEX = { id: "org_gx", name: "Globex", slug: "globex" };
const YOU = { id: "per_m", name: "Márcio Martins" };
const MEMBERSHIPS: OrgMembership[] = [{ org: ACME, role: "admin", waiting: 2 }, { org: NORTHWIND, role: "member", waiting: 0 }];
const INVITE: OrgInvitation = { code: "Gx4t", org: GLOBEX, inviter: "Lin Zhao", expires: "in 9 days" };

function open(trigger: Element) {
  return act(async () => void trigger.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
}
const menu = () => document.querySelector("[role=menu]");
/** What a screen reader says for `el`: its text, leaving out what is aria-hidden, words spaced. */
function spoken(el: Element): string {
  const parts: string[] = [];
  const walk = (n: Node) => {
    if (n instanceof Element && n.getAttribute("aria-hidden") === "true") return;
    if (n instanceof Element && n.getAttribute("aria-label") && n.getAttribute("role") === "img") return void parts.push(n.getAttribute("aria-label")!);
    if (n.nodeType === 3) parts.push(n.textContent ?? "");
    else if (n instanceof Element && n.children.length === 0 && n.childNodes.length === 1) parts.push(` ${n.textContent} `);
    else n.childNodes.forEach(walk);
  };
  walk(el);
  return parts.join("").replace(/\s+/g, " ").trim();
}
const radioFor = (name: string) => allByRole(document, "menuitemradio").find((r) => spoken(r).startsWith(name))!;

describe("OrgFace", () => {
  test("is the organisation's initials on a rounded square, named by the organisation", async () => {
    const el = await mount(<OrgFace org={NORTHWIND} size={32} />);
    const face = byRole(el, "img", "Northwind");
    expect(face.textContent).toBe("NO");
    // The same organisation is the same colour everywhere: its id picks it.
    const again = await mount(<OrgFace org={{ ...NORTHWIND, name: "Renamed" }} />);
    expect(again.querySelector("[data-identity]")!.getAttribute("data-identity")).toBe(face.getAttribute("data-identity"));
  });
});

describe("OrgSwitcher", () => {
  const calls: string[] = [];
  const props = {
    you: YOU, current: ACME, memberships: MEMBERSHIPS, invitations: [INVITE], superAdmin: true,
    onSwitch: (o: { id: string }) => calls.push(`switch ${o.id}`), onJoin: (i: OrgInvitation) => calls.push(`join ${i.code}`),
    onSettings: () => calls.push("settings"), onAdmin: () => calls.push("admin"), onSignOut: () => calls.push("signout"),
    signOutNote: "Of Cloudflare Access, on this browser",
  };
  afterEach(() => void calls.splice(0));

  test("the band's trigger names you, the organisation and your role, and what it opens; a dot says an invitation waits", async () => {
    const el = await mount(<OrgSwitcher {...props} />);
    const trigger = byRole(el, "button", "Márcio Martins, Acme, admin: switch organisation, your settings, sign out");
    expect(trigger.textContent).toContain("Márcio Martins");
    expect(trigger.textContent).toContain("Acme · Admin");
    expect(byRole(el, "img", "1 invitation waiting")).toBeTruthy();
    expect(trigger.getAttribute("aria-haspopup")).toBe("menu");
  });

  test("the menu: your organisations with role and waiting, the current ticked; Join…; settings; All organisations; Sign out", async () => {
    const el = await mount(<OrgSwitcher {...props} adminNote="Super-admin: this dude's organisations" />);
    await open(el.querySelector("[data-testid=switcher]")!);
    const radios = allByRole(document, "menuitemradio");
    expect(radios.map((r) => [spoken(r), r.getAttribute("aria-checked")])).toEqual([
      ["Acme Admin · 2 waiting on you", "true"],
      ["Northwind Member", "false"],
    ]);
    // The waiting count is also the needs-you pill, which the description already says aloud.
    expect(radios[0]!.querySelector("[aria-label='2 need you']")).not.toBeNull();
    expect(allByRole(document, "menuitem").map(spoken)).toEqual([
      "Join Globex… Invited by Lin Zhao · expires in 9 days", "Your settings", "All organisations Super-admin: this dude's organisations",
      "Sign out Of Cloudflare Access, on this browser",
    ]);
  });

  test("the product's words for settings and the super-admin item come from props; without them, neutral ones and no note", async () => {
    const el = await mount(<OrgSwitcher {...props} settingsLabel="Preferences" adminLabel="Every workspace" />);
    await open(el.querySelector("[data-testid=switcher]")!);
    expect(allByRole(document, "menuitem").map(spoken).slice(1, 3)).toEqual(["Preferences", "Every workspace"]);
    await act(async () => root?.unmount());
    const plain = await mount(<OrgSwitcher {...props} />);
    await open(plain.querySelector("[data-testid=switcher]")!);
    expect(allByRole(document, "menuitem").map(spoken).slice(1, 3)).toEqual(["Your settings", "All organisations"]);
  });

  test("picking another organisation switches to it; picking the current one does nothing", async () => {
    const el = await mount(<OrgSwitcher {...props} />);
    await open(el.querySelector("[data-testid=switcher]")!);
    await act(async () => radioFor("Acme").click());
    expect(calls).toEqual([]);
    await act(async () => root?.unmount());
    const again = await mount(<OrgSwitcher {...props} />);
    await open(again.querySelector("[data-testid=switcher]")!);
    await act(async () => radioFor("Northwind").click());
    expect(calls).toEqual(["switch org_nw"]);
  });

  test("Join…, Your settings, All organisations and Sign out each do theirs", async () => {
    for (const [id, want] of [["join-Gx4t", "join Gx4t"], ["settings", "settings"], ["admin", "admin"], ["signout", "signout"]] as const) {
      const el = await mount(<OrgSwitcher {...props} />);
      await open(el.querySelector("[data-testid=switcher]")!);
      await act(async () => (document.querySelector(`[data-testid=rowmenu-${id}]`) as HTMLElement).click());
      expect(calls.pop()).toBe(want);
      await act(async () => root?.unmount());
      host?.remove();
    }
  });

  test("not a super-admin: no All organisations; no invitations: no Join… and no dot", async () => {
    const el = await mount(<OrgSwitcher {...props} superAdmin={false} invitations={[]} />);
    expect(el.querySelector("[role=img][aria-label*=invitation]") === null).toBe(true);
    await open(el.querySelector("[data-testid=switcher]")!);
    expect(menu()!.textContent).not.toContain("All organisations");
    expect(menu()!.textContent).not.toContain("Join");
  });

  test("the keyboard: Enter on the trigger opens the menu, Escape closes it", async () => {
    const el = await mount(<OrgSwitcher {...props} />);
    const trigger = el.querySelector<HTMLElement>("[data-testid=switcher]")!;
    trigger.focus();
    await act(async () => void trigger.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
    expect(menu()).not.toBeNull();
    await act(async () => void menu()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(menu() === null).toBe(true);
  });

  test("on the rail: your face with the organisation's in its corner, named the same", async () => {
    const el = await mount(<OrgSwitcher {...props} variant="rail" />);
    const trigger = byRole(el, "button", "Márcio Martins, Acme, admin: switch organisation, your settings, sign out");
    expect(trigger.querySelector("[data-org-face=org_acme]")).not.toBeNull();
    await open(trigger);
    expect(allByRole(document, "menuitemradio").length).toBe(2);
  });
});

describe("parseEmailEntries", () => {
  test("Name <email>, quoted names, bare addresses, commas and semicolons, lower-cased", () => {
    const got = parseEmailEntries(`Priya Raman <Priya@Acme.dev>; "Jules Moreau" <jules@acme.dev>, lin@acme.dev`);
    expect(got.map(({ email, name }) => ({ email, name }))).toEqual([
      { email: "priya@acme.dev", name: "Priya Raman" }, { email: "jules@acme.dev", name: "Jules Moreau" }, { email: "lin@acme.dev", name: undefined },
    ]);
  });

  test("a spreadsheet row: the cell with @ is the address, the first other cell its name", () => {
    const got = parseEmailEntries("Noah Berg\tnoah.berg@acme.dev\tEngineering\nana@acme.dev\tAna");
    expect(got.map(({ email, name }) => ({ email, name }))).toEqual([
      { email: "noah.berg@acme.dev", name: "Noah Berg" }, { email: "ana@acme.dev", name: "Ana" },
    ]);
  });

  test("what is not an email keeps its words and no address", () => {
    const [bad] = parseEmailEntries("tomás@");
    expect(bad!.raw).toBe("tomás@");
    expect(bad!.email).toBeUndefined();
  });
});

describe("emailChipViews and the summary", () => {
  test("the first of an address wins, later ones are duplicates; the caller classifies the rest", () => {
    const entries = parseEmailEntries("a@x.io, b@x.io, nope, a@x.io, c@x.io, d@x.io");
    const views = emailChipViews(entries, (email) => {
      switch (email) {
        case "b@x.io": return { kind: "member", note: "already in Acme" };
        case "c@x.io": return { kind: "invited", note: "already invited 4 days ago" };
        case "d@x.io": return { kind: "renew" };
        default: return null;
      }
    });
    expect(views.map((v) => v.kind)).toEqual(["valid", "member", "invalid", "duplicate", "invited", "renew"]);
    expect(emailChipSummary(views, { member: "already in Acme" }))
      .toBe("1 new · 1 renewed · 1 not an email · 1 already in Acme · 1 already invited · 1 duplicate. The other 4 are left out.");
    expect(emailChipSummary([])).toBe("");
    expect(emailChipSummary([{ kind: "valid" }])).toBe("1 new.");
  });
});

function Chips({ classify, onAction }: { classify?: (e: string) => EmailChipView | null; onAction?: (e: string) => void }) {
  const [entries, setEntries] = useState<EmailEntry[]>([]);
  const views = emailChipViews(entries, classify ?? ((e) => (e.startsWith("lin@") ? { kind: "invited", note: "already invited today",
    action: { label: "Copy their link", onClick: () => onAction?.(e) } } : null)));
  return <EmailChipsInput entries={entries} onChange={setEntries} views={views} summaryWords={{ member: "already in Acme" }} />;
}

async function type(input: HTMLInputElement, text: string) {
  const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  await act(async () => {
    setter.call(input, text);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const key = (input: HTMLElement, k: string) => act(async () => void input.dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true, cancelable: true })));
const chips = (el: HTMLElement) => [...el.querySelectorAll("li[data-state]")].map((c) => `${c.getAttribute("data-state")}:${c.getAttribute("data-email") ?? c.getAttribute("title")}`);

describe("EmailChipsInput", () => {
  test("the field is named Emails; Enter, comma and space make chips of what is typed", async () => {
    const el = await mount(<Chips />);
    const input = byRole(el, "textbox", "Emails") as HTMLInputElement;
    for (const [entry, delimiter] of [["a@x.io", "Enter"], ["Bo <b@x.io>", ","], ["c@x.io", " "]] as const) {
      await type(input, entry);
      await key(input, delimiter);
    }
    expect(chips(el)).toEqual(["valid:a@x.io", "valid:b@x.io", "valid:c@x.io"]);
    expect(el.textContent).toContain("Bo");
    expect(input.value).toBe("");
  });

  test("a paste becomes chips, each saying what will happen; the summary counts them", async () => {
    const el = await mount(<Chips />);
    const input = byRole(el, "textbox", "Emails");
    const paste = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
    paste.clipboardData = { getData: () => "Priya <priya@x.io>\nNoah\tnoah@x.io\nlin@x.io\ntomás@\npriya@x.io" };
    await act(async () => void input.dispatchEvent(paste));
    expect(chips(el)).toEqual(["valid:priya@x.io", "valid:noah@x.io", "invited:lin@x.io", "invalid:tomás@", "duplicate:priya@x.io"]);
    // The words say it too, not only the colour: a screen reader hears "not an email, left out".
    expect(spoken(el.querySelector("li[data-state=invalid]")!).replace(/ ,/g, ",")).toBe("tomás@ not an email, left out");
    expect(el.querySelector("[data-testid=chip-summary]")!.textContent)
      .toBe("2 new · 1 not an email · 1 already invited · 1 duplicate. The other 3 are left out.");
    // The summary describes the field to a screen reader.
    expect(input.getAttribute("aria-describedby")).toContain(el.querySelector("[data-testid=chip-summary]")!.id);
  });

  test("an already-invited chip offers Copy their link", async () => {
    const copied: string[] = [];
    const el = await mount(<Chips onAction={(e) => copied.push(e)} />);
    const input = byRole(el, "textbox", "Emails") as HTMLInputElement;
    await type(input, "lin@x.io");
    await key(input, "Enter");
    await act(async () => byRole(el, "button", "Copy their link").click());
    expect(copied).toEqual(["lin@x.io"]);
  });

  test("Backspace in the empty field takes the last chip back; ✕ removes one", async () => {
    const el = await mount(<Chips />);
    const input = byRole(el, "textbox", "Emails") as HTMLInputElement;
    await type(input, "a@x.io, b@x.io, c@x.io");
    await key(input, "Enter");
    expect(chips(el).length).toBe(3);
    await key(input, "Backspace");
    expect(chips(el)).toEqual(["valid:a@x.io", "valid:b@x.io"]);
    await act(async () => byRole(el, "button", "Remove a@x.io").click());
    expect(chips(el)).toEqual(["valid:b@x.io"]);
  });

  test("leaving the field makes chips of what is typed", async () => {
    const el = await mount(<Chips />);
    const input = byRole(el, "textbox", "Emails") as HTMLInputElement;
    await type(input, "z@x.io");
    await act(async () => void input.dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
    expect(chips(el)).toEqual(["valid:z@x.io"]);
  });
});

describe("InvitationLinkList", () => {
  const links: InvitationLink[] = [
    { email: "priya@x.io", name: "Priya Raman", role: "Member", url: "https://d/#/invite/A" },
    { email: "kai@x.io", role: "Member", url: "https://d/#/invite/B", renewed: true },
  ];

  test("each invitee, the link in mono, and Copy named for them", async () => {
    const copied: string[] = [];
    const el = await mount(<InvitationLinkList links={links} onCopy={(l) => copied.push(l.url)} />);
    const list = byRole(el, "list", "Invitation links");
    expect(list.querySelectorAll("li").length).toBe(2);
    expect(list.textContent).toContain("Priya Raman");
    expect(list.textContent).toContain("renewed, same link");
    expect([...el.querySelectorAll("code")].map((c) => c.textContent)).toEqual(["https://d/#/invite/A", "https://d/#/invite/B"]);
    await act(async () => byRole(el, "button", "Copy kai@x.io's link").click());
    expect(copied).toEqual(["https://d/#/invite/B"]);
  });

  test("Copy all's text is one line per person: name, email, link", () => {
    expect(invitationLinesText(links)).toBe("Priya Raman, priya@x.io, https://d/#/invite/A\nkai@x.io, https://d/#/invite/B");
  });
});

describe("KeyShownOnce", () => {
  test("the key, Copy that says Copied, and the usage lines under How to use it", async () => {
    const written: string[] = [];
    await mount(<KeyShownOnce title="Key “Laptop” for Acme" secret="dude_sk_x" onClose={() => undefined}
      usage={"curl https://dude.acme.dev/v1/me"} copy={async (s) => void written.push(s)} />);
    const dialog = byRole(document, "dialog", "Key “Laptop” for Acme");
    expect((byRole(dialog, "textbox", "Key") as HTMLInputElement).value).toBe("dude_sk_x");
    expect(dialog.querySelector("[data-testid=key-usage]")!.textContent).toBe("curl https://dude.acme.dev/v1/me");
    expect(dialog.querySelector(".ds-label")!.textContent).toBe("How to use it");
    await act(async () => byRole(dialog, "button", "Copy").click());
    expect(written).toEqual(["dude_sk_x"]);
    expect(byRole(dialog, "button", "Copied")).toBeTruthy();
  });

  test("the usage heading is the caller's when given", async () => {
    await mount(<KeyShownOnce title="Key" secret="s" onClose={() => undefined} usage="x" usageHeading="Send it as a bearer token" />);
    expect(byRole(document, "dialog", "Key").querySelector(".ds-label")!.textContent).toBe("Send it as a bearer token");
  });
});

describe("FrontDoor", () => {
  test("the heading is the page's h1, the line under it, then the cards, then the fine print with its link", async () => {
    const out: string[] = [];
    const el = await mount(<FrontDoor title="Your email isn't in any organisation on this dude" line="dude lets people in by invitation."
      footer={<FrontDoorLink onClick={() => out.push("signout")}>Sign out</FrontDoorLink>}><p>card</p></FrontDoor>);
    expect(byRole(el, "heading", "Your email isn't in any organisation on this dude").tagName).toBe("H1");
    expect(el.textContent).toBe("Your email isn't in any organisation on this dudedude lets people in by invitation.cardSign out");
    await act(async () => byRole(el, "button", "Sign out").click());
    expect(out).toEqual(["signout"]);
  });

  test("a message: a glyph on a disc, or a face; its heading and its facts", async () => {
    const el = await mount(<FrontDoorMessage icon="link" title="This invitation can't be used" line="Ask whoever sent it for a new link." facts={<dl><dt>For</dt><dd>you</dd></dl>} />);
    expect(byRole(el, "heading", "This invitation can't be used")).toBeTruthy();
    expect(el.textContent).toBe("This invitation can't be usedAsk whoever sent it for a new link.Foryou");
  });

  test("HowList: a named list, each a glyph, a bold lead-in and a sentence", async () => {
    const el = await mount(<HowList label="How dude works" items={[{ icon: "git-pr", title: "Deliver", body: "It opens a pull request." }]} />);
    expect(byRole(el, "list", "How dude works").textContent).toBe("Deliver. It opens a pull request.");
  });
});
