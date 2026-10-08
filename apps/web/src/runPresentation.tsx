import type { PersistedEvent } from "@dude/domain";
import type { Run } from "./api/client.ts";
import { actorName, humanActor } from "./api/conversation.ts";
import type { People } from "./people.tsx";
import { formatPlace, inTree } from "./place.ts";

export function runStatusLabel(run: Pick<Run, "replacedBy">): string | undefined {
  return run.replacedBy ? "Restarted" : undefined;
}

export function RunReplacement({ run }: { run: Pick<Run, "replacedBy"> }) {
  return run.replacedBy ? <a href={formatPlace(inTree({ kind: "session", id: run.replacedBy }))}>Open replacement Run</a> : null;
}

export function runRestartedText(run: Run, events: readonly PersistedEvent[], people: People): string | null {
  const label = runStatusLabel(run);
  if (!label) return null;
  const event = events.findLast((e) => e.eventType === "run.restarted" && e.runId === run.id && e.payload.to === run.replacedBy);
  const who = event?.payload.by === "conductor" || event?.actor.type === "agent" ? "the conductor"
    : event ? actorName(humanActor(event), people.names) : null;
  const note = typeof event?.payload.note === "string" ? event.payload.note.trim() : "";
  return `${label}${who ? ` by ${who}` : ""}${note ? `: ${note}` : "."}`;
}
