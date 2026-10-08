/**
 * Your brainstorm sessions: those you are in, newest activity first, and
 * starting a new one — at once, untitled and linked to nothing: its agent
 * names it, and its owner links projects from its rail.
 */

import { useState } from "react";
import { ScreenHeader, SessionRow } from "@dude/design-system/components";
import { Button, EmptyState, useToast } from "@dude/design-system/primitives";
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

/**
 * New session: made at once, with no dialog, and opened. Pressing it twice
 * while the first is on its way makes one.
 */
export function useNewSession(client: ApiClient, onCreated: (id: string) => void): { start: () => void; busy: boolean } {
  const { toast } = useToast();
  const [busy, setBusy] = useState(false);
  const start = () => {
    if (busy) return;
    setBusy(true);
    void client.createSession().then(({ id }) => onCreated(id),
      (err: unknown) => toast({ title: `Could not start a session: ${errorText(err)}`, tone: "danger" }))
      .finally(() => setBusy(false));
  };
  return { start, busy };
}

export function SessionsScreen({ client, sessions, onOpen }: {
  client: ApiClient;
  sessions: readonly SessionSummary[] | null;
  onOpen: (id: string) => void;
}) {
  const fresh = useNewSession(client, onOpen);
  return (
    <div className="screen" data-testid="sessions">
      <ScreenHeader title="Sessions"
        actions={<Button size="sm" variant="primary" disabled={fresh.busy} onClick={fresh.start} data-testid="new-session">New session</Button>} />
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
