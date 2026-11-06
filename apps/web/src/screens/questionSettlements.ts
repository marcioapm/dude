import { createContext, useMemo, useState } from "react";
import { EventTypes, isConductor, type PersistedEvent, type Run } from "@dude/domain";

export type SettlementIndex = ReadonlyMap<string, readonly PersistedEvent[]>;
export const SettlementContext = createContext<SettlementIndex | undefined>(undefined);
const EMPTY: readonly PersistedEvent[] = [];

/** Reconcile only against the current API ledger: removed settlements are not retained. */
export function settlementIndex(taskId: string, runs: readonly Run[], events: readonly PersistedEvent[], previous?: SettlementIndex): SettlementIndex {
  const conductors = new Set(runs.filter((r) => r.taskId === taskId && isConductor(r)).map((r) => r.id));
  const buckets = new Map<string, PersistedEvent[]>();
  for (const e of events) {
    if (e.taskId !== taskId || !e.runId || !conductors.has(e.runId) ||
      (e.eventType !== EventTypes.QuestionAnswered && e.eventType !== EventTypes.QuestionClosed) || typeof e.payload.questionId !== "string") continue;
    const bucket = buckets.get(e.payload.questionId);
    if (bucket) bucket.push(e);
    else buckets.set(e.payload.questionId, [e]);
  }
  const out = new Map<string, readonly PersistedEvent[]>();
  for (const [id, bucket] of buckets) {
    const old = previous?.get(id);
    // Compare content, not just eventId: reloads can correct an actor or answer in place.
    out.set(id, old && JSON.stringify(old) === JSON.stringify(bucket) ? old : bucket);
  }
  return previous && previous.size === out.size && [...out].every(([id, bucket]) => previous.get(id) === bucket) ? previous : out;
}

export function useSettlementIndex(taskId: string, runs: readonly Run[], events: readonly PersistedEvent[]): SettlementIndex {
  const [cached, setCached] = useState(() => ({ taskId, runs, events, index: settlementIndex(taskId, runs, events) }));
  if (cached.taskId === taskId && cached.runs === runs && cached.events === events) return cached.index;
  const index = settlementIndex(taskId, runs, events, cached.taskId === taskId ? cached.index : undefined);
  setCached({ taskId, runs, events, index });
  return index;
}

export function ledgerQuestionIds(events: readonly PersistedEvent[]) {
  return {
    asked: new Set(events.filter((e) => e.eventType === EventTypes.QuestionAsked).map((e) => e.payload.questionId).filter((id): id is string => typeof id === "string")),
    seen: new Set(events.map((e) => e.eventId)),
  };
}

export function ledgerSettlements(ids: ReturnType<typeof ledgerQuestionIds>, index?: SettlementIndex, previous: readonly PersistedEvent[] = EMPTY): readonly PersistedEvent[] {
  const selected: PersistedEvent[] = [];
  for (const id of ids.asked) for (const e of index?.get(id) ?? EMPTY) if (!ids.seen.has(e.eventId)) selected.push(e);
  return selected.length === previous.length && selected.every((e, i) => e === previous[i]) ? previous : selected;
}

export function useLedgerSettlements(events: readonly PersistedEvent[] | null, index?: SettlementIndex): readonly PersistedEvent[] {
  const ids = useMemo(() => ledgerQuestionIds(events ?? EMPTY), [events]);
  const [cached, setCached] = useState(() => ({ ids, index, selected: ledgerSettlements(ids, index) }));
  if (cached.ids === ids && cached.index === index) return cached.selected;
  const selected = ledgerSettlements(ids, index, cached.selected);
  setCached({ ids, index, selected });
  return selected;
}
