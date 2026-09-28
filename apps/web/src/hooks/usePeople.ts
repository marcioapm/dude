/**
 * You and your organization's people, for the shell: who you are (the
 * profile band, "Waiting on you"), whether you are an admin, and who is
 * online. Read once, again each minute — `online` lapses by the clock, five
 * minutes after someone's last request — and on the live `person.seen`,
 * which the shell hands to `seen`.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { formatDuration } from "@dude/design-system";
import type { Person } from "@dude/design-system/components";
import { EventTypes, type PersistedEvent } from "@dude/domain";
import type { ApiClient, Member } from "../api/client.ts";

export interface People {
  /** You; null until read. */
  me: Member | null;
  people: Member[];
  reload: () => void;
  /** A `person.seen` from the live stream: that person is here now. True when it was one. */
  seen: (event: PersistedEvent) => boolean;
}

const EVERY_MS = 60_000;

export function usePeople(client: ApiClient): People {
  const [you, setYou] = useState<string | null>(null);
  const [people, setPeople] = useState<Member[]>([]);

  const reload = useCallback(() => {
    void client.listPeople().then(
      (all) => {
        setYou(all.you);
        setPeople(all.people);
      },
      // The shell works without them: no faces, and every ask is yours.
      () => {},
    );
  }, [client]);

  useEffect(() => {
    reload();
    const timer = setInterval(reload, EVERY_MS);
    return () => clearInterval(timer);
  }, [reload]);

  const seen = useCallback((event: PersistedEvent) => {
    if (event.eventType !== EventTypes.PersonSeen) return false;
    const person = event.payload["person"] as Partial<Member> & { id: string; where?: string | null };
    setPeople((all) => {
      if (!all.some((p) => p.id === person.id)) {
        // Someone new: read the list again rather than guess their details.
        reload();
        return all;
      }
      return all.map((p) => (p.id === person.id
        ? { ...p, ...person, online: true, lastSeenAt: event.occurredAt, lastSeenWhere: person.where ?? p.lastSeenWhere }
        : p));
    });
    return true;
  }, [reload]);

  const me = useMemo(() => people.find((p) => p.id === you) ?? null, [people, you]);
  return { me, people, reload, seen };
}

/** "on TEXT-14 · 2m ago": where someone was, and when, for their face's tooltip. */
export function whereWords(person: Pick<Member, "lastSeenAt" | "lastSeenWhere">, now = Date.now()): string | undefined {
  if (!person.lastSeenAt) return undefined;
  const ms = now - Date.parse(person.lastSeenAt);
  const when = ms < 60_000 ? "just now" : `${formatDuration(ms, { style: "age" })} ago`;
  return person.lastSeenWhere ? `on ${person.lastSeenWhere} · ${when}` : when;
}

/** A person as a face: the design system's `Person` for an API one. */
export function avatarOf(person: { id: string; name: string; photoUrl: string | null }): Person & { id: string } {
  return { id: person.id, name: person.name, imageUrl: person.photoUrl ?? undefined };
}
