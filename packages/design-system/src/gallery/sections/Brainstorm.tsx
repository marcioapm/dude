import { useState } from "react";
import { Block, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import { AgentAvatar } from "../../components/AgentAvatar.tsx";
import { StatusMark } from "../../components/StatusMark.tsx";
import { Capabilities, LinkedProjects, ProposalCard, SessionPeople, SessionRow, SessionTitle, SharedMark, type ProposalCardItem } from "../../components/Brainstorm.tsx";
import { ChatNotice } from "../../components/ChatNotice.tsx";
import { ScreenHeader } from "../../components/ScreenHeader.tsx";
import { Button } from "../../primitives/Button.tsx";
import { Input } from "../../primitives/Input.tsx";
import { SidebarSessions } from "../../components/Sidebar.tsx";
import { PublishedFiles, SessionRail, SessionRailBlock } from "../../components/SessionRail.tsx";
import { ModelPicker } from "../../components/ModelPicker.tsx";
import { GALLERY_MISFIT, GALLERY_ORG_MODEL, GALLERY_TIERS } from "./Welcome.tsx";
import { people } from "../navFixtures.ts";
import styles from "../gallery.module.css";

/** The app's line under the rail's picker. */
const RAIL_MODEL_NOTE = "Applies the next time the agent starts.";

const P = people;

/** The card as Ana sees it: Márcio's edit stays for him; one item filed already. */
const ITEMS: ProposalCardItem[] = [
  { kind: "epic", title: "Usage metering, watch-only", detail: "Count experiment runs per org per day; charge nothing yet.",
    project: { key: "BL", name: "billing" }, canFile: true },
  { kind: "task", child: true, title: "Dedupe experiment runs on run id in the rollup",
    detail: "The meter's 24h key isn't enough: retries can come days later.", project: { key: "BL", name: "billing" }, canFile: true },
  { kind: "task", child: true, title: "Usage panel shows a cost estimate per kind", detail: "Estimate only, at list price; labelled as such.",
    project: { key: "WC", name: "web-console" }, canFile: true, filed: { by: "Ana Ribeiro", key: "WC-240" } },
  { kind: "edit", title: "Edit BL-58 · Daily per-org usage rollup", tag: "Márcio's · not started", taskKey: "BL-58",
    before: "meter_daily(org, kind, day, count), backfilled from events.",
    after: "meter_daily(org, kind, day, count), backfilled from events, counting each run id once.",
    canFile: false, why: "Only Márcio can file this: it's his task" },
  { kind: "comment", title: "Comment on WC-214 · Checkout v2", tag: "Ana's", taskKey: "WC-214",
    detail: "“Invoice for annual plans assumes the plan step owns the method list; v2 should keep it there.”", canFile: true },
];

function Card({ readOnly }: { readonly readOnly?: boolean }) {
  const [selected, setSelected] = useState<Set<number>>(() => new Set([0, 1, 3, 4]));
  return (
    <ProposalCard items={ITEMS} selected={selected} filingAs="Ana" readOnly={readOnly}
      onToggle={(i) => setSelected((s) => { const n = new Set(s); if (n.has(i)) n.delete(i); else n.add(i); return n; })}
      onFile={() => undefined} />
  );
}

function TitleDemo({ initial }: { readonly initial: string | null }) {
  const [title, setTitle] = useState(initial);
  return <SessionTitle title={title} onRename={async (t) => setTitle(t)} />;
}

/** A rename whose save waits on "Finish the save", as a slow request would, beside another field to move to meanwhile. */
function SlowTitleDemo() {
  const [title, setTitle] = useState<string | null>("Usage-based billing");
  const [pending, setPending] = useState<(() => void) | null>(null);
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 12 }} data-testid="slow-title">
      <SessionTitle title={title} onRename={(t) => new Promise<void>((resolve) => {
        setPending(() => () => { setTitle(t); setPending(null); resolve(); });
      })} />
      <Button size="sm" variant="secondary" disabled={!pending} onClick={() => pending?.()}>Finish the save</Button>
      <Input aria-label="Another field" placeholder="Another field" size="sm" />
    </div>
  );
}

