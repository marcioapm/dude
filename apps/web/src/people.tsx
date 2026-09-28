/**
 * The organisation's people, as the screens need them: who is signed in
 * (`me`: the profile band, your settings, whose asks are yours), a name
 * and face for every id the ledger records as an actor, and who is online.
 *
 * Read once for the shell, again each minute — `online` lapses by the
 * clock, five minutes after someone's last request — and on a live
 * `person.seen`, which the shell's event stream hands to `seen`.
 */

import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { formatDuration } from "@dude/design-system";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import type { ApiClient, Member } from "./api/client.ts";

/** A person as the screens draw them: `/v1/people`'s. */
export type Someone = Member;

export interface People {
  /** The signed-in person's id, once known. */
  you: string | null;
  /** The signed-in person, once known. */
  me: Someone | null;
  all: readonly Someone[];
  byId: ReadonlyMap<string, Someone>;
  /** Names by id, for signing what people did. */
  names: ReadonlyMap<string, string>;
  /**
   * Read the people again, and resolve with them: after a change to them,
   * or for a name the screen met that was not there when the page loaded.
   */
  refresh: () => Promise<People>;
  /** A `person.seen` from the live stream: that person is here now. True when it was one. */
  seen: (event: PersistedEvent) => boolean;
}

const EVERY_MS = 60_000;
const EMPTY: People = { you: null, me: null, all: [], byId: new Map(), names: new Map(), refresh: async () => EMPTY, seen: () => false };
const PeopleContext = createContext<People>(EMPTY);

type Listed = { people: Someone[]; you: string };

export function PeopleProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  const [listed, setListed] = useState<Listed | null>(null);
  const latest = useRef<Listed | null>(null);
  latest.current = listed;

  const read = useCallback(async (): Promise<Listed | null> => {
    try {
      const fresh = await client.listPeople();
      setListed(fresh);
      return fresh;
    } catch {
      // Failing to learn names only leaves acts unsigned; the screens say so.
      return latest.current;
    }
  }, [client]);

  const seen = useCallback((event: PersistedEvent): boolean => {
    if (event.eventType !== EventTypes.PersonSeen) return false;
    const person = event.payload["person"] as Partial<Someone> & { id: string; where?: string | null };
    if (!latest.current?.people.some((p) => p.id === person.id)) {
      // Someone new: read the list again rather than guess their details.
      void read();
      return true;
    }
    setListed((l) => l && {
      ...l,
      people: l.people.map((p) => (p.id === person.id
        ? { ...p, ...person, online: true, lastSeenAt: event.occurredAt, lastSeenWhere: person.where ?? p.lastSeenWhere }
        : p)),
    });
    return true;
  }, [read]);

  const refresh = useCallback(async (): Promise<People> => {
    const fresh = await read();
    return fresh ? build(fresh, refresh, seen) : EMPTY;
  }, [read, seen]);

  useEffect(() => {
    void read();
    const timer = setInterval(() => void read(), EVERY_MS);
    return () => clearInterval(timer);
  }, [read]);

  const value = useMemo<People>(() => (listed ? build(listed, refresh, seen) : { ...EMPTY, refresh, seen }), [listed, refresh, seen]);
  return <PeopleContext.Provider value={value}>{children}</PeopleContext.Provider>;
}

function build(p: Listed, refresh: People["refresh"], seen: People["seen"]): People {
  const byId = new Map(p.people.map((x) => [x.id, x]));
  return { you: p.you, me: byId.get(p.you) ?? null, all: p.people, byId, names: new Map(p.people.map((x) => [x.id, x.name])), refresh, seen };
}

export function usePeople(): People {
  return useContext(PeopleContext);
}

/** "on TEXT-14 · 2m ago": where someone was, and when, for their face's tooltip. */
export function whereWords(person: Pick<Someone, "lastSeenAt" | "lastSeenWhere">, now = Date.now()): string | undefined {
  if (!person.lastSeenAt) return undefined;
  const ms = now - Date.parse(person.lastSeenAt);
  const when = ms < 60_000 ? "just now" : `${formatDuration(ms, { style: "age" })} ago`;
  return person.lastSeenWhere ? `on ${person.lastSeenWhere} · ${when}` : when;
}

export { firstName } from "@dude/design-system";
