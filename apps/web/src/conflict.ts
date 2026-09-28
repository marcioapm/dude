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

/**
 * What a person's act on a Run was, as a verb phrase, and the status it
 * leaves the Run in when that is what it is about: a pause explains a
 * paused Run, not a finished one.
 */
const ACTED: Record<string, { verb: string; leaves?: RunStatus }> = {
  "run.paused": { verb: "paused it", leaves: "paused" },
  "run.resumed": { verb: "resumed it", leaves: "running" },
  "run.aborted": { verb: "aborted it", leaves: "aborted" },
  "question.answered": { verb: "answered it" },
  "repository.approved": { verb: "approved the request" },
  "repository.denied": { verb: "declined the request" },
};

/** What the Run is now, as the end of a sentence. */
const NOW: Partial<Record<RunStatus, string>> = {
  paused: "It is paused.",
  running: "It is running.",
  completed: "It has finished.",
  failed: "It has failed.",
  aborted: "It was aborted.",
};

/**
 * `events` are what the page had not seen when it acted: the one who acted
 * first is among them. Without knowing who you are, no one is named — it
 * could be you, in another tab.
 */
export function conflictNotice(
  attempted: string,
  serverMessage: string,
  events: readonly PersistedEvent[],
  people: Pick<People, "you" | "names">,
  status?: RunStatus,
): Notice {
  if (people.you) {
    for (let i = events.length - 1; i >= 0; i--) {
      const event = events[i]!;
      const act = ACTED[event.eventType];
      const by = humanActor(event);
      if (!act || !by || by.id === people.you) continue;
      if (act.leaves && status && act.leaves !== status) continue;
      const name = actorName(by, people.names) ?? "Someone else";
      return { text: `${name} ${act.verb} first, so you did not ${attempted}. ${NOW[status ?? "running"] ?? ""}`.trim(), by: name };
    }
  }
  return { text: `Could not ${attempted}: ${serverMessage}`, by: null };
}
