/**
 * Your brainstorm sessions: those you are in, newest activity first. New
 * session goes to the welcome, where a session is made by its first message.
 */

import { ScreenHeader, SessionRow } from "@dude/design-system/components";
import { Button, EmptyState } from "@dude/design-system/primitives";
import { sessionTitle, type SessionSummary } from "@dude/domain";

/** What the session's agent is doing, as its row says it. */
export function sessionState(s: Pick<SessionSummary, "runStatus" | "dudePause">): string {
  if (!s.runStatus) return "Not started";
  if (s.runStatus === "paused") return s.dudePause ? "Parked" : "Paused";
  if (["completed", "failed", "aborted"].includes(s.runStatus)) return "Resting";
  return "Talking";
}

export function SessionsScreen({ sessions, onOpen, onNew }: {
  sessions: readonly SessionSummary[] | null;
  onOpen: (id: string) => void;
  /** New session: the welcome. */
  onNew: () => void;
}) {
  return (
    <div className="screen" data-testid="sessions">
      <ScreenHeader title="Sessions"
        actions={<Button size="sm" variant="primary" onClick={onNew} data-testid="new-session">New session</Button>} />
      <div className="screenBody narrow">
        {sessions && sessions.length === 0 ? (
          <EmptyState icon="brainstorm" title="No sessions yet"
            description="A session is a conversation with an agent that reads the projects you link and proposes work. It changes nothing; you file what it proposes." />
        ) : (
          <ul className="sessionList">
            {(sessions ?? []).map((s) => (
              <SessionRow key={s.id} data-testid="session-row" data-session={s.id} title={sessionTitle(s)}
                summary={s.filed > 0 ? `Filed ${s.filed}` : "Nothing filed yet"}
                projects={s.projects.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories.length }))}
                state={sessionState(s)}
                shared={s.shared ? { owner: s.role === "owner" ? undefined : s.owner } : undefined}
                onOpen={() => onOpen(s.id)} />
            ))}
          </ul>
        )}
      </div>
    </div>
  );
}
