import { useState } from "react";
import { Block, Col, Label, Panes, Row, Section, type PaneMode } from "../Frame.tsx";
import {
  ConnectionCard, ExternalThread, FactList, IdentityPair, InstructionStack, OriginFact, ProviderGlyph, ProviderList, ROUTE_SPEC, RouteBadge,
  RoutingLine, type ChatProvider, type ChatRoute,
} from "../../components/Chat.tsx";
import { SettingsLayout } from "../../components/Settings.tsx";
import { Board } from "../../components/Board.tsx";
import { Breadcrumb } from "../../components/Breadcrumb.tsx";
import type { NavProject } from "../../util/navModel.ts";

/*
 * Chat providers: what an incoming chat looks like in dude, provider-neutral.
 * Each piece with Slack and with a placeholder provider (the `integration`
 * glyph), so a second provider is seen to cost no component.
 */

const slack: ChatProvider = { id: "slack", name: "Slack", glyph: "slack" };
const other: ChatProvider = { id: "chatter", name: "Chatter", glyph: "integration" };
const marcia = { id: "per_marcia", name: "Márcia Costa", online: true };
const eli = { id: "per_eli", name: "Eli Park" };
const ana = { id: "per_ana", name: "Ana Ribeiro" };

const thread = [
  { by: { display: "Zed Outside" }, time: "10:02", text: "CSV export times out for 40 hourly meters." },
  { by: { display: "Ana Ribeiro", person: ana }, time: "10:04", text: "Same here, since Monday." },
  { by: { display: "Kim Lee" }, time: "10:05", text: "**Not bold**: this is a quote, never Markdown." },
  { by: { display: "Márcia Costa", person: marcia }, time: "10:07", text: "@dude can you reproduce this?" },
];

const epicBoard: NavProject = {
  id: "prj_abs", name: "abs", epics: [{ id: "epc_csv", title: "CSV exports", tasks: [
    { id: "t1", key: "ABS-31", title: "Measure export time and queries per meter", status: "received", people: [marcia] },
    { id: "t2", key: "ABS-32", title: "Batch the readings query per meter", status: "received", people: [marcia] },
    { id: "t3", key: "ABS-33", title: "Stream the CSV response", status: "received", people: [marcia] },
  ] }], tasks: [{ id: "t4", key: "ABS-29", title: "Typed in dude", status: "received", people: [eli] }],
};