export function BrainstormSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section id="brainstorm" title="Brainstorm sessions"
      intro="A conversation with an agent that belongs to its members, not a task. It reads the linked projects and proposes work; a member files it with a click, as themselves.">
      <Block id="bs-avatar" title="The brainstorm's face"
        note="The conductor's colour and round shape, its own bulb glyph and label: the glyph tells the two conversation agents apart. Beside needs-you, it has none of the attention tone's hue, so it never reads as waiting on you. Grayscale keeps all three apart by glyph and shape.">
        <Panes mode={mode}>
          <Col>
            <Row style={{ gap: 16 }}>
              <AgentAvatar role="conductor" size="chat" name="Conductor" />
              <AgentAvatar role="brainstorm" size="chat" name="Brainstorm" />
              <StatusMark status="awaiting_input" />
            </Row>
            <States items={[
              ["sm / md / lg", <>{(["sm", "md", "lg"] as const).flatMap((s) => [
                <AgentAvatar key={`c-${s}`} role="conductor" size={s} />, <AgentAvatar key={`b-${s}`} role="brainstorm" size={s} />,
              ])}</>],
              ["solid", <><AgentAvatar role="conductor" size="md" solid /><AgentAvatar role="brainstorm" size="md" solid /><StatusMark status="awaiting_input" size="sm" /></>],
              ["grayscale", <span style={{ filter: "grayscale(1)", display: "inline-flex", alignItems: "center", gap: 8 }}>
                <AgentAvatar role="conductor" size="md" /><AgentAvatar role="brainstorm" size="md" /><StatusMark status="awaiting_input" />
              </span>],
            ]} />
          </Col>
        </Panes>
      </Block>
      <Block id="bs-title" title="SessionTitle"
        note="A session's name in its header. Untitled until its agent or a member names it: “New session” in muted ink. A member who can chat presses it to rename in place — Enter saves, Escape cancels; a reader's is plain words. The Chat says who named it, signed by the agent or naming the person.">
        <Panes mode={mode}>
          <Col>
            <Label>untitled, can chat</Label>
            <TitleDemo initial={null} />
            <Label>named, can chat (press it)</Label>
            <TitleDemo initial="Usage-based billing" />
            <Label>a reader</Label>
            <SessionTitle title="Usage-based billing" />
            <Label>a slow save: the title takes the focus back only if it was not moved meanwhile</Label>
            <SlowTitleDemo />
            <Label>in a screen's header, as the session screen draws it</Label>
            <div style={{ width: "100%" }} data-testid="title-in-header">
              <ScreenHeader fillTitle title={<TitleDemo initial="Billing v2" />}
                meta={<><SharedMark owner={P["marcio"]} label="Shared with 2" /><span>Brainstorm · claude-opus-5-5</span></>}
                actions={<Button size="sm" variant="secondary">Share</Button>} />
              <ScreenHeader fillTitle title={<TitleDemo initial={null} />} meta={<span>Brainstorm</span>} />
              <ScreenHeader fillTitle title={<TitleDemo initial={"Usage-based billing for experiment runs, with a dedupe on run id ".repeat(4).trim()} />}
                meta={<span>Brainstorm</span>} />
            </div>
            <Label>in the Chat</Label>
            <ChatNotice kind="renamed" by="Brainstorm" text="Named it “Usage-based billing”" at={Date.now() - 60_000} />
            <ChatNotice kind="renamed" text="Ana renamed it “Billing v2”" at={Date.now()} />
          </Col>
        </Panes>
      </Block>
      <Block id="bs-proposal" title="ProposalCard"
        note="Filing acts as the person who presses File. An item only someone else may file is dimmed and says who; one filed says who filed it as what. A reader sees the card and files nothing. Nothing on it names the session.">
        <Panes mode={mode} surface>
          <Col>
            <Label>Ana, who can chat</Label>
            <Card />
            <Label>a reader</Label>
            <Card readOnly />
          </Col>
        </Panes>
      </Block>
      <Block id="bs-row" title="SessionRow / SharedMark"
        note="A session in the list: what it filed, the projects it reads, how it is. Shared ones carry the shared glyph, and the owner's face when it is someone else.">
        <Panes mode={mode}>
          <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
            <SessionRow title="Usage-based billing" summary="Filed 1 epic, 4 tasks · edited BL-58" state="Talking" age="2m ago"
              projects={[{ key: "WC", name: "web-console", repositories: 2 }, { key: "BL", name: "billing", repositories: 1 }]}
              shared={{ owner: P["marcio"] }} onOpen={() => undefined} />
            <SessionRow title="Q4 cleanup ideas" summary="Nothing filed yet" state="Parked" age="3 days ago" projects={[]} onOpen={() => undefined} />
          </ul>
          <SharedMark />
        </Panes>
      </Block>
      <Block id="bs-sidebar" title="SidebarSessions"
        note="Above Projects in the sidebar: only sessions you are in, then New session.">
        <Panes mode={mode}>
          <div style={{ width: 280 }}>
            <SidebarSessions selected="s2" onSelect={() => undefined} onNew={() => undefined} onOpenList={() => undefined}
              sessions={[{ id: "s1", title: "Meter v2 notes" }, { id: "s2", title: "Usage-based billing", shared: true, owner: P["marcio"] }]} />
          </div>
        </Panes>
      </Block>
      <Block id="bs-rail" title="SessionPeople / LinkedProjects / ModelPicker / Capabilities"
        note="The session's rail: its people (here: has it open now), what it reads, the model its agent runs on (the owner changes it here; it applies at the agent's next start), and what it can and cannot do.">
        <Panes mode={mode}>
          <div style={{ width: 300 }}>
            <SessionRail>
              <SessionRailBlock label="People">
                <SessionPeople members={[
                  { person: P["marcio"]!, role: "owner", open: true, you: true },
                  { person: P["ana"]!, role: "chat", open: true },
                  { person: P["tom"]!, role: "read" },
                  { person: P["lin"]!, role: "chat", invited: true },
                ]} />
              </SessionRailBlock>
              <SessionRailBlock label="Linked">
                <LinkedProjects projects={[
                  { key: "WC", name: "web-console", repositories: [{ name: "web", defaultBranch: "main" }, { name: "api", defaultBranch: "main" }] },
                  { key: "BL", name: "billing", repositories: [] },
                ]} />
              </SessionRailBlock>
              <SessionRailBlock label="Model" data-testid="rail-model">
                <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: null, harness: null }} onChange={() => undefined} />
                <span className={styles["railNote"]}>{RAIL_MODEL_NOTE}</span>
              </SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read linked code, tasks, PRs, findings", "Propose epics and tasks · you file them"]}
                  cannot={["Change code, push, start or steer work", "Touch projects you didn't link"]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </Panes>
      </Block>
      <Block id="bs-rail-model" title="The rail's Model"
        note="The owner's chip on a chosen pair (no default mark), a member's read-only chip on the hover wash with nothing to open, and a pair that stopped fitting: the attention mark, and why under the chip. Under each, muted: when a change applies.">
        <Panes mode={mode}>
          <div style={{ width: 300 }}>
            <SessionRail>
              <SessionRailBlock label="Model · chosen" data-testid="rail-model-chosen">
                <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: "mtr_sol", harness: "codex" }} onChange={() => undefined} />
                <span className={styles["railNote"]}>{RAIL_MODEL_NOTE}</span>
              </SessionRailBlock>
              <SessionRailBlock label="Model · read only" data-testid="rail-model-readonly">
                <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: "mtr_coder", harness: null }} readOnly />
                <span className={styles["railNote"]}>{RAIL_MODEL_NOTE}</span>
              </SessionRailBlock>
              <SessionRailBlock label="Model · no longer fits" data-testid="rail-model-misfit">
                <ModelPicker tiers={GALLERY_TIERS} organization={GALLERY_ORG_MODEL} value={{ tier: "mtr_coder", harness: "codex" }}
                  misfit={GALLERY_MISFIT} onChange={() => undefined} />
                <span className={styles["railNote"]}>{RAIL_MODEL_NOTE}</span>
              </SessionRailBlock>
            </SessionRail>
          </div>
        </Panes>
      </Block>
      <Block id="bs-files" title="PublishedFiles"
        note="The rail's Files: what the session's agent published for its members, newest first, each by its kind's glyph and name, a version mark when published again, and under the name, muted, the one line the agent said it is for when it said one. Picking one opens it in the task's file viewer (FileViewer, ArtifactPreview). Long lists end in “N more”.">
        <Panes mode={mode}>
          <div style={{ width: 300 }}>
            <SessionRail>
              <SessionRailBlock label="Files 4">
                <PublishedFiles onOpen={() => undefined} files={[
                  { name: "design/metering.md", contentType: "text/markdown", versions: 3, description: "How usage is metered and billed" },
                  { name: "usage-by-kind.csv", contentType: "text/csv", description: "Usage per kind, last 30 days" },
                  { name: "flow.svg", contentType: "image/svg+xml" },
                  { name: "rollup.json", contentType: "application/json" },
                ]} />
              </SessionRailBlock>
              <SessionRailBlock label="Files, many">
                <PublishedFiles onOpen={() => undefined} max={2} files={[
                  { name: "a.md", contentType: "text/markdown" }, { name: "b.md", contentType: "text/markdown" },
                  { name: "c.md", contentType: "text/markdown" },
                ]} />
              </SessionRailBlock>
            </SessionRail>
          </div>
        </Panes>
      </Block>
    </Section>
  );
}
