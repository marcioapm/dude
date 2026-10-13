/**
 * Accounts in the web app, mounted from the address as main.tsx mounts
 * it: the gatekeeper's first screen for each answer of the sign-in gate,
 * the organisation every tenant request and the stream name, switching,
 * older links, inviting with the chip editor, the Invitations page, every
 * state of an invitation link, keys per organisation, and the
 * super-admin's pages.
 */

import { afterEach, beforeEach, describe, expect, test } from "bun:test";
import { useState } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import { ThemeProvider } from "@dude/design-system";
import { act, click, mount, settle, type, until } from "./dom.ts";
import { ApiClient, ApiError, type MeAnswer } from "../src/api/client.ts";
import { App } from "../src/App.tsx";
import { Gatekeeper } from "../src/accounts/Gatekeeper.tsx";
import { PeopleProvider } from "../src/people.tsx";
import { setPlaceOrganization } from "../src/place.ts";
import { LAST_ORGANIZATION } from "../src/accounts/route.ts";
import { ACME, AccountsClient, GLOBEX, GLOBEX_INVITE, NORTHWIND, invitationView, memberMe, world, type World } from "./accountsFake.ts";
import { ORG, PEOPLE, TASK_ID } from "../src/fixtures/data.ts";

let mounted: Array<() => Promise<void>> = [];
let copied: string[] = [];
let signedOut = 0;
beforeEach(() => {
  copied = [];
  signedOut = 0;
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (t: string) => void copied.push(t) } });
});
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  window.history.replaceState(null, "", " ");
  localStorage.clear();
  sessionStorage.clear();
  setPlaceOrganization(null);
  // ThemeProvider stamps the root; later suites in this process read the root as unthemed.
  for (const a of ["data-theme", "data-density", "data-reduced-motion"]) document.documentElement.removeAttribute(a);
});

/** The signed-in app at `hash`, as main.tsx mounts it: the gatekeeper, then the tenant app it chose. */
async function open(hash: string, w: World, authMethod: "api_key" | "cloudflare_access" = "cloudflare_access") {
  window.history.replaceState(null, "", hash);
  const client = new AccountsClient(w);
  function Root() {
    const [me, setMe] = useState(w.me);
    const reread = async () => {
      const next = await client.gate();
      setMe(next);
      return next;
    };
    return (
      <ThemeProvider storageKey={null} densityStorageKey={null}>
      <TooltipProvider>
        <ToastProvider>
          <Gatekeeper client={client} me={me} authMethod={authMethod} reread={reread} onSignOut={() => void signedOut++}>
            {(scoped) => (
              <PeopleProvider client={scoped}>
                <App client={scoped} onKeyRefused={() => {}} onSignOut={() => void signedOut++} />
              </PeopleProvider>
            )}
          </Gatekeeper>
        </ToastProvider>
      </TooltipProvider>
      </ThemeProvider>
    );
  }
  const { container, unmount } = await mount(<Root />);
  mounted.push(unmount);
  return { page: container, client };
}

const byTestId = (root: ParentNode, id: string) => root.querySelector<HTMLElement>(`[data-testid="${id}"]`);
const text = (el: Element | null) => (el?.textContent ?? "").replace(/\s+/g, " ").trim();
const button = (root: ParentNode, name: string) => [...root.querySelectorAll<HTMLButtonElement>("button")].find((b) => text(b) === name || b.getAttribute("aria-label") === name);
async function pointer(el: Element) {
  await act(async () => void el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, button: 0, pointerType: "mouse" })));
}

