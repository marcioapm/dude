/**
 * A conflict, said calmly. The orchestrator answers 409 when a Run moved
 * on while someone was deciding: it was already paused, already ended, the
 * question was already answered. Usually because someone else acted — and
 * then the useful thing is to say who, not to show an error.
 *
 * The ledger has who: the latest act by a person on the Run, other than
 * you, is the likely one. Without one the server's words stand.
 */

import type { PersistedEvent, RunStatus } from "@dude/domain";
import { actorName, humanActor } from "./api/conversation.ts";
import type { People } from "./people.tsx";

export interface Notice {
  text: string;
  /** Who acted first, when known. */
  by: string | null;
}

/** What a person's act on a Run was, as a verb phrase. */
const ACTED: Record<string, string> = {
  "run.paused": "paused it",
  "run.resumed": "resumed it",
  "run.aborted": "aborted it",
  "run.steered": "steered it",
  "question.answered": "answered it",
  "repository.approved": "approved the request",
  "repository.denied": "declined the request",
};

/** What the Run is now, as the end of a sentence. */
const NOW: Partial<Record<RunStatus, string>> = {
  paused: "It is paused.",
  running: "It is running.",
  completed: "It has finished.",
  failed: "It has failed.",
  aborted: "It was aborted.",
};

export function conflictNotice(
  attempted: string,
  serverMessage: string,
  events: readonly PersistedEvent[],
  people: Pick<People, "you" | "names">,
  status?: RunStatus,
): Notice {
  for (let i = events.length - 1; i >= 0; i--) {
    const event = events[i]!;
    const verb = ACTED[event.eventType];
    const by = humanActor(event);
    if (!verb || !by || by.id === people.you) continue;
    const name = actorName(by, people.names) ?? "Someone else";
    return { text: `${name} ${verb} first, so you did not ${attempted}. ${NOW[status ?? "running"] ?? ""}`.trim(), by: name };
  }
  return { text: `Could not ${attempted}: ${serverMessage}`, by: null };
}