export function ChatProvidersSection({ mode }: { readonly mode: PaneMode }) {
  const [current, setCurrent] = useState("chat");
  return (
    <Section id="chat-providers" title="Chat providers"
      intro="Mention @dude in a team chat, and dude routes it to a session, a task or an epic and replies with the link. Shared surfaces take the provider as props — its name and glyph — and never name one in words of their own.">
      <Block id="cp-glyph" title="ProviderGlyph"
        note="A provider's monochrome glyph in the ink beside it: Slack's mark on the set's stroke (never its colours), and the neutral integration glyph for a provider without its own. 12, 14 and 16px; the last named, standing alone.">
        <Panes mode={mode}>
          <Row>
            {[12, 14, 16].map((s) => <span key={s} style={{ display: "inline-flex", gap: 8 }}><ProviderGlyph provider={slack} size={s} /><ProviderGlyph provider={other} size={s} /></span>)}
            <ProviderGlyph provider={slack} size={16} label />
          </Row>
        </Panes>
      </Block>

      <Block id="cp-route" title="RouteBadge"
        note="Where a mention went, in a word and a glyph. Refusals and failures are subtle; what made or linked something is tinted. `fallback` says what the model answered when dude went another way.">
        <Panes mode={mode}>
          <Col style={{ gap: 8 }}>
            {(["sm", "md"] as const).map((size) => (
              <Row key={size}>{(Object.keys(ROUTE_SPEC) as ChatRoute[]).map((r) => <RouteBadge key={r} route={r} size={size} />)}</Row>
            ))}
            <Row><RouteBadge route="brainstorm" fallback="task" /></Row>
          </Col>
        </Panes>
      </Block>

      <Block id="cp-origin" title="OriginFact"
        note="Where a session, task or epic came from: in a header's meta, a list row (compact) and the rail; Slack and the placeholder.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 8 }}>
            <Label>Header meta</Label>
            <OriginFact provider={slack} place="#support" by="Márcia" href="https://example.com/archives/C1/p1" />
            <Label>List row</Label>
            <OriginFact provider={slack} place="#support" by="Márcia" compact />
            <Label>Rail fact, another provider</Label>
            <OriginFact provider={other} place="#ops" by="Eli" href="https://example.com/ops" />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-thread" title="ExternalThread"
        note="A thread quoted from outside dude: a linked author shows their face and name, anyone else neutral initials and 'not in dude'. Never Markdown. Clipped to the last messages with the way to the rest.">
        <Panes mode={mode} surface>
          <Col style={{ gap: 12 }}>
            <ExternalThread provider={slack} place="#support" messages={thread} href="https://example.com/archives/C1/p1" />
            <ExternalThread provider={other} place="#ops" messages={thread} clip={2} />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-routing" title="RoutingLine"
        note="dude's margin line in a chat-born transcript: closed; open with its reasoning; a fallback; a private channel's with skipped people; with none skipped.">
        <Panes mode={mode}>
          <Col style={{ gap: 8 }}>
            <RoutingLine route="brainstorm" confidence={0.82} reasoning="An investigation: reproduce it first." model="Fast · gpt-6-sol-mini" />
            <RoutingLine route="task" confidence={0.91} reasoning="One clear change in ABS." defaultOpen />
            <RoutingLine route="brainstorm" fallback="task" confidence={0.61} reasoning="Might be a task; under the 75% bar." />
            <RoutingLine route="brainstorm" confidence={0.79} reasoning="A discussion." defaultOpen
              shared={{ place: "#design-private", added: [marcia, eli, ana], skipped: ["Kim Lee", "Zed Outside"] }} />
            <RoutingLine route="brainstorm" confidence={0.88} reasoning="A discussion." shared={{ place: "a group message", added: [eli], skipped: [] }} />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-facts" title="FactList"
        note="Rules that are not settings, read as cases: who gets a session made from chat. Stacked under 720px.">
        <Panes mode={mode}>
          {() => <FactList label="Who gets the session" facts={[
            { icon: "globe", when: "Public channel", then: "Everyone in Acme · can chat. The session is the organisation's." },
            { icon: "lock", when: "Private channel or group message", then: "Shared, as can chat, with every connected member of Acme in that conversation; the rest are listed as skipped." },
            { icon: "human", when: "Direct message with @dude", then: "Private to the person." },
          ]} />}
        </Panes>
      </Block>

      <Block id="cp-connection" title="ConnectionCard / IdentityPair"
        note="Your settings → Connections: GitHub, then one per chat provider the organisation installed. Not connected with an outcome; connected; the placeholder provider connected.">
        <Panes mode={mode}>
          <Col style={{ gap: 12 }}>
            <ConnectionCard title="Slack" glyph={<ProviderGlyph provider={slack} size={16} />} intro="Connect your Slack account so dude can act for you when you mention @dude."
              connected={false} connectLabel="Connect Slack" disconnectLabel="Disconnect Slack…" onConnect={() => {}}
              outcome={{ tone: "attention", text: "Slack authorization was denied. Connect again when ready." }} />
            <ConnectionCard title="Slack" glyph={<ProviderGlyph provider={slack} size={16} />} intro="Mentions of @dude in Acme Slack act as you."
              connected facts={[{ label: "Connected as", value: <IdentityPair provider={slack} external={{ handle: "marcia" }} person={marcia} /> }]}
              connectLabel="Connect Slack" disconnectLabel="Disconnect Slack…" onConnect={() => {}} onDisconnect={() => {}} />
            <ConnectionCard title="Chatter" glyph={<ProviderGlyph provider={other} size={16} />} intro="Mentions of @dude in Chatter act as you."
              connected facts={[{ label: "Connected as", value: "@eli" }]} connectLabel="Connect Chatter" disconnectLabel="Disconnect Chatter…" onDisconnect={() => {}} />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-stack" title="InstructionStack"
        note="What the model is told, in order: built-in (collapsed) → the organisation's → the provider's, the provider's tinted as this page's; and an empty provider layer.">
        <Panes mode={mode}>
          <Col style={{ gap: 12 }}>
            <InstructionStack label="What the model is told" layers={[
              { id: "builtin", title: "Built-in", owner: "dude's own", text: "1\n2\n3\n4\n5\n6\n7\n8\n9\n10", collapsed: true, action: <a href="#cp-stack">View on Chat</a> },
              { id: "org", title: "Acme's", owner: "every chat", text: "#support is almost always ABS.\nBugs about exports go to the Exports epic.", action: <a href="#cp-stack">Edit on Chat</a> },
              { id: "provider", title: "Slack's", owner: "Slack only", text: "#eng-* channels belong to engineering.", here: true },
            ]} />
            <InstructionStack label="What the model is told, nothing added" layers={[
              { id: "builtin", title: "Built-in", owner: "dude's own", text: "1\n2", collapsed: true },
              { id: "org", title: "Acme's", owner: "every chat", text: "" },
              { id: "provider", title: "Chatter's", owner: "Chatter only", text: "", here: true },
            ]} />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-providers" title="ProviderList"
        note="The Chat page's providers: Slack installed and the placeholder available; both installed.">
        <Panes mode={mode}>
          <Col style={{ gap: 12 }}>
            <ProviderList onOpen={() => {}} providers={[
              { provider: slack, installed: true, detail: "Acme workspace · 7 people connected" },
              { provider: other, installed: false, detail: "Not added yet", illustrative: true },
            ]} />
            <ProviderList onOpen={() => {}} providers={[
              { provider: slack, installed: true, detail: "Acme workspace" },
              { provider: other, installed: true, detail: "Acme server" },
            ]} />
          </Col>
        </Panes>
      </Block>

      <Block id="cp-settings" title="Settings: a group that is a page"
        note="Chat is a page with children (one per provider, then Requests): selecting it opens it, and a child carries a small Breadcrumb back. Without `page`, a group opens its first sub-page.">
        <Panes mode={mode}>
          <SettingsLayout scope={{ title: "Acme", subtitle: "Organisation settings" }} current={current} onSelect={setCurrent} items={[
            { id: "members", label: "Members", icon: "human" },
            { id: "chat", label: "Chat", icon: "message", page: true, items: [
              { id: "chat/slack", label: "Slack", icon: "slack" },
              { id: "chat/requests", label: "Requests", note: "12" },
            ] },
            { id: "judgements", label: "Judgements", icon: "list-check" },
          ]}>
            {current === "chat" ? <p>The Chat page.</p> : (
              <Breadcrumb size="sm" items={[{ id: "chat", label: "Chat", icon: "message", onSelect: () => setCurrent("chat") },
                { id: current, label: current === "chat/slack" ? "Slack" : "Requests" }]} />
            )}
          </SettingsLayout>
        </Panes>
      </Block>

      <Block id="cp-board" title="Board: cardLead"
        note="A chat-born task's card carries the provider's glyph and name on its first line, before its key; ABS-29, typed in dude, has none.">
        <Panes mode={mode}>
          <Board project={epicBoard} epic={epicBoard.epics?.[0] ?? null} hideHeader
            cardLead={(card) => (card.task.id === "t4" ? null : <><ProviderGlyph provider={slack} size={12} />Slack</>)} />
        </Panes>
      </Block>
    </Section>
  );
}