describe("the gate decides the first screen before any organisation mounts", () => {
  test("a member at the bare address opens their last organisation's home, and the shell reads it naming that organisation", async () => {
    const w = world();
    localStorage.setItem(LAST_ORGANIZATION, "northwind");
    const { page } = await open("#/", w);
    await until(() => byTestId(page, "shell"), "the shell");
    expect(window.location.hash).toBe("#/o/northwind/");
    await until(() => w.calls.some((c) => c.method === "navigation") ? true : null, "the tree read");
    // Nothing tenant is asked without an organisation.
    expect(w.calls.filter((c) => ["navigation", "listPeople"].includes(c.method)).every((c) => c.org === NORTHWIND.id)).toBe(true);
  });

  test("invited: You're invited, one card per invitation, and nothing of any organisation's work is read", async () => {
    const two = [
      { ...GLOBEX_INVITE, code: "NW", organization: { id: NORTHWIND.id, name: "Northwind", slug: "northwind" }, inviter: "Gus Lindqvist" },
      GLOBEX_INVITE,
    ];
    const w = world({ gate: "invited", email: "chloe.durand@hey.com", name: "Chloé Durand", photoUrl: null, superAdmin: false, invitations: two });
    const { page } = await open("#/", w);
    const invited = await until(() => byTestId(page, "invited"), "You're invited");
    expect(text(invited.querySelector("h1"))).toBe("You're invited, Chloé");
    expect([...page.querySelectorAll("[data-testid=invitation-card]")].map((c) => c.getAttribute("data-org"))).toEqual(["northwind", "globex"]);
    expect(text(invited)).toContain("Two organisations on this dude sent you an invitation link.");
    expect(w.calls.some((c) => c.org !== null)).toBe(false);
  });

  test("invited with one: the same page with one card; Join sends the edited name, then opens the organisation's welcome", async () => {
    const w = world({ gate: "invited", email: "chloe.durand@hey.com", name: "Chloé Durand", photoUrl: null, superAdmin: false,
      invitations: [{ ...GLOBEX_INVITE, code: "NW", organization: { id: NORTHWIND.id, name: "Northwind", slug: "northwind" }, inviter: "Gus Lindqvist" }] });
    w.codes.set("NW", invitationView("NW", { id: NORTHWIND.id, name: "Northwind", slug: "northwind" }));
    w.onAccept = () => { w.me = memberMe({ organizations: [{ ...NORTHWIND, role: "member" }], invitations: [], superAdmin: false }); };
    const { page } = await open("#/", w);
    await until(() => byTestId(page, "invited"), "You're invited");
    expect(page.querySelectorAll("[data-testid=invitation-card]").length).toBe(1);
    await type(byTestId(page, "invited-name")!, "Chloé D.");
    await click(byTestId(page, "join-northwind")!);
    await until(() => (window.location.hash === "#/o/northwind/welcome" ? true : null), `the welcome's address, not ${window.location.hash}`);
    expect(w.calls.find((c) => c.method === "acceptInvitation")!.args).toEqual(["NW", "Chloé D."]);
    const welcome = await until(() => byTestId(page, "first-one"), "Welcome to Northwind");
    expect(text(welcome.querySelector("h1"))).toBe("Welcome to Northwind, Márcio");
    expect(text(welcome)).toContain("You joined from Gus Lindqvist's invitation link, as a member.");
  });

  test("super-admin with no membership: this dude's organisations, at #/admin", async () => {
    const w = world({ gate: "super_admin", email: "op@example.com", name: null, photoUrl: null, superAdmin: true });
    const { page } = await open("#/", w);
    const admin = await until(() => byTestId(page, "admin"), "the super-admin's page");
    expect(window.location.hash).toBe("#/admin");
    await until(() => page.querySelectorAll("[data-testid=admin-org]").length === 2 ? true : null, "the organisations");
    expect(text(admin)).toContain("Outside every organisation");
    expect(byTestId(page, "admin-back")).toBeNull();
  });

  test("none: No organisation shows their own address and nothing about this deployment; Check again asks again", async () => {
    const w = world({ gate: "none", email: "stranger@example.com", name: "Stranger", photoUrl: null });
    const { page } = await open("#/", w);
    const none = await until(() => byTestId(page, "no-org"), "No organisation");
    const words = text(none);
    expect(words).toContain("stranger@example.com");
    for (const secret of ["Acme", "Northwind", "Globex", "Márcio", "Ana Ribeiro", "Gus", "Lin Zhao", "operator", "super", "admin", "@acme", "expired"]) {
      expect(words).not.toContain(secret);
    }
    expect(words).toBe(
      "Your email isn't in any organisation on this dudeYou signed in as stranger@example.com. dude lets people in by invitation."
      + "If someone invited you, open the link they sent you. Links expire after 14 days; ask them for a new one."
      + "Signed in with the wrong address? Sign out and sign in with the one you were invited with.Check againSign out"
      + "dude keeps no record of this visit. Cloudflare Access decides who reaches this page; dude decides who gets past it.");
    const asked = w.calls.filter((c) => c.method === "gate").length;
    await click(byTestId(page, "check-again")!);
    await settle();
    expect(w.calls.filter((c) => c.method === "gate").length).toBe(asked + 1);
    await click(byTestId(page, "no-org-sign-out")!);
    expect(signedOut).toBe(1);
    expect(w.calls.every((c) => c.org === null)).toBe(true);
  });
});

