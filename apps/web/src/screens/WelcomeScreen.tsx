/**
 * The welcome: what dude opens on, and where New session goes. Nothing is
 * made here until the first message is sent; then the session is made with
 * that message and the projects linked in the composer, in one call, and
 * opened. A starter fills the composer and never sends.
 */

import { useEffect, useRef, useState, type ReactNode } from "react";
import { firstName, useDensity, type NavProject } from "@dude/design-system";
import {
  ChatComposer, ComposerLinks, Duration, RECENT_SESSIONS_SHOWN, RecentSessions, StarterPills, WELCOME_FIRST_TIME, WELCOME_STARTERS, Welcome, WelcomeNote,
  type LinkableProject, type Starter,
} from "@dude/design-system/components";
import { Callout } from "@dude/design-system/primitives";
import { sessionTitle, type SessionLink, type SessionSummary } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { DudeMark } from "../DudeMark.tsx";

/** The greeting's word for now, on the reader's clock. */
export function partOfDay(d: Date = new Date()): string {
  const h = d.getHours();
  if (h < 5) return "Late one";
  if (h < 12) return "Morning";
  if (h < 18) return "Afternoon";
  return "Evening";
}

export function WelcomeScreen({ client, projects, sessions, name, now, offer, onOpenSession, onAllSessions, onCreated }: {
  client: ApiClient;
  /** The organisation's projects: what the composer can link. */
  projects: readonly NavProject[];
  /** Your sessions, newest activity first; null while they load. */
  sessions: readonly SessionSummary[] | null;
  /** The person's name, for the greeting. */
  name: string | null;
  /** The clock the greeting reads; the browser's by default. */
  now?: () => Date;
  /** Under the recent sessions (or the first-time line): New project, for an organisation with none. */
  offer?: ReactNode;
  onOpenSession: (id: string) => void;
  onAllSessions: () => void;
  /** A session was made from here: the app re-reads its list and opens it. */
  onCreated: (id: string) => void;
}) {
  const density = useDensity();
  const [text, setText] = useState("");
  const [linked, setLinked] = useState<LinkableProject[]>([]);
  const [problem, setProblem] = useState<string | null>(null);
  const sending = useRef(false);
  const wrap = useRef<HTMLDivElement>(null);
  // Each linked project's repositories, fetched as it is linked: a session
  // made from here reads all of them (the rail's Link dialog can narrow it).
  const repos = useRef(new Map<string, Promise<string[]>>());
  const reposOf = (id: string) => {
    let found = repos.current.get(id);
    if (!found) {
      found = client.getProject(id).then((p) => p.repositories.map((r) => r.id));
      // A failed read is tried again at send time, not kept.
      found.catch(() => repos.current.delete(id));
      repos.current.set(id, found);
    }
    return found;
  };

  // A starter's words land with the caret at their end, once they are in the field.
  const [caretToEnd, setCaretToEnd] = useState(0);
  useEffect(() => {
    if (!caretToEnd) return;
    const area = wrap.current?.querySelector("textarea");
    area?.setSelectionRange(area.value.length, area.value.length);
  }, [caretToEnd]);
  const fill = (starter: Starter) => {
    setText(starter.prompt);
    // Focus moves now, so what is typed next goes to the field and not to the pill.
    wrap.current?.querySelector("textarea")?.focus();
    setCaretToEnd((n) => n + 1);
  };

  const send = async (message: string): Promise<boolean> => {
    // The composer refuses a second Enter while busy; this also covers a send it did not start.
    if (sending.current) return false;
    sending.current = true;
    setProblem(null);
    try {
      const links: SessionLink[] = await Promise.all(linked.map(async (p) => ({ projectId: p.id, repositoryIds: await reposOf(p.id) })));
      const { id } = await client.createSession({ message, projects: links });
      onCreated(id);
      return true;
    } catch (err) {
      setProblem(`Could not start the session: ${errorText(err)}`);
      return false;
    } finally {
      sending.current = false;
    }
  };

  const shown = (sessions ?? []).slice(0, RECENT_SESSIONS_SHOWN[density]);
  const clock = now ? now() : new Date();
  const options: LinkableProject[] = projects.map((p) => ({ id: p.id, name: p.name, imageUrl: p.imageUrl, colorSlot: p.colorSlot }));
  return (
    <div className="screen welcomeScreen" data-testid="welcome">
      <Welcome
        mark={<DudeMark size="fill" />}
        greeting={name ? `${partOfDay(clock)}, ${firstName(name)}` : partOfDay(clock)}
        line="What are we working out today?"
        composer={
          <div ref={wrap} className="welcomeComposer">
            <ChatComposer
              mode="chat"
              variant="stage"
              autoFocus
              value={text}
              onValueChange={setText}
              placeholder="Start a session: an idea, a question, a plan…"
              to={<>To <b>Brainstorm</b></>}
              leading={<ComposerLinks linked={linked} projects={options}
                onLink={(p) => {
                  void reposOf(p.id).catch(() => undefined);
                  setLinked((l) => [...l, p]);
                }}
                onUnlink={(p) => setLinked((l) => l.filter((x) => x.id !== p.id))} />}
              onSubmit={({ text: t }) => send(t)}
              data-testid="welcome-composer"
            />
            {problem ? <Callout tone="danger" data-testid="welcome-problem">{problem}</Callout> : null}
          </div>
        }
        starters={<StarterPills starters={WELCOME_STARTERS} onPick={fill} />}
        footer={<>
          {sessions === null ? null : shown.length === 0 ? <WelcomeNote>{WELCOME_FIRST_TIME}</WelcomeNote> : (
            <RecentSessions onOpen={onOpenSession} onAll={onAllSessions} sessions={shown.map((s) => ({
              id: s.id,
              title: sessionTitle(s),
              summary: s.filed > 0 ? `Filed ${s.filed}` : "Nothing filed yet",
              age: <Age at={s.lastActivityAt} />,
              ...(s.shared ? { shared: { owner: s.role === "owner" ? null : s.owner } } : {}),
            }))} />
          )}
          {offer ? <div className="welcomeOffer" data-testid="welcome-offer">{offer}</div> : null}
        </>}
      />
    </div>
  );
}

/** How long ago, in one coarse unit: "4h ago". */
function Age({ at }: { at: string }) {
  const [now] = useState(() => Date.now());
  return <><Duration ms={Math.max(0, now - Date.parse(at))} format="age" tone="muted" /> ago</>;
}
