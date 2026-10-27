/**
 * Chat providers in the design system: every route reads in words and a
 * glyph (refusals quieter), an origin links to its permalink, a thread from
 * outside marks who is not in dude on neutral initials, the routing line
 * says who was shared with and who was skipped, the instruction stack is in
 * order with the page's own layer marked, a connection's Connect is primary
 * until connected, and nothing shared names a provider of its own accord.
 * Also the two DS changes: a settings group that is a page selects itself,
 * and a board card's lead sits before its key.
 */

import { afterEach, describe, expect, test } from "bun:test";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { renderToStaticMarkup } from "react-dom/server";
import {
  ConnectionCard, ExternalThread, FactList, IdentityPair, InstructionStack, OriginFact, PromptText, ProviderGlyph, ProviderList, ROUTE_SPEC, RouteBadge,
  RoutingLine, providerGlyph, type ChatProvider, type ChatRoute,
} from "../src/components/Chat.tsx";
import { SettingsLayout } from "../src/components/Settings.tsx";
import { Board } from "../src/components/Board.tsx";
import type { NavProject } from "../src/util/navModel.ts";
import { ICON_NAMES } from "../src/icons/index.tsx";
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
  await act(async () => root!.render(el));
  return host;
}

const text = (h: string) => h.replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();
const slack: ChatProvider = { id: "slack", name: "Slack", glyph: "slack" };
const other: ChatProvider = { id: "chatter", name: "Chatter", glyph: "integration" };
const marcia = { id: "per_m", name: "Márcia Costa" };
const eli = { id: "per_e", name: "Eli Park" };

describe("ProviderGlyph", () => {
  test("is the provider's glyph, decorative beside its name and named when alone; unknown providers get the neutral one", () => {
    expect(ICON_NAMES).toContain("slack");
    const quiet = renderToStaticMarkup(<ProviderGlyph provider={slack} />);
    expect(quiet).toContain('data-icon="slack"');
    expect(quiet).toContain('aria-hidden="true"');
    const named = renderToStaticMarkup(<ProviderGlyph provider={slack} label />);
    expect(named).toContain('role="img"');
    expect(named).toContain("<title>Slack</title>");
    expect(providerGlyph("slack", ICON_NAMES)).toBe("slack");
    expect(providerGlyph("discord", ICON_NAMES)).toBe("integration");
  });
});

describe("RouteBadge", () => {
  test("every route has a word and a glyph; refusals and failures are subtle, what made or linked something tinted", () => {
    for (const route of Object.keys(ROUTE_SPEC) as ChatRoute[]) {
      const h = renderToStaticMarkup(<RouteBadge route={route} />);
      expect(text(h)).toBe(ROUTE_SPEC[route].label);
      expect(h).toContain(`data-icon="${ROUTE_SPEC[route].icon}"`);
    }
    expect(Object.entries(ROUTE_SPEC).filter(([, s]) => s.subtle).map(([r]) => r)).toEqual(["not_connected", "not_member", "failed"]);
    expect(text(renderToStaticMarkup(<RouteBadge route="brainstorm" fallback="task" />))).toBe("Brainstorm was Task");
    expect(text(renderToStaticMarkup(<RouteBadge route="task" fallback="task" />))).toBe("Task");
  });

  test("a failure says what was made instead only when something was", () => {
    expect(text(renderToStaticMarkup(<RouteBadge route="failed" />))).toBe("Failed");
    expect(text(renderToStaticMarkup(<RouteBadge route="failed" made="brainstorm" />))).toBe("Failed → Brainstorm");
    expect(text(renderToStaticMarkup(<RouteBadge route="failed" made="brainstorm" fallback="task" />))).toBe("Failed → Brainstorm was Task");
    expect(text(renderToStaticMarkup(<RouteBadge route="failed" made="task" fallback="task" />))).toBe("Failed → Task");
    expect(text(renderToStaticMarkup(<RouteBadge route="failed" fallback="clarify" />))).toBe("Failed was Clarify");
    // Only a failure names what it made: a route that made something is its own word.
    expect(text(renderToStaticMarkup(<RouteBadge route="task" made="brainstorm" />))).toBe("Task");
  });
});