describe("every tenant request names its organisation", () => {
  const original = globalThis.fetch;
  afterEach(() => { globalThis.fetch = original; });

  test("X-Dude-Organization on tenant calls; none from the client naming no organisation", async () => {
    const seen: Array<string | null> = [];
    globalThis.fetch = (async (_input: string | URL | Request, init?: RequestInit) => {
      seen.push(new Headers(init?.headers).get("x-dude-organization"));
      return Response.json({ projects: [] });
    }) as typeof fetch;
    const base = new ApiClient({});
    await base.navigation();
    const scoped = base.inOrganization("org_northwind");
    await scoped.navigation();
    await scoped.listMyKeys();
    expect(seen).toEqual([null, "org_northwind", "org_northwind"]);
    expect(scoped.root).toBe(base);
  });

  test("org= on the event stream, beside the key", () => {
    const url = new URL(new ApiClient({ apiKey: "k" }).inOrganization("org_acme").streamUrl({ taskId: "t" }), "http://x");
    expect(url.searchParams.get("org")).toBe("org_acme");
    expect(url.searchParams.get("key")).toBe("k");
    expect(new URL(new ApiClient({}).streamUrl({}), "http://x").searchParams.has("org")).toBe(false);
  });

  test("key creation sends the organisation it is for", async () => {
    let body: unknown;
    globalThis.fetch = (async (_i: string | URL | Request, init?: RequestInit) => {
      body = JSON.parse(String(init?.body));
      return Response.json({ id: "k", name: "Laptop", prefix: "p", key: "s" });
    }) as typeof fetch;
    await new ApiClient({}).createMyKey("Laptop", "org_northwind");
    expect(body).toEqual({ name: "Laptop", organizationId: "org_northwind" });
    await new ApiClient({}).inOrganization("org_acme").createMyKey("CI");
    expect(body).toEqual({ name: "CI", organizationId: "org_acme" });
  });
});

describe("switching organisation", () => {
  test("the profile band's menu lists them with role and waiting; picking Northwind goes to its home, and what is read next names it", async () => {
    const w = world();
    const { page } = await open("#/o/acme/", w);
    const band = await until(() => byTestId(page, "switcher"), "the switcher");
    expect(band.getAttribute("aria-label")).toBe("Márcio Martins, Acme, admin: switch organisation, your settings, sign out");
    await pointer(band);
    const menu = document.querySelector("[role=menu]")!;
    expect(text(menu)).toContain("Admin · 2 waiting on you");
    expect(text(menu)).toContain("Member · 1 waiting on you");
    expect(text(menu)).toContain("Join Globex…");
    expect(text(menu)).toContain("All organisations");
    expect(document.querySelector(`[data-testid=rowmenu-${ACME.id}]`)!.getAttribute("aria-checked")).toBe("true");
    const before = w.calls.length;
    await click(document.querySelector("[data-testid=rowmenu-org_northwind]")!);
    await until(() => (window.location.hash === "#/o/northwind/" ? true : null), "Northwind's home");
    await until(() => w.calls.slice(before).some((c) => c.method === "navigation") ? true : null, "Northwind's tree");
    expect(w.calls.slice(before).filter((c) => c.method === "navigation").every((c) => c.org === NORTHWIND.id)).toBe(true);
    expect(localStorage.getItem(LAST_ORGANIZATION)).toBe("northwind");
    // Links written from here on are Northwind's.
    await until(() => byTestId(page, "org-settings-button"), "the band");
    await click(byTestId(page, "org-settings-button")!);
    expect(window.location.hash).toBe("#/o/northwind/org/settings");
  });

  test("Join… opens that invitation's page; All organisations the super-admin's", async () => {
    const w = world();
    const { page } = await open("#/o/acme/", w);
    await pointer(await until(() => byTestId(page, "switcher"), "the switcher"));
    await click(document.querySelector(`[data-testid=rowmenu-join-${GLOBEX_INVITE.code}]`)!);
    expect(window.location.hash).toBe(`#/invite/${GLOBEX_INVITE.code}`);
    window.location.hash = "#/o/acme/";
    await pointer(await until(() => byTestId(page, "switcher"), "the switcher again"));
    await click(document.querySelector("[data-testid=rowmenu-admin]")!);
    await until(() => byTestId(page, "admin"), "the super-admin's page");
    expect(text(byTestId(page, "admin-back"))).toBe("Back to Acme");
  });

  test("the collapsed rail's face opens the same menu", async () => {
    const w = world();
    localStorage.setItem("dude.sidebar", "rail");
    const { page } = await open("#/o/acme/", w);
    const face = await until(() => byTestId(page, "rail-switcher"), "the rail's face");
    await pointer(face);
    expect(document.querySelectorAll("[role=menuitemradio]").length).toBe(2);
  });

  test("an address naming an organisation you are not in says so, and offers yours", async () => {
    const { page } = await open("#/o/initech/", world());
    const notYours = await until(() => byTestId(page, "not-yours"), "not yours");
    expect(text(notYours)).not.toContain("initech");
  });
});

