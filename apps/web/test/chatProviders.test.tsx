/**
 * Chat in the web app, against the fixture client in happy-dom: the Chat
 * page shows the fixed facts and who gets a session, and an admin saves the
 * organisation's instructions; a provider's page shows "What the model is
 * told" in order, Install for an admin when it is not installed and "Ask an
 * admin" (with the admins) for a member; Requests filters by route and keeps
 * a face's ring inside its cell; Your settings' Connections shows each
 * installed provider with the callback's outcome; the connect page offers
 * Connect, or says the link expired; a chat-born session shows where it came
 * from and how it was routed. The settings menu's Chat is a page with its
 * children.
 */

import { afterEach, describe, expect, test } from "bun:test";
import type { ReactNode } from "react";
import { ToastProvider, TooltipProvider } from "@dude/design-system/primitives";
import type { ChatInstallationView, ChatRequestRow, SessionChatOrigin } from "@dude/domain";
import { act, click, mount, settle, until } from "./dom.ts";
import { FixtureClient } from "../src/fixtures/client.ts";
import { ChatPages, chatNav, useChatOverview } from "../src/screens/ChatSettings.tsx";
import { ChatConnections } from "../src/screens/ChatConnections.tsx";
import { ConnectScreen } from "../src/screens/ConnectScreen.tsx";
import { SessionOrigin, TaskOrigin } from "../src/chatOrigin.tsx";
import { parsePlace, formatPlace } from "../src/place.ts";

let mounted: Array<() => Promise<void>> = [];
afterEach(async () => {
  for (const unmount of mounted) await unmount();
  mounted = [];
  document.body.innerHTML = "";
  window.history.replaceState(null, "", "/");
});

async function show(node: ReactNode, testId: string) {
  const { container, unmount } = await mount(<TooltipProvider><ToastProvider>{node}</ToastProvider></TooltipProvider>);
  mounted.push(unmount);
  await until(() => container.querySelector(`[data-testid="${testId}"]`), testId);
  await settle();
  return container;
}

class Chatting extends FixtureClient {
  saved: string[] = [];
  member = false;
  installed = true;
  override async setRoutingInstructions(additional: string) {
    this.saved.push(additional);
    return super.setRoutingInstructions(additional);
  }
  override async chatOverview() {
    const o = await super.chatOverview();
    return { ...o, canEdit: !this.member, providers: o.providers.map((p) => ({ ...p, installed: this.installed })) };
  }
  override async chatInstallation(): Promise<ChatInstallationView> {
    const v = await super.chatInstallation();
    return this.installed ? { ...v, canEdit: !this.member, health: this.member ? null : v.health, people: this.member ? null : v.people }
      : { ...v, installed: false, canEdit: !this.member, workspaceName: null, health: null, people: null, connectedCount: 0 };
  }
}

function Pages({ client, page, onPage = () => {} }: { client: Chatting; page: string; onPage?: (p: string) => void }) {
  const overview = useChatOverview(client);
  return <ChatPages client={client} page={page} orgName="Acme" admin={!client.member} overview={overview} onPage={onPage} onChanged={() => {}}
    judgementTier={{ name: "Fast", model: "gpt-6-sol-mini" }} />;
}

const text = (el: Element | null | undefined) => (el?.textContent ?? "").replace(/\s+/g, " ").trim();

