/*
 * The proposed fold, built on the app's own (api/conversation.ts `apply`)
 * so that everything else is today's, event for event. Three changes:
 *
 *   1. A move's notice ("The implementer moved to Large …") is held while
 *      the move's row is live and put down when the agent is up, as a
 *      "Taken back up" notice is (heldUnpark). Its resume's timing, matched
 *      by the epoch run.resized records (machine.sinceEpoch), gives it the
 *      whole move's time and its stages in the hover: no "Resumed in …"
 *      row of its own.
 *   2. A move lux refused keeps its refusal notice; its resume's timing
 *      (cause "machine", after machine.refused) says nothing.
 *   3. `run.told` (proposed) is dude's words to the agent at that resume:
 *      a Told turn, put down just after the move's notice, before the
 *      agent's first words.
 */

import { EventTypes, START_STAGE_WORDS, type PersistedEvent, type RunStatus } from "@dude/domain";
import { formatDuration } from "@dude/design-system";
import { apply, emptyProjection, resumeTiming, snapshot, type Conversation, type NoticeTurn, type Projection, type Turn } from "../../src/api/conversation.ts";
import { RUN_TOLD } from "./events.ts";

/** What dude put in front of the agent's input: shown as ChatMessage intent="told". */
export interface ToldTurn {
  kind: "told";
  id: string;
  text: string;
  at: string;
}

export type ShownTurn = Turn | ToldTurn;

/** Today's fold, unchanged: run.told is a type it does not know, and skips. */
export function today(events: readonly PersistedEvent[], status: RunStatus): Conversation {
  return snapshot(apply(emptyProjection(), events), status);
}

/** The proposed fold: today's, with the three changes above. */
export function proposed(events: readonly PersistedEvent[], status: RunStatus): { conversation: Conversation; turns: ShownTurn[] } {
  const state: Projection = emptyProjection();
  const turns = state.turns as ShownTurn[];
  // Move notices waiting for their timing, by the epoch the move resumed into.
  const moveNotices = new Map<number, { turn: NoticeTurn; words: string }>();
  let refusedMove = false;
  let told: ToldTurn | null = null;

  for (const event of events) {
    const payload = event.payload ?? {};
    if (event.eventType === RUN_TOLD) {
      told = { kind: "told", id: event.eventId, text: String(payload.text ?? ""), at: event.occurredAt };
      state.cursor = event.cursor;
      continue;
    }
    if (event.eventType === EventTypes.RunResumeTimed && typeof payload.epoch === "number") {
      const timing = resumeTiming(payload);
      const move = moveNotices.get(payload.epoch);
      if (timing && move) {
        // The move's own line takes the whole time, from the agent's call (or the Reset) to its first words.
        move.turn.text = `${move.words} in ${formatDuration(payload.totalMs as number)}, with its conversation and work.`;
        // A move's `react` is its stop: from the call (or the Reset) to dude asking lux for the sized resume.
        move.turn.title = timing.title.replace(/^dude asked lux /, `${START_STAGE_WORDS.park.done} `);
        moveNotices.delete(payload.epoch);
        state.cursor = event.cursor;
        continue;
      }
      if (refusedMove && payload.cause === "machine") {
        // The refusal's notice already says where it resumed: no timing row.
        refusedMove = false;
        state.cursor = event.cursor;
        continue;
      }
    }

    const move = state.start?.machine ?? null;
    const reset = move !== null && events.some((e) => e.eventType === EventTypes.MachineReset && e.payload.moveId === move.moveId);
    const hadStart = state.start !== null;
    const before = turns.length;
    apply(state, [event]);

    if (event.eventType === EventTypes.MachineRefused) refusedMove = true;
    if (event.eventType === EventTypes.RunResized && move && turns.length > before) {
      // Today's "moved to Large in 10s" (its park only), taken back and held until the agent is up.
      const notice = turns.pop() as NoticeTurn;
      const role = notice.text.replace(/^The (.*?) moved to .*$/, "$1");
      const words = `The ${role} moved ${reset ? "back " : ""}to ${move.to}`;
      notice.text = `${words}, with its conversation and work.`;
      state.heldUnpark = notice;
      const epoch = (payload.machine as { sinceEpoch?: unknown } | undefined)?.sinceEpoch;
      if (typeof epoch === "number") moveNotices.set(epoch, { turn: notice, words });
    }
    if (told && hadStart && state.start === null && state.ran) {
      // The agent is up: dude's words go just after the move's notice, before anything the agent said, at the time it took them.
      const placed = turns.findIndex((t, i) => i >= before && t.kind === "notice" && t.notice === "unparked");
      told.at = event.occurredAt;
      turns.splice(placed >= 0 ? placed + 1 : before, 0, told);
      if (state.progressIndex !== null && state.progressIndex >= before) state.progressIndex += 1;
      told = null;
    }
  }
  const conversation = snapshot(state, status);
  return { conversation, turns: conversation.turns as ShownTurn[] };
}
