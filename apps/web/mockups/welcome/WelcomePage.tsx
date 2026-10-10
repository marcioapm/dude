/*
 * The welcome page: what dude opens on, and what New session opens. It is
 * not a session — nothing is made until the first message is sent, and
 * then the session is made with that message, what it reads, and opened.
 *
 * Composed from the app's own pieces (ChatComposer, ProjectAvatar,
 * SessionTitle, ChatTranscript, the session rail). New pieces are in
 * welcome.module.css and named in docs/design/session-welcome.md: the
 * greeting, the starter pills, the link chips in the composer and the
 * short list of recent sessions.
 *
 * A starter writes its words in the composer for the person to finish;
 * nothing is sent by itself.
 */

import { useRef, useState } from "react";
import {
  Capabilities, ChatComposer, ChatMessage, ChatTranscript, CostDisplay, LinkedProjects, PersonAvatar, ProjectAvatar, ScreenHeader,
  SessionFacts, SessionPeople, SessionRail, SessionRailBlock, SessionTitle,
} from "@dude/design-system/components";
import { Icon, useTheme } from "@dude/design-system";
import { Button } from "@dude/design-system/primitives";
import dudeSvg from "../../public/dude.svg?url";
import dudeOutlinedSvg from "../../public/dude-outlined.svg?url";
import { P, PROJECTS, STARTERS, partOfDay, type Project, type Recent } from "./data.ts";
import s from "./welcome.module.css";

function Mark({ size }: { size: number }) {
  const box = { width: size, height: size };
  return (
    <span className="dudeMark" style={box} aria-hidden="true">
      <img className="dudeMarkLight" src={dudeSvg} alt="" style={box} />
      <img className="dudeMarkDark" src={dudeOutlinedSvg} alt="" style={box} />
    </span>
  );
}

/** What the session will read, in the composer: each project as a chip, and Link. */
function LinkChips({ linked, onUnlink, onLink }: { linked: readonly Project[]; onUnlink: (key: string) => void; onLink: (p: Project) => void }) {
  const [open, setOpen] = useState(false);
  const left = PROJECTS.filter((p) => !linked.some((l) => l.key === p.key));
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
      {left.length > 0 ? (
        <span className={s.linkWrap}>
          <button type="button" className={s.linkAdd} aria-expanded={open} onClick={() => setOpen((o) => !o)}>
            <Icon name="plus" size={12} /><span className="ds-cap">{linked.length === 0 ? "Link a project" : "Link"}</span>
          </button>
          {open ? (
            <span className={s.linkMenu} role="menu">
              {left.map((p) => (
                <button key={p.key} type="button" role="menuitem" className={s.linkItem} onClick={() => { onLink(p); setOpen(false); }}>
                  <ProjectAvatar project={{ id: p.id, name: p.name }} size={16} />{p.name}
                  <span className={s.linkItemRepos}>{p.repositories.length} repo</span>
                </button>
              ))}
            </span>
          ) : null}
        </span>
      ) : null}
    </span>
  );
}

function RecentSessions({ recent, show, onOpen, onAll }: { recent: readonly Recent[]; show: number; onOpen: (id: string) => void; onAll: () => void }) {
  return (
    <section className={s.recent} aria-label="Recent sessions">
      <div className={s.recentHead}>
        <h2 className="ds-label">Recent sessions</h2>
        <Button size="sm" variant="quiet" onClick={onAll}>All sessions</Button>
      </div>
      <ul className={s.recentList}>
        {recent.slice(0, show).map((r) => (
          <li key={r.id}>
            <button type="button" className={s.recentRow} onClick={() => onOpen(r.id)} data-session={r.id}>
              <Icon name="brainstorm" size={14} className={s.recentIcon} />
              <span className={s.recentTitle}>{r.title}</span>
              {r.shared ? (
                <span className={s.recentShared} aria-label={r.owner ? `shared, ${r.owner.name}'s` : "shared"}>
                  <Icon name="shared" size={12} />{r.owner ? <PersonAvatar person={r.owner} size={16} /> : null}
                </span>
              ) : null}
              <span className={s.recentSummary}>{r.summary}</span>
              <span className={`${s.recentAge} ds-tnum`}>{r.age}</span>
            </button>
          </li>
        ))}
      </ul>
    </section>
  );
}