describe("links from before addresses named an organisation", () => {
  test("a task's old link is located among your organisations and rewritten into its organisation's address", async () => {
    const w = world();
    w.located.set(TASK_ID, { organization: { id: NORTHWIND.id, name: "Northwind", slug: "northwind" }, kind: "task", id: TASK_ID });
    const { page } = await open(`#/task/${TASK_ID}/servers`, w);
    await until(() => (window.location.hash === `#/o/northwind/task/${TASK_ID}/servers` ? true : null), "the rewritten address");
    expect(w.calls.find((c) => c.method === "locate")?.args).toEqual([TASK_ID]);
    await until(() => byTestId(page, "shell"), "the shell");
  });

  test("with one organisation there is nothing to ask: the link opens in it", async () => {
    const w = world(memberMe({ organizations: [ACME], invitations: [] }));
    await open(`#/task/${TASK_ID}`, w);
    await until(() => (window.location.hash === `#/o/acme/task/${TASK_ID}` ? true : null), "the rewritten address");
    expect(w.calls.some((c) => c.method === "locate")).toBe(false);
  });
});

/** Members, as an admin of Acme, with the invite dialog open. */
async function inviteDialog(w: World) {
  const { page } = await open("#/o/acme/org/settings/members", w);
  await click(await until(() => byTestId(page, "invite"), "Invite people"));
  const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the invite dialog");
  // Its pending and expired invitations are read for the chips.
  await until(() => w.calls.filter((c) => c.method === "listInvitations" && ["pending", "expired"].includes((c.args[0] as { status: string }).status)).length >= 2 ? true : null, "the live invitations");
  await settle();
  return { page, dialog };
}

async function paste(input: HTMLElement, words: string) {
  const e = new Event("paste", { bubbles: true, cancelable: true }) as Event & { clipboardData: unknown };
  e.clipboardData = { getData: () => words };
  await act(async () => void input.dispatchEvent(e));
}