describe("the Chat page", () => {
  test("says the fixed facts, who gets a session, and lists the providers; an admin saves the additional instructions", async () => {
    const client = new Chatting("a");
    const opened: string[] = [];
    const c = await show(<Pages client={client} page="chat" onPage={(p) => opened.push(p)} />, "chat-page");
    const facts = text(c.querySelector("[data-testid=chat-facts]"));
    for (const fact of ["The Judgements tier (Fast · gpt-6-sol-mini)", "75%", "At most 2, then a brainstorm", "At most 12 tasks per epic"]) {
      expect(facts).toContain(fact);
    }
    expect([...c.querySelectorAll("dl[aria-label='Who gets the session'] dt")].map(text))
      .toEqual(["Public channel", "Private channel or group message", "Direct message with @dude"]);
    await click(c.querySelector("[data-provider-row=slack]")!);
    expect(opened).toEqual(["chat/slack"]);
    const area = c.querySelector<HTMLTextAreaElement>("[data-testid=chat-instructions]")!;
    expect(area.value).toContain("#support is almost always ABS.");
    // The built-in instructions are collapsed until asked for.
    expect(c.querySelectorAll("[data-testid=chat-builtin-text]").length).toBe(0);
    await click(c.querySelector("[data-testid=chat-builtin-view]")!);
    expect(text(c.querySelector("[data-testid=chat-builtin-text]"))).toContain("You route a message");
    // Save is offered only once something changed, and sends the text.
    expect(c.querySelector<HTMLButtonElement>("[data-testid=chat-instructions-save]")!.disabled).toBe(true);
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value")!.set!.call(area, "Billing goes in ABS.");
      area.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click(c.querySelector("[data-testid=chat-instructions-save]")!);
    await until(() => (client.saved.length ? client.saved : null), "the save");
    expect(client.saved).toEqual(["Billing goes in ABS."]);
  });
});

describe("a provider's page", () => {
  test("shows the installation, its health, and what the model is told in order, the provider's layer marked", async () => {
    const c = await show(<Pages client={new Chatting("a")} page="chat/slack" />, "chat-provider-slack");
    expect(text(c.querySelector("nav"))).toBe("ChatSlack");
    expect(c.querySelector("nav [aria-current=page]")?.textContent).toBe("Slack");
    expect(text(c.querySelector("[data-testid=chat-workspace]"))).toBe("Acme · acme-hq");
    expect(text(c.querySelector("[data-testid=chat-health]"))).toContain("0 failed today");
    const layers = [...c.querySelectorAll("ol[aria-label='What the model is told'] > li")];
    expect(layers.map((l) => l.getAttribute("data-layer"))).toEqual(["builtin", "organisation", "provider"]);
    expect(text(layers[1])).toContain("Acme's");
    expect(text(layers[2])).toContain("This page");
    expect(c.querySelectorAll("[data-testid=chat-disconnect]").length).toBe(1);
  });

  test("its card says the bot user, the people connected behind Show who, and its recent requests", async () => {
    const c = await show(<Pages client={new Chatting("a")} page="chat/slack" />, "chat-provider-slack");
    expect(text(c.querySelector("[data-testid=chat-bot-user]"))).toBe("@dude · UB0DUDE");
    expect(text(c.querySelector("[data-testid=chat-connected-count]"))).toContain("7 people connected");
    expect(c.querySelectorAll("[data-testid=chat-people]").length).toBe(0);
    await click(c.querySelector("[data-testid=chat-people-show]")!);
    expect(c.querySelectorAll("[data-testid=chat-people] li").length).toBe(1);
    await until(() => c.querySelector("section[aria-label] [data-testid=chat-request], [data-testid=chat-request]"), "recent requests");
    expect(text(c)).toContain("Recent requests from Slack");
    expect(c.querySelectorAll("[data-testid=chat-request]").length).toBeLessThanOrEqual(5);
  });

  test("long instructions take the control column: their row fills it", async () => {
    const c = await show(<Pages client={new Chatting("a")} page="chat/slack" />, "chat-provider-slack");
    const area = c.querySelector("[data-testid=chat-provider-instructions]")!;
    expect(area.closest("[data-fill=true]")).not.toBeNull();
  });

  test("not installed: an admin may add it; a member is asked to ask an admin, by name", async () => {
    const admin = new Chatting("a");
    admin.installed = false;
    let c = await show(<Pages client={admin} page="chat/slack" />, "chat-provider-slack");
    expect(text(c.querySelector("[data-testid=chat-install]"))).toBe("Add dude to Slack");
    expect(c.querySelectorAll("[data-testid=chat-ask-admin]").length).toBe(0);
    for (const unmount of mounted) await unmount();
    mounted = [];
    const member = new Chatting("a");
    member.installed = false;
    member.member = true;
    c = await show(<Pages client={member} page="chat/slack" />, "chat-provider-slack");
    expect(c.querySelectorAll("[data-testid=chat-install]").length).toBe(0);
    const ask = text(c.querySelector("[data-testid=chat-ask-admin]"));
    expect(ask).toContain("Ask an admin to add dude to Slack.");
    expect(ask).toContain("Márcio");
  });

  test("an install's callback outcome is said once, then dropped from the address", async () => {
    window.history.replaceState(null, "", "/?chatInstall=slack&outcome=workspace_taken#/org/settings/chat/slack");
    const c = await show(<Pages client={new Chatting("a")} page="chat/slack" />, "chat-provider-slack");
    expect(text(c.querySelector("[data-testid=chat-install-outcome]"))).toContain("already connected to another organisation");
    expect(window.location.search).toBe("");
  });
});

