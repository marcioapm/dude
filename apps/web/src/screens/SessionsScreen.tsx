/**
 * Your brainstorm sessions: those you are in, newest activity first. New
 * session goes to the welcome, where a session is made by its first message.
 * Archived shows the sessions you archived, read when it is picked; one is
 * unarchived from its own page.
 */

import { useEffect, useState } from "react";
import { ScreenHeader, Segmented, SessionRow } from "@dude/design-system/components";
import { Button, Callout, EmptyState, Spinner } from "@dude/design-system/primitives";
import { sessionTitle, type SessionSummary } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "../hooks/useSave.tsx";

/** What the session's agent is doing, as its row says it. */
export function sessionState(s: Pick<SessionSummary, "runStatus" | "dudePause">): string {
  if (!s.runStatus) return "Not started";
  if (s.runStatus === "paused") return s.dudePause ? "Parked" : "Paused";
  if (["completed", "failed", "aborted"].includes(s.runStatus)) return "Resting";
  return "Talking";
}

type Shown = "yours" | "archived";

export function SessionsScreen({ client, sessions, onOpen, onNew }: {
  client: ApiClient;
  /** Yours not archived: the sidebar's list. */
  sessions: readonly SessionSummary[] | null;
  onOpen: (id: string) => void;
  /** New session: the welcome. */
  onNew: () => void;
}) {
  const [shown, setShown] = useState<Shown>("yours");
  const [archived, setArchived] = useState<readonly SessionSummary[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  useEffect(() => {
    if (shown !== "archived") return;
    let current = true;
    setArchived(null);
    setProblem(null);
    client.sessions({ archived: true }).then(
      (list) => current && setArchived(list.sessions.filter((s) => s.archived)),
      (err: unknown) => current && setProblem(`Could not read your archived sessions: ${errorText(err)}`),
    );
    return () => {
      current = false;
    };
  }, [client, shown]);

  const row = (s: SessionSummary) => (
    <SessionRow key={s.id} data-testid="session-row" data-session={s.id} title={sessionTitle(s)}
      summary={s.filed > 0 ? `Filed ${s.filed}` : "Nothing filed yet"}
      projects={s.projects.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories.length }))}
      state={sessionState(s)}
      shared={s.shared ? { owner: s.role === "owner" ? undefined : s.owner } : undefined}
      onOpen={() => onOpen(s.id)} />
  );
  let body;
  if (shown === "archived") {
    body = problem ? <Callout tone="danger" data-testid="sessions-problem">{problem}</Callout>
      : !archived ? <div className="centered"><Spinner label="Reading your archived sessions…" /></div>
      : archived.length === 0 ? (
        <EmptyState icon="archive" title="Nothing archived"
          description="A session you archive leaves your list and sidebar, and nobody else's. Open one and Unarchive brings it back." />
      ) : <ul className="sessionList">{archived.map(row)}</ul>;
  } else if (sessions && sessions.length === 0) {
    body = (
      <EmptyState icon="brainstorm" title="No sessions yet"
        description="A session is a conversation with an agent that reads the projects you link and proposes work. It changes nothing; you file what it proposes." />
    );
  } else {
    body = <ul className="sessionList">{(sessions ?? []).map(row)}</ul>;
  }
  return (
    <div className="screen" data-testid="sessions" data-shown={shown}>
      <ScreenHeader title="Sessions"
        actions={<>
          <Segmented<Shown> size="sm" label="Which sessions" value={shown} onChange={setShown} data-testid="sessions-shown"
            options={[{ value: "yours", label: "Yours" }, { value: "archived", label: "Archived" }]} />
          <Button size="sm" variant="primary" onClick={onNew} data-testid="new-session">New session</Button>
        </>} />
      <div className="screenBody narrow">{body}</div>
    </div>
  );
}