describe("inviting with the chip editor", () => {
  test("Members has no interim guidance line; its header says how people join", async () => {
    const { page } = await open("#/o/acme/org/settings/members", world());
    const members = await until(() => byTestId(page, "members"), "Members");
    expect(text(members)).toContain("People join Acme by invitation. Cloudflare Access must also let them in; dude doesn't manage that.");
    expect(text(members)).not.toContain("not available yet");
  });

  test("every chip state from one paste: Name <email>, a spreadsheet row, a member, already invited, expired, invalid, a duplicate", async () => {
    const w = world();
    const { dialog } = await inviteDialog(w);
    const input = dialog.querySelector<HTMLInputElement>("input#" + CSS.escape(dialog.querySelector("label[for]")!.getAttribute("for")!))!;
    await paste(input, `Priya Raman <Priya@acme.dev>\nNoah Berg\tnoah.berg@acme.dev\nmarcio@example.com\njules@acme.dev\nkai.nakamura@acme.dev\ntomás@\npriya@acme.dev`);
    const chips = [...dialog.querySelectorAll("li[data-state]")].map((c) => [c.getAttribute("data-state"), c.getAttribute("data-email") ?? c.getAttribute("title")]);
    expect(chips).toEqual([["valid", "priya@acme.dev"], ["valid", "noah.berg@acme.dev"], ["member", "marcio@example.com"], ["invited", "jules@acme.dev"],
      ["renew", "kai.nakamura@acme.dev"], ["invalid", "tomás@"], ["duplicate", "priya@acme.dev"]]);
    expect(text(dialog.querySelector("li[data-state=member]"))).toContain("already in Acme");
    expect(text(dialog.querySelector("li[data-state=invited]"))).toContain("already invited today");
    expect(text(dialog.querySelector("li[data-state=renew]"))).toContain("invitation expired 2 days ago: Invite renews it");
    expect(text(byTestId(dialog, "chip-summary"))).toBe("2 new · 1 renewed · 1 not an email · 1 already in Acme · 1 already invited · 1 duplicate. The other 4 are left out.");
    expect(text(byTestId(dialog, "invite-submit"))).toBe("Invite 3 people");
    // Already invited: Copy their link copies that invitation's link.
    await click(button(dialog.querySelector("li[data-state=invited]")!, "Copy their link")!);
    await settle();
    expect(copied).toEqual([`${location.origin}/#/invite/Ju00000000000000000000`]);
  });

  test("Backspace takes the last chip back; ✕ removes one", async () => {
    const { dialog } = await inviteDialog(world());
    const input = dialog.querySelector<HTMLInputElement>("input[autocomplete=off]")!;
    await paste(input, "a@x.io, b@x.io, c@x.io");
    await act(async () => void input.dispatchEvent(new KeyboardEvent("keydown", { key: "Backspace", bubbles: true, cancelable: true })));
    expect([...dialog.querySelectorAll("li[data-email]")].map((c) => c.getAttribute("data-email"))).toEqual(["a@x.io", "b@x.io"]);
    await click(button(dialog, "Remove a@x.io")!);
    expect([...dialog.querySelectorAll("li[data-email]")].map((c) => c.getAttribute("data-email"))).toEqual(["b@x.io"]);
  });

  test("Invite sends only the new and the expired, with the role; the result is each person's link, Copy and Copy all", async () => {
    const w = world();
    const { dialog } = await inviteDialog(w);
    const input = dialog.querySelector<HTMLInputElement>("input[autocomplete=off]")!;
    await paste(input, "Priya Raman <priya@acme.dev>\nkai.nakamura@acme.dev\njules@acme.dev\nnope");
    await click(byTestId(dialog, "invite-submit")!);
    const links = await until(() => document.querySelector<HTMLElement>("[data-testid=invite-links]"), "the links");
    expect(w.calls.find((c) => c.method === "invite")!.args).toEqual([[{ email: "priya@acme.dev", name: "Priya Raman" }, { email: "kai.nakamura@acme.dev" }], "member"]);
    const result = document.querySelector("[role=dialog]")!;
    expect(text(result.querySelector("h2"))).toBe("2 invitations to Acme");
    expect([...links.querySelectorAll("code")].map((c) => c.textContent)).toEqual([
      `${location.origin}/#/invite/New0000000000000000000`, `${location.origin}/#/invite/Ka00000000000000000000`]);
    expect(text(links)).toContain("renewed, same link");
    await click(button(links, "Copy Priya Raman's link")!);
    await click(byTestId(result, "copy-all")!);
    await settle();
    expect(copied).toEqual([`${location.origin}/#/invite/New0000000000000000000`,
      `Priya Raman, priya@acme.dev, ${location.origin}/#/invite/New0000000000000000000\nkai.nakamura@acme.dev, ${location.origin}/#/invite/Ka00000000000000000000`]);
  });
});