export function Welcome({ recent, startLinked, onStart, onOpen, onAll }: {
  recent: readonly Recent[];
  startLinked: boolean;
  /** The first message: the session is made with it, and what it reads, and opened. */
  onStart: (text: string, linked: readonly Project[]) => void;
  onOpen: (id: string) => void;
  onAll: () => void;
}) {
  const { density } = useTheme();
  const compact = density === "compact";
  const [text, setText] = useState("");
  const [linked, setLinked] = useState<Project[]>(() => (startLinked ? [PROJECTS[0], PROJECTS[1]] : []));
  const wrap = useRef<HTMLDivElement>(null);
  const fill = (prompt: string) => {
    setText(prompt);
    // The composer takes the focus with the caret at the end, ready to finish the sentence.
    requestAnimationFrame(() => {
      const ta = wrap.current?.querySelector("textarea");
      if (ta) { ta.focus(); ta.setSelectionRange(ta.value.length, ta.value.length); }
    });
  };
  return (
    <div className="screen" data-testid="welcome">
      <div className={s.stage}>
        <div className={s.column}>
          <header className={s.greeting}>
            <Mark size={compact ? 48 : 64} />
            <h1 className={s.hello}>{partOfDay()}, Márcio</h1>
            <p className={s.sub}>What are we working out today?</p>
          </header>
          <div ref={wrap} className={s.composer}>
            <ChatComposer
              mode="chat"
              autoFocus
              value={text}
              onValueChange={setText}
              placeholder="Start a session: an idea, a question, a plan…"
              leading={<LinkChips linked={linked} onLink={(p) => setLinked((l) => [...l, p])} onUnlink={(k) => setLinked((l) => l.filter((p) => p.key !== k))} />}
              to={<>To <b>Brainstorm</b></>}
              onSubmit={({ text: t }) => { onStart(t, linked); return true; }}
              data-testid="welcome-composer"
            />
          </div>
          <div className={s.pills} role="group" aria-label="Ways to start">
            {STARTERS.map((st) => (
              <button key={st.id} type="button" className={s.pill} onClick={() => fill(st.prompt)} title={st.detail}>
                <Icon name={st.icon} size={14} /><span className="ds-cap">{st.title}</span>
              </button>
            ))}
          </div>
          {recent.length > 0 ? <RecentSessions recent={recent} show={compact ? 6 : 4} onOpen={onOpen} onAll={onAll} /> : (
            <p className={s.promise}>A session reads what you link, asks what it needs and proposes work. It changes nothing: you file what you want.</p>
          )}
        </div>
      </div>
    </div>
  );
}

/** A session, as it is today: header, transcript, the rail beside it and the composer at the foot. */
export function Session({ title, first, reply, linked, live }: {
  title: string | null;
  first: string;
  reply?: string | undefined;
  linked: readonly Project[];
  live: boolean;
}) {
  const at = useRef(Date.now()).current;
  return (
    <div className="screen sessionScreen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title={title} onRename={async () => undefined} />} meta={<span>Brainstorm · claude-opus-5-5</span>} />
      <div className="runScreen" data-view="chat">
        <div className="runView">
          <div className="runChat">
            <ChatTranscript fill live={live} revision={reply ? 2 : 1} turns={reply ? 2 : 1}
              footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" onSubmit={() => true} to={<>To <b>Brainstorm</b></>} />}>
              <ChatMessage role="human" person={P["marcio"]} name="Márcio Martins" intent="message" content={first} startedAt={at - (reply ? 3_600_000 : 0)} />
              {reply ? <ChatMessage role="brainstorm" content={reply} startedAt={at - 3_590_000} costUsd={0.03} /> : <ChatMessage role="brainstorm" activity="thinking" />}
            </ChatTranscript>
            <SessionRail className="runRail" aria-label="The session">
              <SessionRailBlock label={<span className="sessionRailHead">People<Button size="sm" variant="quiet">Share</Button></span>}>
                <SessionPeople members={[{ person: P["marcio"]!, role: "owner", open: true, you: true }]} />
              </SessionRailBlock>
              <SessionRailBlock label={<span className="sessionRailHead">Linked<Button size="sm" variant="quiet">Link</Button></span>}>
                <LinkedProjects projects={linked.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories }))} />
              </SessionRailBlock>
              <SessionRailBlock label="It can">
                <Capabilities can={["Read the linked projects' code, tasks, pull requests and findings", "Propose epics, tasks, edits and comments · members file them"]}
                  cannot={["Change code, push, start or steer work", "Read projects nobody linked"]} />
              </SessionRailBlock>
              <SessionRailBlock label="Cost"><SessionFacts facts={[{ label: "Session", value: <CostDisplay usd={reply ? 0.42 : 0.004} /> }]} /></SessionRailBlock>
            </SessionRail>
          </div>
        </div>
      </div>
    </div>
  );
}

/** Today's new session, for comparison: an empty transcript with its one muted line, the rail's empty blocks. */
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