describe("Requests", () => {
  test("lists every request with its route and filters by route; each row has its person's face", async () => {
    const client = new Chatting("a");
    const asked: Array<{ provider?: string | undefined; route?: string | undefined }> = [];
    const original = client.chatRequests.bind(client);
    client.chatRequests = (f = {}) => {
      asked.push(f);
      return original(f);
    };
    const c = await show(<Pages client={client} page="chat/requests" />, "chat-requests");
    await until(() => c.querySelector("[data-testid=chat-request]"), "a request");
    const rows = [...c.querySelectorAll("[data-testid=chat-request]")];
    expect(rows.map((r) => r.getAttribute("data-route"))).toEqual(["epic", "brainstorm", "brainstorm", "not_connected"]);
    expect(text(rows[1])).toContain("Shared with 2 · 2 not connected");
    expect(text(rows[2])).toContain("was Task");
    expect(text(rows[3])).toContain("Asked to connect");
    // The face is there; that its ring is not clipped is a layout fact happy-dom cannot measure (checked in the browser shots).
    expect(rows[0]!.querySelector(".chatRequestWho [role=img]")).not.toBeNull();
    expect(asked[0]).toEqual({ provider: undefined, route: undefined, limit: undefined, summary: true });
  });

  test("the summary is the server's count over the week, not the rows listed; a provider page asks for 5 rows", async () => {
    const client = new Chatting("a");
    const asked: Array<Parameters<typeof client.chatRequests>[0]> = [];
    const original = client.chatRequests.bind(client);
    client.chatRequests = async (f = {}) => {
      asked.push(f);
      return { ...(await original(f)), ...(f.summary ? { week: { mentions: 312, asked: 290 } } : {}) };
    };
    const c = await show(<Pages client={client} page="chat/requests" />, "chat-requests");
    await until(() => c.querySelector("[data-testid=chat-requests-summary]"), "the summary");
    expect(text(c.querySelector("[data-testid=chat-requests-summary]"))).toBe("Last 7 days · 312 mentions · 290 asked the model");
    for (const unmount of mounted) await unmount();
    mounted = [];
    asked.length = 0;
    const p = await show(<Pages client={client} page="chat/slack" />, "chat-provider-slack");
    await until(() => p.querySelector("[data-testid=chat-request]"), "recent requests");
    expect(asked).toEqual([{ provider: "slack", route: undefined, limit: 5, summary: undefined }]);
  });

  test("a row's where-line names the provider inline; its reasoning is behind a focusable ⓘ; a summary heads the list", async () => {
    const c = await show(<Pages client={new Chatting("a")} page="chat/requests" />, "chat-requests");
    await until(() => c.querySelector("[data-testid=chat-request]"), "a request");
    const where = c.querySelector("[data-testid=chat-request-where]")!;
    expect(text(where)).toMatch(/^.+ · Slack · #/);
    expect(where.querySelector("[data-provider=slack]")).not.toBeNull();
    const why = c.querySelector<HTMLButtonElement>("[data-testid=chat-request-why]")!;
    expect(why.tagName).toBe("BUTTON");
    expect(why.getAttribute("aria-label")).toMatch(/^Why: /);
    expect(text(c.querySelector("[data-testid=chat-requests-summary]"))).toMatch(/^Last 7 days · \d+ mentions? · \d+ asked the model$/);
  });

  test("is an admin's: a member is told so", async () => {
    const member = new Chatting("a");
    member.member = true;
    const { container, unmount } = await mount(<TooltipProvider><ToastProvider><Pages client={member} page="chat/requests" /></ToastProvider></TooltipProvider>);
    mounted.push(unmount);
    await until(() => (container.textContent?.includes("Only Acme’s admins see Requests") ? container : null), "the refusal");
    expect(container.querySelectorAll("[data-testid=chat-requests]").length).toBe(0);
  });
});

describe("Connections and the connect page", () => {
  test("each installed provider has a card; the callback's outcome shows on it once", async () => {
    window.history.replaceState(null, "", "/?chat=slack&outcome=already_linked#/me/settings");
    const client = new Chatting("a");
    const c = await show(<ChatConnections client={client} me={{ id: "per_x", name: "Márcio Martins" }} />, "chat-connection-slack");
    const card = c.querySelector("[data-testid=chat-connection-slack]")!;
    expect(text(card)).toContain("That Slack account is already connected to someone else");
    expect(text(card)).toContain("Connected");
    expect(card.querySelector("[aria-label='Slack @marcio is Márcio Martins in dude']")).not.toBeNull();
    expect(window.location.search).toBe("");
    // Nothing installed: no card at all.
    for (const unmount of mounted) await unmount();
    mounted = [];
    const none = new Chatting("a");
    none.installed = false;
    const { container, unmount } = await mount(<ChatConnections client={none} />);
    mounted.push(unmount);
    await settle(120);
    expect(container.querySelectorAll("[data-testid^=chat-connection]").length).toBe(0);
  });

  test("the connect page offers Connect while ready, and says so when the link expired", async () => {
    const ready = new (class extends Chatting {
      override chatConnectState() {
        return Promise.resolve({ status: "ready" as const, provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" },
          workspaceName: "Acme", connected: null });
      }
    })("a");
    let c = await show(<ConnectScreen client={ready} provider="slack" state="st" onSettings={() => {}} />, "chat-connect");
    expect(c.querySelector("[data-testid=chat-connect]")!.getAttribute("data-status")).toBe("ready");
    expect(text(c.querySelector("h1"))).toBe("Connect your Slack account");
    expect(text(c.querySelector("[data-testid=chat-connect-go]"))).toBe("Connect Slack");
    for (const unmount of mounted) await unmount();
    mounted = [];
    const expired = new (class extends Chatting {
      override chatConnectState() {
        return Promise.resolve({ status: "expired" as const, provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" },
          workspaceName: "Acme", connected: null });
      }
    })("a");
    c = await show(<ConnectScreen client={expired} provider="slack" state="st" onSettings={() => {}} />, "chat-connect");
    expect(text(c.querySelector("h1"))).toBe("This connect link has expired");
    expect(c.querySelectorAll("[data-testid=chat-connect-go]").length).toBe(0);
  });

  test("the connect page shows the two accounts being linked, who is signed in, Not you?, and Back to the provider once linked", async () => {
    const me = { id: "per_m", name: "Márcia Costa", email: "marcia@example.com", organisation: "Acme" };
    let signedOut = false;
    const ready = new (class extends Chatting {
      override chatConnectState() {
        return Promise.resolve({ status: "ready" as const, provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" },
          workspaceName: "Acme", connected: null, backUrl: "https://acme.slack.com/archives/C1/p1" });
      }
    })("a");
    let c = await show(<ConnectScreen client={ready} provider="slack" state="st-9" onSettings={() => {}} me={me} onSignOut={() => (signedOut = true)} />,
      "chat-connect");
    const pair = c.querySelector("[data-testid=identity-pair]")!;
    expect(pair.getAttribute("aria-label")).toBe("Slack @you is Márcia Costa in dude");
    expect(text(pair)).toContain("marcia@example.com");
    expect(text(c.querySelector("[data-testid=chat-connect-who]"))).toBe("Signed in to dude as marcia@example.com · Not you?");
    await click(c.querySelector("[data-testid=chat-connect-not-you]")!);
    expect(signedOut).toBe(true);
    for (const unmount of mounted) await unmount();
    mounted = [];
    // After the sign-in the state is spent: Back goes where the button was, remembered from before.
    const linked = new (class extends Chatting {
      override chatConnectState() {
        return Promise.resolve({ status: "connected" as const, provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" },
          workspaceName: "Acme", connected: { handle: "marcia", displayName: "Márcia Costa" } });
      }
    })("a");
    c = await show(<ConnectScreen client={linked} provider="slack" state="st-9" onSettings={() => {}} me={me} />, "chat-connect");
    expect(c.querySelector("[data-testid=chat-connect-back]")!.getAttribute("href")).toBe("https://acme.slack.com/archives/C1/p1");
    expect(c.querySelector("[data-testid=identity-pair]")!.getAttribute("aria-label")).toBe("Slack @marcia is Márcia Costa in dude");
  });

  test("the connect place round-trips with its state", () => {
    expect(parsePlace("#/o/acme/connect/slack?state=abc")).toEqual({ view: "connect", provider: "slack", state: "abc" });
    expect(formatPlace({ view: "connect", provider: "slack", state: "a b" })).toBe("#/connect/slack?state=a%20b");
  });
});

describe("a chat-filed task", () => {
  test("says where it was filed from and quotes the thread as filed", async () => {
    const origin = { provider: "slack", workspaceId: "T1", conversationId: "C1", conversationName: "#support", threadId: "1.0",
      permalink: "https://acme.slack.com/archives/C1/p1", requestedBy: "Ana",
      thread: [{ by: "Priya (Support)", text: "exports spin then fail", inDude: false }, { by: "Ana", person: "per_ana", text: "720 rows", inDude: true }] };
    const c = await show(<TaskOrigin origin={origin} names={(id) => (id === "per_ana" ? { id, name: "Ana Ribeiro" } : null)} />, "task-origin-thread");
    expect(text(c.querySelector("[data-testid=task-origin-callout]"))).toBe(
      "Filed from Slack by Ana's mention in #support, in the Backlog. Nothing runs until someone starts it.");
    expect(text(c.querySelector("figure figcaption"))).toBe("Quoted from Slack·#support·2 messagesOpen in Slack");
    expect(c.querySelector("[aria-label='Priya (Support), not in dude']")!.textContent).toBe("PS");
    expect(text(c.querySelector("figure"))).toContain("Ana Ribeiro");
  });
});

describe("a chat-born session", () => {
  test("quotes its thread, people outside dude marked, and says how it was routed and who was skipped", async () => {
    const origin: SessionChatOrigin = {
      provider: "slack", workspaceId: "T1", conversationId: "G1", conversationName: "#design-private", conversationKind: "private",
      threadId: "1.0", permalink: "https://acme.slack.com/archives/G1/p1", requestedBy: "Márcia",
      thread: [{ by: "Zed Outside", text: "exports are slow", inDude: false }, { by: "Eli", person: "per_eli", text: "same", inDude: true }],
      routing: { route: "brainstorm", confidence: 0.79, reasoning: "A discussion.", added: ["per_eli"], skipped: ["U3"], skippedNames: ["Gus"], requestId: "chr_1" },
    };
    const c = await show(<SessionOrigin origin={origin} names={(id) => (id === "per_eli" ? { id, name: "Eli Park" } : null)} />, "session-origin");
    expect(text(c.querySelector("figure figcaption"))).toContain("#design-private");
    expect(c.querySelector("[aria-label='Zed Outside, not in dude']")).not.toBeNull();
    expect(text(c.querySelector("[data-testid=routing-line] summary"))).toContain("Routed as a brainstorm · 79% sure");
    expect(text(c.querySelector("[data-testid=routing-shared]"))).toBe("· Shared with 1 person from #design-private; 1 is not connected");
    expect(text(c.querySelector("[data-testid=routing-line]"))).toContain("Skipped, not connectedGUGus");
    expect(text(c.querySelector("[data-testid=routing-line]"))).toContain("Asked the model once, on the Judgements tier.");
  });
});

describe("the settings menu", () => {
  test("Chat is a page with a child per provider, off when not installed, and Requests for admins", () => {
    const overview = { canEdit: true, admins: [], providers: [
      { provider: { id: "slack", name: "Slack", glyph: "slack", workspaceWord: "workspace" }, installed: false, workspaceName: null, installedAt: null, connected: null },
    ] };
    const admin = chatNav(overview, true);
    expect(admin.page).toBe(true);
    expect(admin.items!.map((i) => [i.id, i.label, i.note ?? null])).toEqual([["chat/slack", "Slack", "off"], ["chat/requests", "Requests", null]]);
    expect(chatNav(overview, false).items!.map((i) => i.id)).toEqual(["chat/slack"]);
  });
});

export type { ChatRequestRow };