describe("OriginFact", () => {
  test("names the provider from props, links to its permalink, and drops 'thread by' when compact", async () => {
    const el = await mount(<OriginFact provider={slack} place="#support" by="Márcia" href="https://example.com/p/1" testId="origin" />);
    const link = el.querySelector("a")!;
    expect(link.getAttribute("href")).toBe("https://example.com/p/1");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.textContent!.replace(/\s+/g, " ").trim()).toBe("From Slack·#support·thread by Márcia");
    const compact = text(renderToStaticMarkup(<OriginFact provider={other} place="#ops" by="Eli" compact />));
    expect(compact).toBe("From Chatter · #ops");
  });
});

describe("ExternalThread", () => {
  test("a linked author shows as themselves, anyone else on neutral initials marked 'not in dude'; text is never Markdown", async () => {
    const el = await mount(<ExternalThread provider={slack} place="#support" href="https://example.com/t" clip={2} messages={[
      { by: { display: "Zed Outside" }, text: "first" },
      { by: { display: "Márcia", person: marcia }, text: "**not bold**" },
      { by: { display: "Zed Outside" }, text: "third" },
    ]} />);
    expect(accessibleName(byRole(el, "figure"))).toBe("Thread in #support on Slack");
    // Clipped to the last two, with the way to the rest.
    expect(el.querySelectorAll("li").length).toBe(2);
    await act(async () => byRole(el, "button", "Show 1 earlier").click());
    expect(el.querySelectorAll("li").length).toBe(3);
    expect(allByRole(el, "img").map(accessibleName)).toContain("Zed Outside, not in dude");
    expect(el.textContent).toContain("**not bold**");
    expect(el.querySelectorAll("strong").length).toBe(0);
    expect(byRole(el, "link", "Open in Slack").getAttribute("href")).toBe("https://example.com/t");
    // Its head says where it is quoted from and how long it is.
    expect(el.querySelector("figcaption")!.textContent).toBe("Quoted from Slack·#support·3 messagesOpen in Slack");
  });

  test("an outside name's punctuation is no initial", async () => {
    const el = await mount(<ExternalThread provider={slack} place="#support" messages={[{ by: { display: "Priya (Support)" }, text: "x" }]} />);
    expect(byRole(el, "img", "Priya (Support), not in dude").textContent).toBe("PS");
  });
});

