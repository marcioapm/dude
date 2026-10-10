/*
 * A new session's first screen, three ways. Each is composed from the
 * app's own pieces — ScreenHeader, SessionTitle, ChatComposer, the faces,
 * StatusMark, SessionRow, ChatTranscript once something is sent. What is
 * new is in welcome.module.css and named in the notes under each option:
 * the greeting, the starters, the leads and the link chips in the composer.
 *
 * Pressing a starter or a lead writes its words in the composer for the
 * person to finish. Nothing is sent by itself.
 */

import { useRef, useState, type ReactNode } from "react";
import {
  AgentAvatar, Capabilities, ChatComposer, ChatMessage, ChatTranscript, CostDisplay, LinkedProjects, ProjectAvatar, ScreenHeader,
  SessionFacts, SessionPeople, SessionRail, SessionRailBlock, SessionRow, SessionTitle, StatusMark,
} from "@dude/design-system/components";
import { Icon } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";
import { LEADS, P, PROJECTS, STARTERS, partOfDay, type Lead, type Project, type Starter } from "./data.ts";
import s from "./welcome.module.css";

export type Option = "centred" | "starters" | "context";

function Mark({ size }: { size: number }) {
  const box = { width: size, height: size };
  return (
    <span className="dudeMark" style={box} aria-hidden="true">
      <img className="dudeMarkLight" src={dudeSvg} alt="" style={box} />
      <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={box} />
    </span>
  );
}

/** What the session reads, in the composer: each linked project as a chip, and Link. */
function LinkChips({ linked, onUnlink, onLink }: { linked: readonly Project[]; onUnlink: (key: string) => void; onLink: () => void }) {
  return (
    <span className={s.links} data-testid="composer-links">
      {linked.length === 0 ? <span className={s.linksNone}>Reads memory only</span> : <span className={s.linksLabel}>Reads</span>}
      {linked.map((p) => (
        <span key={p.key} className={s.linkChip}>
          <ProjectAvatar project={{ id: p.id, name: p.name }} size={14} />
          <span className="ds-cap">{p.name}</span>
          <button type="button" className={s.linkX} aria-label={`Stop reading ${p.name}`} onClick={() => onUnlink(p.key)}>
            <Icon name="close" size={12} />
          </button>
        </span>
      ))}
      <button type="button" className={s.linkAdd} onClick={onLink}>
        <Icon name="plus" size={12} /><span className="ds-cap">{linked.length === 0 ? "Link a project" : "Link"}</span>
      </button>
    </span>
  );
}

function Greeting({ align = "center", mark = "dude", sub }: { align?: "center" | "start"; mark?: "dude" | "brainstorm" | "none"; sub: ReactNode }) {
  return (
    <header className={s.greeting} data-align={align}>
      {mark === "dude" ? <Mark size={64} /> : mark === "brainstorm" ? <AgentAvatar role="brainstorm" size="lg" /> : null}
      <h1 className={s.hello}>{partOfDay()}, Márcio</h1>
      <p className={s.sub}>{sub}</p>
    </header>
  );
}

function StarterPills({ onPick }: { onPick: (st: Starter) => void }) {
  return (
    <div className={s.pills} role="group" aria-label="Ways to start">
      {STARTERS.map((st) => (
        <button key={st.id} type="button" className={s.pill} onClick={() => onPick(st)} title={st.detail}>
          <Icon name={st.icon} size={14} /><span className="ds-cap">{st.title}</span>
        </button>
      ))}
    </div>
  );
}

function StarterTiles({ onPick }: { onPick: (st: Starter) => void }) {
  return (
    <div className={s.tiles} role="group" aria-label="Ways to start">
      {STARTERS.map((st) => (
        <button key={st.id} type="button" className={s.tile} onClick={() => onPick(st)}>
          <span className={s.tileIcon}><Icon name={st.icon} size={16} /></span>
          <span className={s.tileTitle}>{st.title}</span>
          <span className={s.tileDetail}>{st.detail}</span>
        </button>
      ))}
    </div>
  );
}

function LeadMark({ lead }: { lead: Lead }) {
  if (lead.kind === "waiting") return <StatusMark status="awaiting_input" iconOnly />;
  if (lead.kind === "failed") return <StatusMark status="failed" iconOnly />;
  if (lead.kind === "epic") return <span className={s.leadGlyph}><Icon name="layers" size={14} /></span>;
  return <span className={s.leadGlyph}><Icon name="brainstorm" size={14} /></span>;
}

function Leads({ onPick }: { onPick: (l: Lead) => void }) {
  return (
    <section className={s.leads} aria-label="From your work">
      <h2 className={`${s.leadsHead} ds-label`}>From your work</h2>
      <div className={s.leadGrid}>
        {LEADS.map((l) => (
          <button key={l.id} type="button" className={s.lead} data-kind={l.kind} onClick={() => onPick(l)}>
            <span className={s.leadTop}>
              <LeadMark lead={l} />
              <span className={s.leadHeading}>{l.heading}</span>
              {l.project ? <span className={s.leadProject}><ProjectAvatar project={{ id: l.project.id, name: l.project.name }} size={14} />{l.project.name}</span> : null}
            </span>
            <span className={s.leadTitle}>{l.title}</span>
            <span className={s.leadDetail}>{l.detail}</span>
          </button>
        ))}
      </div>
    </section>
  );
}

const PROMISE = "It reads what you link, asks what it needs and proposes work. It changes nothing: you file what you want.";

