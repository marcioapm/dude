/**
 * Open to everyone: every session open to the whole organisation. Active
 * ones come in two groups — those you joined (added, or you wrote in one)
 * and are active in, which is the sidebar's count, then those you have
 * not; Archived by owner lists those whose owner archived them. Writing in
 * one joins you to it, and it then shows in your Sessions too. Read when
 * the screen opens; the sidebar has only the numbers.
 */

import { useEffect, useState } from "react";
import { ScreenHeader, Segmented, SessionRow } from "@dude/design-system/components";
import { Callout, EmptyState, Page, Section, Spinner } from "@dude/design-system/primitives";
import { sessionTitle, type OpenSessionSummary, type OpenSessionsList } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";
import { sessionState } from "./SessionsScreen.tsx";

type Shown = "active" | "archived";

export function OpenSessionsScreen({ client, org, onOpen }: {
  client: ApiClient;
  org: string;
  onOpen: (id: string) => void;
}) {
  const [list, setList] = useState<OpenSessionsList | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [shown, setShown] = useState<Shown>("active");
  useEffect(() => {
    let current = true;
    client.openSessions().then(
      (l) => current && setList(l),
      (err: unknown) => current && setProblem(`Could not read the sessions open to everyone: ${errorText(err)}`),
    );
    return () => {
      current = false;
    };
  }, [client]);

  const row = (s: OpenSessionSummary) => (
    <SessionRow key={s.id} data-testid="open-session-row" data-session={s.id} data-joined={s.joined ? "true" : "false"} title={sessionTitle(s)}
      summary={s.ownedByAdmins ? `Owner left ${org} · owned by ${org}'s admins` : `${s.people} ${s.people === 1 ? "person" : "people"} in it`}
      projects={s.projects.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories.length }))}
      state={sessionState({ runStatus: s.runStatus, dudePause: null })}
      shared={{ owner: s.ownedByAdmins ? undefined : s.owner ?? undefined }}
      access="organisation" org={org} onOpen={() => onOpen(s.id)} />
  );
  const active = list?.sessions.filter((s) => !s.archivedByOwner) ?? [];
  const archived = list?.sessions.filter((s) => s.archivedByOwner) ?? [];
  let body;
  if (problem) {
    body = <Callout tone="danger" data-testid="open-sessions-problem">{problem}</Callout>;
  } else if (!list) {
    body = <div className="centered"><Spinner label="Reading the sessions open to everyone…" /></div>;
  } else if (list.sessions.length === 0) {
    body = <EmptyState icon="building" title="Nothing open to everyone yet"
      description={`An owner can open a session to everyone in ${org} from its Share dialog. Everyone can then read it and chat.`} />;
  } else if (shown === "archived") {
    body = archived.length > 0 ? <ul className="sessionList" data-testid="open-archived">{archived.map(row)}</ul>
      : <EmptyState icon="archive" title="Nothing archived by its owner" description="A session its owner archives stays open to everyone, listed here." />;
  } else {
    const joined = active.filter((s) => s.joined);
    const not = active.filter((s) => !s.joined);
    body = (
      <Page width="full">
        <p className="muted">Sessions anyone in {org} can open and chat in, without being added. Writing in one joins you to it; it then shows in your Sessions too.</p>
        <Section title="Joined · active" count={joined.length} note="the sidebar's count" data-testid="open-joined">
          {joined.length > 0 ? <ul className="sessionList">{joined.map(row)}</ul> : <p className="muted">None yet.</p>}
        </Section>
        <Section title="Not joined" count={not.length} note="open them to read; write to join" data-testid="open-not-joined">
          {not.length > 0 ? <ul className="sessionList">{not.map(row)}</ul> : <p className="muted">You are in every one.</p>}
        </Section>
      </Page>
    );
  }
  return (
    <div className="screen" data-testid="open-sessions" data-shown={shown}>
      <ScreenHeader title="Open to everyone"
        actions={<Segmented<Shown> size="toolbar" label="Which open sessions" value={shown} onChange={setShown} data-testid="open-sessions-shown"
          options={[{ value: "active", label: `Active · ${active.length}` }, { value: "archived", label: "Archived by owner" }]} />} />
      <div className="screenBody narrow">{body}</div>
    </div>
  );
}