describe("RoutingLine", () => {
  test("says how it was routed and how sure, and for a group who was added and skipped", async () => {
    const el = await mount(<RoutingLine route="brainstorm" confidence={0.82} reasoning="Needs a look." model="Fast · gpt-6"
      shared={{ place: "#design-private", added: [marcia, eli], skipped: ["Gus", "Zed"] }} />);
    const summary = el.querySelector("summary")!.textContent!.replace(/\s+/g, " ").trim();
    expect(summary).toBe("Routed as a brainstorm · 82% sure· Shared with 2 people from #design-private; 2 are not connected");
    const groups = el.querySelector("[aria-label='Who it was shared with']")!;
    expect([...groups.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["Added, can chat", "Skipped, not connected"]);
    expect([...groups.querySelectorAll("dd")].map((d) => d.textContent)).toEqual(["MCMárcia CostaEPEli Park", "GUGusZEZed"]);
    // Skipped people are on neutral initials, named as outside dude.
    expect(allByRole(groups as HTMLElement, "img").map(accessibleName).slice(2)).toEqual(["Gus, not in dude", "Zed, not in dude"]);
    expect(el.textContent).toContain("Fast · gpt-6");
    const fallback = text(renderToStaticMarkup(<RoutingLine route="brainstorm" fallback="task" confidence={0.6} reasoning="x" />));
    expect(fallback).toContain("was Task");
    expect(text(renderToStaticMarkup(<RoutingLine route="brainstorm" reasoning="x" shared={{ place: "#a", added: [eli], skipped: [] }} />)))
      .toContain("Shared with 1 person from #a x");
    // Nobody added: never "with 0 people".
    const none = text(renderToStaticMarkup(<RoutingLine route="brainstorm" reasoning="x" shared={{ place: "#a", added: [], skipped: ["Gus", "Zed"] }} />));
    expect(none).toContain("Made in #a; 2 are not connected, so nobody else was added");
    expect(none).not.toContain("0 people");
  });
});

describe("FactList", () => {
  test("is a named description list of cases", async () => {
    const el = await mount(<FactList label="Who gets the session" facts={[
      { icon: "globe", when: "Public channel", then: "Everyone in Acme can chat." },
      { icon: "lock", when: "Private channel or group message", then: "Shared with connected members." },
    ]} />);
    const dl = el.querySelector("dl")!;
    expect(dl.getAttribute("aria-label")).toBe("Who gets the session");
    expect([...dl.querySelectorAll("dt")].map((d) => d.textContent)).toEqual(["Public channel", "Private channel or group message"]);
  });
});

describe("ConnectionCard", () => {
  test("Connect is primary until connected; Disconnect only when connected; the outcome is announced", async () => {
    let clicked = "";
    const el = await mount(<ConnectionCard title="Slack" glyph={<ProviderGlyph provider={slack} />} intro="Connect to mention @dude." connected={false}
      connectLabel="Connect Slack" disconnectLabel="Disconnect Slack…" onConnect={() => (clicked = "connect")} onDisconnect={() => {}}
      outcome={{ tone: "danger", text: "That request expired." }} testId="chat-connection-slack" />);
    expect(byRole(el, "alert").textContent).toBe("That request expired.");
    expect(byRole(el, "button", "Connect Slack").getAttribute("data-variant")).toBe("primary");
    expect(allByRole(el, "button", "Disconnect Slack…").length).toBe(0);
    await act(async () => byRole(el, "button", "Connect Slack").click());
    expect(clicked).toBe("connect");
    const on = renderToStaticMarkup(<ConnectionCard title="Slack" glyph={null} intro="x" connected facts={[{ label: "Connected as", value: "@eli" }]}
      connectLabel="Connect Slack" disconnectLabel="Disconnect Slack…" onConnect={() => {}} onDisconnect={() => {}} />);
    expect(on).toContain('data-variant="secondary"');
    expect(text(on)).toContain("Connected as @eli");
    expect(text(on)).toContain("Disconnect Slack…");
  });
});

describe("IdentityPair, InstructionStack and ProviderList", () => {
  test("the pair says which account is whom", () => {
    const h = renderToStaticMarkup(<IdentityPair provider={slack} external={{ handle: "eli" }} person={eli} />);
    expect(h).toContain('aria-label="Slack @eli is Eli Park in dude"');
  });

  test("the large pair is two named sides, the provider's and dude's, as one group", async () => {
    const el = await mount(<IdentityPair size="lg" provider={slack} external={{ handle: "eli", display: "Eli Park", workspace: "acme.slack.com" }}
      person={{ ...eli, email: "eli@example.com", organisation: "Acme" }} />);
    const group = byRole(el, "group", "Slack @eli is Eli Park in dude");
    expect(group.textContent).toBe("SlackEP@eliEli Parkacme.slack.comdudeEPEli ParkAcmeeli@example.com");
  });

  test("the large pair with no account yet shows the provider's glyph where a face would be, no handle and no initials", async () => {
    const el = await mount(<IdentityPair size="lg" provider={slack} external={null} workspace="acme-hq" person={{ ...eli, organisation: "Acme" }} />);
    const group = byRole(el, "group", "Your Slack account, once you sign in, is Eli Park in dude");
    expect(group.textContent).toBe("SlackYour Slack accountSigned in nextacme-hqdudeEPEli ParkAcme");
    expect(group.querySelector("[data-unknown=true] svg[data-provider=slack]")).not.toBeNull();
  });

  test("an inline glyph carries the provider's name in the line", () => {
    expect(text(renderToStaticMarkup(<span>Ana · <ProviderGlyph provider={other} size={11} inline /> · #ops</span>))).toBe("Ana · Chatter · #ops");
  });

  test("a prompt's text is shown as written, in a named well a keyboard reaches: no Markdown, no code block", async () => {
    const el = await mount(<PromptText label="Built-in instructions" text={"  You route a message.\n- **not bold**\n```\nnot a fence\n"} />);
    const well = byRole(el, "document", "Built-in instructions");
    expect(well.textContent).toBe("You route a message.\n- **not bold**\n```\nnot a fence");
    expect(well.getAttribute("tabindex")).toBe("0");
    expect(well.querySelectorAll("pre, code, strong, button").length).toBe(0);
  });

  test("the stack is in order, the page's layer marked, the collapsed one head only, an empty one says so", async () => {
    const el = await mount(<InstructionStack label="What the model is told" layers={[
      { id: "builtin", title: "Built-in", owner: "dude's own", text: "a\nb\nc", collapsed: true },
      { id: "org", title: "Acme's", owner: "every chat", text: "Billing goes in BIL." },
      { id: "provider", title: "Slack's", owner: "Slack only", text: "", here: true },
    ]} />);
    const items = [...byRole(el, "list", "What the model is told").querySelectorAll("li")];
    expect(items.map((li) => li.getAttribute("data-layer"))).toEqual(["builtin", "org", "provider"]);
    expect(items[0]!.textContent).toBe("1Built-indude's own · 3 lines");
    expect(items[1]!.textContent).toContain("Billing goes in BIL.");
    expect(items[2]!.textContent).toContain("Nothing added");
    expect(items[2]!.textContent).toContain("This page");
  });

  test("each provider is a row with Installed or Available, opening its page", async () => {
    let opened = "";
    const el = await mount(<ProviderList onOpen={(p) => (opened = p.id)} providers={[
      { provider: slack, installed: true, detail: "Acme workspace" },
      { provider: other, installed: false, detail: "Not added yet", illustrative: true },
    ]} />);
    const rows = allByRole(el, "button");
    expect(rows.map((r) => r.textContent)).toEqual(["SlackAcme workspaceInstalled", "Chatter · illustrativeNot added yetAvailable"]);
    await act(async () => rows[1]!.click());
    expect(opened).toBe("chatter");
  });
});

describe("shared copy names no provider", () => {
  test("rendered with a fictional provider, nothing says Slack", () => {
    const h = renderToStaticMarkup(<>
      <OriginFact provider={other} place="#ops" by="Eli" />
      <ExternalThread provider={other} place="#ops" messages={[{ by: { display: "Zed" }, text: "hi" }]} href="https://example.com" />
      <ConnectionCard title="Chatter" glyph={<ProviderGlyph provider={other} />} intro="Connect." connected={false} connectLabel="Connect Chatter"
        disconnectLabel="Disconnect Chatter…" onConnect={() => {}} />
      <IdentityPair provider={other} external={{ handle: "eli" }} person={eli} />
      <RoutingLine route="epic" confidence={0.9} reasoning="Several tasks." />
    </>);
    expect(h).not.toMatch(/slack/i);
  });
});

describe("SettingsLayout groups", () => {
  test("a page group selects itself; a plain group selects its first sub-page", async () => {
    const chosen: string[] = [];
    const items = [
      { id: "chat", label: "Chat", page: true, items: [{ id: "chat/slack", label: "Slack" }, { id: "chat/requests", label: "Requests" }] },
      { id: "agents", label: "Agents", items: [{ id: "implementer", label: "Implementer" }] },
    ];
    const el = await mount(<SettingsLayout scope={{ title: "Acme" }} items={items} current="chat/slack" onSelect={(id) => chosen.push(id)}>x</SettingsLayout>);
    await act(async () => (el.querySelector('[data-settings-nav="chat"]') as HTMLElement).click());
    await act(async () => (el.querySelector('[data-settings-nav="agents"]') as HTMLElement).click());
    expect(chosen).toEqual(["chat", "implementer"]);
    // The children show while the group or one of them is current.
    expect(el.querySelectorAll('[data-settings-nav="chat/requests"]').length).toBe(1);
  });
});

describe("Board cardLead", () => {
  test("sits on the card's first line, before its key, only where the app gives one", () => {
    const project: NavProject = { id: "p", name: "Billing", epics: [], tasks: [
      { id: "t1", key: "BIL-1", title: "From chat", status: "received" },
      { id: "t2", key: "BIL-2", title: "Typed in dude", status: "received" },
    ] };
    const html = renderToStaticMarkup(<Board project={project} hideHeader
      cardLead={(card) => (card.task.id === "t1" ? <span data-lead="">From Slack</span> : null)} />);
    const card = (key: string) => html.slice(html.indexOf(`data-board-key="task:${key}"`), html.indexOf("</li>", html.indexOf(`data-board-key="task:${key}"`)));
    expect(card("t1").indexOf("data-lead")).toBeGreaterThan(-1);
    expect(card("t1").indexOf("data-lead")).toBeLessThan(card("t1").indexOf(">BIL-1<"));
    expect(card("t2").includes("data-lead")).toBe(false);
  });
});
