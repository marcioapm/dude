/**
 * Your brainstorm sessions: those you are in, newest activity first, and
 * starting a new one (a title, and what it reads).
 */

import { useState } from "react";
import { type NavProject } from "@dude/design-system";
import { ScreenHeader, SessionRow } from "@dude/design-system/components";
import { Button, EmptyState, Input } from "@dude/design-system/primitives";
import type { SessionSummary } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { LinkDialog } from "./SessionDialogs.tsx";

/** What the session's agent is doing, as its row says it. */
export function sessionState(s: Pick<SessionSummary, "runStatus" | "dudePause">): string {
  if (!s.runStatus) return "Not started";
  if (s.runStatus === "paused") return s.dudePause ? "Parked" : "Paused";
  if (["completed", "failed", "aborted"].includes(s.runStatus)) return "Resting";
  return "Talking";
}

export function SessionsScreen({ client, sessions, projects, onOpen }: {
  client: ApiClient;
  sessions: readonly SessionSummary[] | null;
  projects: readonly NavProject[];
  onOpen: (id: string) => void;
}) {
  const [creating, setCreating] = useState(false);
  return (
    <div className="screen" data-testid="sessions">
      <ScreenHeader title="Sessions"
        actions={<Button size="sm" variant="primary" onClick={() => setCreating(true)} data-testid="new-session">New session</Button>} />
      <div className="screenBody narrow">
        {sessions && sessions.length === 0 ? (
          <EmptyState icon="brainstorm" title="No sessions yet"
            description="A session is a conversation with an agent that reads the projects you link and proposes work. It changes nothing; you file what it proposes." />
        ) : (
          <ul className="sessionList">
            {(sessions ?? []).map((s) => (
              <SessionRow key={s.id} data-testid="session-row" data-session={s.id} title={s.title}
                summary={s.filed > 0 ? `Filed ${s.filed}` : "Nothing filed yet"}
                projects={s.projects.map((p) => ({ key: p.key, name: p.name, repositories: p.repositories.length }))}
                state={sessionState(s)}
                shared={s.shared ? { owner: s.role === "owner" ? undefined : s.owner } : undefined}
                onOpen={() => onOpen(s.id)} />
            ))}
          </ul>
        )}
      </div>
      <NewSessionDialog client={client} projects={projects} open={creating} onClose={() => setCreating(false)} onCreated={onOpen} />
    </div>
  );
}

/** A new session's links before any are picked: one array, so the dialog keeps its picks across renders. */
const NOTHING_LINKED: SessionSummary["projects"] = [];

/** A new session: its title first, then the projects it reads. */
export function NewSessionDialog({ client, projects, open, onClose, onCreated }: {
  client: ApiClient;
  projects: readonly NavProject[];
  open: boolean;
  onClose: () => void;
  onCreated: (id: string) => void;
}) {
  const [title, setTitle] = useState("");
  const named = title.trim();
  return (
    <LinkDialog client={client} projects={projects} linked={NOTHING_LINKED} open={open} onClose={() => {
      setTitle("");
      onClose();
    }} title="New session" saveLabel="Start"
      onSave={async (links) => {
        if (!named) throw new Error("Name the session first.");
        const { id } = await client.createSession(named, links);
        setTitle("");
        onCreated(id);
      }}
      lead={<Input label="Title" value={title} onChange={(e) => setTitle(e.target.value)} autoFocus data-testid="session-title"
        placeholder="What you want to think through" />} />
  );
}