export function NewSession({ option, startLinked }: { option: Option; startLinked: boolean }) {
  const [text, setText] = useState("");
  const [sent, setSent] = useState<string | null>(null);
  const [linked, setLinked] = useState<Project[]>(() => (startLinked ? [PROJECTS[0], PROJECTS[1]] : []));
  const wrap = useRef<HTMLDivElement>(null);
  const fill = (prompt: string) => {
    if (!prompt) return;
    setText(prompt);
    // The composer takes the focus with the caret at the end, ready to finish the sentence.
    requestAnimationFrame(() => {
      const ta = wrap.current?.querySelector("textarea");
      if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
    });
  };
  const link = () => setLinked((l) => (l.length < PROJECTS.length ? [...l, PROJECTS[l.length]!] : l));
  const unlink = (key: string) => setLinked((l) => l.filter((p) => p.key !== key));

  const composer = (
    <div ref={wrap} className={s.composer}>
      <ChatComposer
        mode="chat"
        autoFocus
        value={text}
        onValueChange={setText}
        placeholder={option === "context" ? "What are we working out?" : "Message the brainstorm…"}
        leading={<LinkChips linked={linked} onLink={link} onUnlink={unlink} />}
        to={<>To <b>Brainstorm</b></>}
        onSubmit={({ text: t }) => { setSent(t); setText(""); return true; }}
        data-testid="session-composer"
      />
    </div>
  );

  const header = (
    <ScreenHeader fillTitle title={<SessionTitle title={null} onRename={async () => undefined} />}
      meta={<span>Brainstorm · claude-opus-5-5</span>} />
  );

  if (sent) return <AfterFirst text={sent} linked={linked} header={header} />;

  return (
    <div className="screen sessionScreen" data-testid="session-screen" data-option={option}>
      {header}
      {option === "centred" ? (
        <div className={s.stage} data-layout="centred">
          <div className={s.column}>
            <Greeting sub="What are we working out today?" />
            {composer}
            <StarterPills onPick={(st) => fill(st.prompt)} />
            <p className={s.promise}>{PROMISE}</p>
          </div>
        </div>
      ) : null}
      {option === "starters" ? (
        <div className={s.stage} data-layout="docked">
          <div className={s.scroll}>
            <div className={s.column} data-wide>
              <Greeting align="start" mark="brainstorm" sub={PROMISE} />
              <StarterTiles onPick={(st) => fill(st.prompt)} />
              <section className={s.recent} aria-label="Recent sessions">
                <div className={s.recentHead}><h2 className="ds-label">Your recent sessions</h2><Button size="sm" variant="quiet">All sessions</Button></div>
                <ul className="sessionList">
                  <SessionRow title="Usage-based billing" summary="Filed 1 epic, 4 tasks" state="Resting" age="yesterday"
                    projects={[{ key: "BL", name: "billing", repositories: 1 }]} onOpen={() => undefined} />
                  <SessionRow title="Q4 cleanup ideas" summary="Nothing filed yet" state="Parked" age="3 days ago" projects={[]} onOpen={() => undefined} />
                </ul>
              </section>
            </div>
          </div>
          <div className={s.dock}><div className={s.column} data-wide>{composer}</div></div>
        </div>
      ) : null}
      {option === "context" ? (
        <div className={s.stage} data-layout="context">
          <div className={s.column} data-wide>
            <Greeting sub={<>WI-2401 is waiting on your answer, and WI-2408 failed again overnight. Talk either through, or start from scratch.</>} />
            {composer}
            <StarterPills onPick={(st) => fill(st.prompt)} />
            <Leads onPick={(l) => fill(l.prompt)} />
          </div>
        </div>
      ) : null}
    </div>
  );
}

/** Once the first message is sent: the session as today, the composer back at the foot and the rail beside it. */
function AfterFirst({ text, linked, header }: { text: string; linked: readonly Project[]; header: ReactNode }) {
  const at = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      {header}
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live revision={1} turns={1}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}>
              <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" content={text} startedAt={at} />
              <ChatMessage role="brainstorm" activity="thinking" />
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label="People"><SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }]} /></SessionRailBlock>
              <SessionRailBlock label="Linked"><LinkedProjects projects={linked.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories }))} /></SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked projects' code, tasks, pull requests and findings", "Propose epics, tasks, edits and comments · members file them"]}
                  cannot={["Change code, push, start or steer work", "Read projects nobody linked"]} />
              </SessionRailBlock>
              <SessionRailBlock label="Cost"><SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={0.004} /> }]} /></SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}

/** Today's screen, for comparison: the header, an empty transcript with its one muted line, the rail, the composer at the foot. */
export function Today() {
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={null} onRename={async () => undefined} />} meta={<span>Brainstorm · claude-opus-5-5</span>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill revision={0} turns={0}
              emptyMessage="Write to start: the agent reads the linked projects, asks what it needs, and proposes work you file yourself. It changes nothing."
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>}
                sentAs="Márcio · everyone in the session sees it" />} />
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label={<span className="sessionRailHead">People<Button size="sm" variant="quiet">Share</Button></span>}>
                <SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }]} />
              </SessionRailBlock>
              <SessionRailBlock label={<span className="sessionRailHead">Linked<Button size="sm" variant="quiet">Link</Button></span>}>
                <span className="muted">Nothing linked: it reads only what the organisation remembers.</span>
              </SessionRailBlock>
              <SessionRailBlock label="Files"><span className="muted">Nothing published yet: documents it writes for you appear here.</span></SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked projects' code, tasks, pull requests and findings", "Propose epics, tasks, edits and comments · members file them"]}
                  cannot={["Change code, push, start or steer work", "Read projects nobody linked"]} />
              </SessionRailBlock>
              <SessionRailBlock label="Cost"><SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={0} /> }]} /></SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}