describe("the Invitations page", () => {
  async function invitationsPage(w: World) {
    const { page } = await open("#/o/acme/org/settings/invitations", w);
    const list = await until(() => byTestId(page, "org-invitations"), "Invitations");
    await until(() => list.querySelectorAll("[data-testid=invitation-row]").length ? true : null, "its rows");
    return { page, list };
  }
  const rows = (list: HTMLElement) => [...list.querySelectorAll("[data-testid=invitation-row]")].map((r) => r.getAttribute("data-email"));

  test("is beside Members with the pending count; tabs count each status; pending first", async () => {
    const { page, list } = await invitationsPage(world());
    expect(text(page.querySelector("[data-settings-nav=invitations]"))).toBe("Invitations2 pending");
    await until(() => text(byTestId(list, "status-filter")).includes("All 5") ? true : null, "the counts");
    expect([...byTestId(list, "status-filter")!.querySelectorAll("button")].map(text)).toEqual(["Pending 2", "Expired 1", "Accepted 1", "Revoked 1", "All 5"]);
    expect(rows(list)).toEqual(["jo.becker@acme.dev", "jules@acme.dev"]);
  });

  test("filtering and search are the server's: each asks with its status and words", async () => {
    const w = world();
    const { list } = await invitationsPage(w);
    await click(button(byTestId(list, "status-filter")!, "All 5") ?? [...byTestId(list, "status-filter")!.querySelectorAll("button")].at(-1)!);
    await until(() => rows(list).length === 5 ? true : null, "every invitation");
    await type(byTestId(list, "invite-search")!.querySelector("input") ?? byTestId(list, "invite-search")!, "kai");
    await until(() => rows(list).length === 1 ? true : null, "the search");
    expect(rows(list)).toEqual(["kai.nakamura@acme.dev"]);
    expect(w.calls.filter((c) => c.method === "listInvitations").map((c) => c.args[0])).toContainEqual({ status: "all", q: "kai" });
  });

  test("Copy link, Revoke… (confirmed), Renew and Invite again", async () => {
    const w = world();
    const { list } = await invitationsPage(w);
    const jo = list.querySelector<HTMLElement>("[data-email='jo.becker@acme.dev']")!;
    await click(byTestId(jo, "copy-link")!);
    await settle();
    expect(copied).toEqual([`${location.origin}/#/invite/Jo00000000000000000000`]);
    await click(byTestId(jo, "revoke")!);
    const confirm = await until(() => document.querySelector<HTMLElement>("[role=alertdialog], [role=dialog]"), "the revoke dialog");
    expect(text(confirm)).toContain("The link stops working at once");
    await click(byTestId(confirm, "revoke-confirm")!);
    await until(() => w.calls.some((c) => c.method === "revokeInvitation") ? true : null, "revoked");
    expect(w.calls.find((c) => c.method === "revokeInvitation")!.args).toEqual(["inv_jo"]);

    await click([...byTestId(list, "status-filter")!.querySelectorAll("button")].find((b) => text(b).startsWith("Expired"))!);
    const kai = await until(() => list.querySelector<HTMLElement>("[data-email='kai.nakamura@acme.dev']"), "the expired one");
    await click(byTestId(kai, "renew")!);
    await until(() => w.calls.some((c) => c.method === "renewInvitation") ? true : null, "renewed");
    expect(w.calls.find((c) => c.method === "renewInvitation")!.args).toEqual(["inv_kai"]);

    await click([...byTestId(list, "status-filter")!.querySelectorAll("button")].find((b) => text(b).startsWith("Revoked"))!);
    const priya = await until(() => list.querySelector<HTMLElement>("[data-email='priya.raman@gmail.com']"), "the revoked one");
    await click(byTestId(priya, "invite-again")!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the invite dialog");
    expect([...dialog.querySelectorAll("li[data-email]")].map((c) => c.getAttribute("data-email"))).toEqual(["priya.raman@gmail.com"]);
  });
});

describe("an invitation link, every state", () => {
  const nw = { id: NORTHWIND.id, name: "Northwind", slug: "northwind" };
  const invited = (): MeAnswer => ({ gate: "invited", email: "chloe.durand@hey.com", name: "Chloé", photoUrl: null, superAdmin: false, invitations: [] });

  test("join: who invited, for you, the role and the expiry; Join accepts and opens the organisation's welcome", async () => {
    const w = world(memberMe({ organizations: [ACME], invitations: [] }));
    w.codes.set("C1", invitationView("C1", nw));
    w.onAccept = () => { w.me = memberMe({ organizations: [ACME, { ...NORTHWIND }], invitations: [] }); };
    const { page } = await open("#/invite/C1", w);
    const join = await until(() => byTestId(page, "accept-join"), "the join state");
    expect(text(join.querySelector("h1"))).toBe("Join Northwind as member");
    expect(text(join)).toContain("Invited by Gus Lindqvist on");
    expect(text(join)).toContain("chloe.durand@hey.com · you");
    expect(text(join)).not.toContain("You aren't in any organisation");
    await click(byTestId(page, "accept-join-button")!);
    await until(() => (window.location.hash === "#/o/northwind/welcome" ? true : null), "Northwind's welcome");
  });

  test("first sign-in: the note that Join makes the account; Not now goes back to the invitations", async () => {
    const w = world(invited());
    w.codes.set("C1", invitationView("C1", nw));
    const { page } = await open("#/invite/C1", w);
    const first = await until(() => byTestId(page, "accept-first"), "the first state");
    expect(text(first)).toContain("You aren't in any organisation on this dude yet. Join makes your account");
    await click(byTestId(page, "not-now")!);
    expect(window.location.hash).toBe("#/");
    expect(w.calls.some((c) => c.method === "acceptInvitation")).toBe(false);
  });

  test("another email: nothing about the invitation, a centred Accept invitation that signs out of Access, and accept is never called", async () => {
    const w = world({ gate: "none", email: "chloe.d@gmail.com", name: null, photoUrl: null });
    w.codes.set("C1", new ApiError(403, "invitation_for_another_email", "invitation_for_another_email"));
    const { page } = await open("#/invite/C1", w);
    const wrong = await until(() => byTestId(page, "accept-wrong"), "the different-email state");
    expect(text(wrong)).toBe("This invitation is for a different email address.or ask whoever sent it for a new link.Accept invitation"
      + "Signed in as chloe.d@gmail.com with Cloudflare Access. Sign out");
    expect(byTestId(page, "switch-account")!.closest(".cardFooterCentred")).not.toBeNull();
    await click(byTestId(page, "switch-account")!);
    expect(signedOut).toBe(1);
    expect(w.calls.some((c) => c.method === "acceptInvitation")).toBe(false);
  });

  test("already a member: Open Northwind, your role kept", async () => {
    const w = world(memberMe());
    w.codes.set("C1", invitationView("C1", nw, "member"));
    const { page } = await open("#/invite/C1", w);
    const member = await until(() => byTestId(page, "accept-member"), "the member state");
    expect(text(member.querySelector("h1"))).toBe("You're already in Northwind");
    expect(text(member)).toContain("Your role stays Member.");
    await click(byTestId(page, "open-org")!);
    await until(() => (window.location.hash === "#/o/northwind/" ? true : null), "Northwind");
  });

  test("can't be used: one generic page, whatever the reason", async () => {
    const w = world({ gate: "none", email: "x@example.com", name: null, photoUrl: null });
    const { page } = await open("#/invite/gone", w);
    const invalid = await until(() => byTestId(page, "accept-invalid"), "the unusable state");
    expect(text(invalid)).toBe("This invitation can't be usedAsk whoever sent it for a new link.Open dudeSigned in as x@example.com with Cloudflare Access. Sign out");
  });
});

describe("your settings: keys by organisation", () => {
  test("keys are grouped under each organisation; New key for Northwind makes it there and shows it once with the CLI line", async () => {
    const w = world();
    const { page } = await open("#/o/acme/me/settings", w);
    const keys = await until(() => byTestId(page, "keys"), "Keys");
    await until(() => keys.querySelectorAll("[data-testid=key-row]").length === 2 ? true : null, "the keys");
    const groups = [...keys.querySelectorAll("[data-testid=key-group]")];
    expect(groups.map((g) => [g.getAttribute("data-org"), g.querySelectorAll("[data-testid=key-row]").length])).toEqual([["acme", 2], ["northwind", 0]]);
    expect(text(groups[1]!)).toContain("No keys for Northwind yet.");
    // Read with the credential naming no organisation: every organisation's.
    expect(w.calls.find((c) => c.method === "listMyKeys")!.org).toBeNull();
    await click(groups[1]!.querySelector("[data-testid=key-new]")!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the new-key dialog");
    expect(text(dialog.querySelector("h2"))).toBe("New key for Northwind");
    await type(byTestId(dialog, "key-name")!, "Laptop");
    await click(byTestId(dialog, "key-create")!);
    const shown = await until(() => byTestId(document, "key-usage"), "the key, shown once");
    expect(w.calls.find((c) => c.method === "createMyKey")!.args).toEqual(["Laptop", NORTHWIND.id]);
    expect(shown.textContent).toBe(`dude login ${location.origin}\n# paste the key; the CLI keeps it as "northwind"\ndude whoami\n# Márcio Martins · Northwind · Member`);
    expect(text(document.querySelector("[role=dialog] h2"))).toBe("Key “Laptop” for Northwind");
  });

  test("the profile is the account's: the same in every organisation, the email from Cloudflare Access", async () => {
    const { page } = await open("#/o/acme/me/settings", world());
    const profile = await until(() => byTestId(page, "profile"), "the profile");
    expect(text(byTestId(profile, "profile-scope"))).toBe("The same in every organisation you're in: Acme and Northwind see this name and photo.");
    expect(text(byTestId(profile, "profile-facts"))).toContain("from Cloudflare Access, can't be changed here");
  });
});

describe("the super-admin's pages", () => {
  test("Create organisation: name, slug from it, first admin's email, then that admin's link", async () => {
    const w = world();
    const { page } = await open("#/admin", w);
    await click(await until(() => byTestId(page, "create-org"), "Create organisation"));
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "the create dialog");
    await type(byTestId(dialog, "create-name")!, "Initech Labs");
    expect((byTestId(dialog, "create-slug") as HTMLInputElement).value).toBe("initech-labs");
    await type(byTestId(dialog, "create-email")!, "Bill@Initech.com");
    await click(byTestId(dialog, "create-submit")!);
    const link = await until(() => document.querySelector("[data-testid=create-link]"), "the first admin's link");
    expect(w.calls.find((c) => c.method === "createOrganization")!.args).toEqual([{ name: "Initech Labs", slug: "initech-labs", email: "Bill@Initech.com" }]);
    expect(text(document.querySelector("[role=dialog] h2"))).toBe("Initech Labs is made");
    expect(link.querySelector("code")!.textContent).toBe(`${location.origin}/#/invite/First00000000000000000`);
  });

  test("one organisation: members with Set email… for a key-only person, its invitations, and the last-admin refusal shown", async () => {
    const w = world();
    w.orgPeople.set(NORTHWIND.id, [
      { id: "per_gus", name: "Gus Lindqvist", photoUrl: null, online: false, email: "gus@northwind.io", role: "admin", accountId: "acc_gus" },
      { id: "per_ops", name: "ops-bot", photoUrl: null, online: false, email: null, role: "member", accountId: null },
    ]);
    const { page } = await open("#/admin/o/northwind", w);
    const org = await until(() => byTestId(page, "admin-org-page"), "Northwind's page");
    await until(() => org.querySelectorAll("[data-testid=admin-member]").length === 2 ? true : null, "its members");
    const ops = org.querySelector<HTMLElement>("[data-member=ops-bot]")!;
    expect(text(ops)).toContain("no email: signs in only with a key");
    expect(org.querySelector("[data-member='Gus Lindqvist'] [data-testid=set-email]")).toBeNull();
    await click(byTestId(ops, "set-email")!);
    const dialog = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "Set email");
    await type(byTestId(dialog, "set-email-input")!, "ops@northwind.io");
    await click(byTestId(dialog, "set-email-submit")!);
    await until(() => w.calls.some((c) => c.method === "adminUpdatePerson") ? true : null, "set");
    expect(w.calls.find((c) => c.method === "adminUpdatePerson")!.args).toEqual(["per_ops", { email: "ops@northwind.io" }]);
    await until(() => document.querySelector("[role=dialog]") === null ? true : null, "closed");

    await click(byTestId(org.querySelector<HTMLElement>("[data-member='Gus Lindqvist']")!, "member-remove")!);
    const remove = await until(() => document.querySelector<HTMLElement>("[role=dialog]"), "Remove");
    await click(byTestId(remove, "member-remove-confirm")!);
    const problem = await until(() => byTestId(remove, "remove-problem"), "the refusal");
    expect(text(problem)).toBe("the organization needs an admin; make someone else one first");
    expect(byTestId(org, "invitations")).not.toBeNull();
  });

  test("a member who is no super-admin is sent home from #/admin", async () => {
    const w = world(memberMe({ superAdmin: false }));
    await open("#/admin", w);
    await until(() => (window.location.hash === "#/o/acme/" ? true : null), "home");
  });
});

describe("api_key mode", () => {
  test("the key's own organisation: the addresses stay bare, the switcher shows just it, and no organisation is asked of", async () => {
    const w = world({ person: { ...PEOPLE[0]!, githubLogin: null }, organization: { id: ORG.id, name: "Example", slug: "example" }, authMethod: "api_key", gate: "member" });
    const { page } = await open(`#/task/${TASK_ID}`, w, "api_key");
    await until(() => byTestId(page, "shell"), "the shell");
    expect(window.location.hash).toBe(`#/task/${TASK_ID}`);
    expect(w.calls.some((c) => c.method === "locate")).toBe(false);
    await pointer(await until(() => byTestId(page, "switcher"), "the switcher"));
    expect([...document.querySelectorAll("[role=menuitemradio]")].map(text)).toEqual(["EXExampleAdmin"]);
    expect(text(document.querySelector("[role=menu]"))).not.toContain("All organisations");
    expect(text(document.querySelector("[data-testid=rowmenu-signout]"))).toContain("Forget this browser's key");
  });
});

// GLOBEX is named for the reader: the menu's Join… is its invitation.
void GLOBEX;
