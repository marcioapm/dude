/**
 * Open to everyone: every session open to the whole organisation, in two
 * groups — those you joined (added, or you wrote in one) and are active
 * in, which is the sidebar's count, then those you have not. Writing in
 * one joins you to it, and it then shows in your Sessions too.
 */

import { ScreenHeader, SessionRow } from "@dude/design-system/components";
import { EmptyState, Spinner } from "@dude/design-system/primitives";
import { sessionTitle, type OpenSessionSummary, type OpenSessionsList } from "@dude/domain";
import { sessionState } from "./SessionsScreen.tsx";

export function OpenSessionsScreen({ list, org, onOpen }: {
  /** null until read. */
  list: OpenSessionsList | null;
  org: string;
  onOpen: (id: string) => void;
}) {
  const row = (s: OpenSessionSummary) => (
    <SessionRow key={s.id} data-testid="open-session-row" data-session={s.id} data-joined={s.joined ? "true" : "false"} title={sessionTitle(s)}
      summary={s.ownedByAdmins ? `Owner left ${org} · owned by ${org}'s admins` : `${s.people} ${s.people === 1 ? "person" : "people"} in it`}
      projects={s.projects.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories.length }))}
      state={sessionState({ runStatus: s.runStatus, dudePause: null })}
      access="organisation" org={org} onOpen={() => onOpen(s.id)} />
  );
  let body;
  if (!list) {
    body = <div className="centered"><Spinner label="Reading the sessions open to everyone…" /></div>;
  } else if (list.sessions.length === 0) {
    body = <EmptyState icon="building" title="Nothing open to everyone yet"
      description={`An owner can open a session to everyone in ${org} from its Share dialog. Everyone can then read it and chat.`} />;
  } else {
    const joined = list.sessions.filter((s) => s.joined);
    const not = list.sessions.filter((s) => !s.joined);
    body = (
      <>
        <p className="muted">Sessions anyone in {org} can open and chat in, without being added. Writing in one joins you to it; it then shows in your Sessions too.</p>
        <section aria-labelledby="open-joined" data-testid="open-joined">
          <h2 id="open-joined" className="ds-label">Joined · active <span className="muted">{joined.length} · the sidebar's count</span></h2>
          {joined.length > 0 ? <ul className="sessionList">{joined.map(row)}</ul> : <p className="muted">None yet.</p>}
        </section>
        <section aria-labelledby="open-not-joined" data-testid="open-not-joined">
          <h2 id="open-not-joined" className="ds-label">Not joined <span className="muted">{not.length} · open them to read; write to join</span></h2>
          {not.length > 0 ? <ul className="sessionList">{not.map(row)}</ul> : <p className="muted">You are in every one.</p>}
        </section>
      </>
    );
  }
  return (
    <div className="screen" data-testid="open-sessions">
      <ScreenHeader title="Open to everyone" />
      <div className="screenBody narrow">{body}</div>
    </div>
  );
}
