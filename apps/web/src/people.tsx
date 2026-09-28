/**
 * The organisation's people, as the screens need them: who is signed in,
 * and a name (and face, once people have them) for every id the ledger
 * records as an actor. Read once for the shell; a steer, an answer or an
 * abort is then signed with a name instead of "a person".
 *
 * Today a person is a user API key (`GET /v1/people`: `{ id, name }`);
 * when the people work lands the same route carries photos and presence,
 * and they flow through untouched.
 */

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import type { ApiClient, Person } from "./api/client.ts";

/** A person as the screens draw them: a name, and a photo and presence when the API has them. */
export interface Someone extends Person {
  photoUrl?: string | null;
  online?: boolean;
}

export interface People {
  /** The signed-in person's id, once known. */
  you: string | null;
  all: readonly Someone[];
  byId: ReadonlyMap<string, Someone>;
  /** Names by id, for signing what people did. */
  names: ReadonlyMap<string, string>;
  /**
   * Read the people again, and resolve with them: for a name the screen
   * met that was not there when the page loaded (someone just joined).
   */
  refresh: () => Promise<People>;
}

const EMPTY: People = { you: null, all: [], byId: new Map(), names: new Map(), refresh: async () => EMPTY };
const PeopleContext = createContext<People>(EMPTY);

export function PeopleProvider({ client, children }: { client: ApiClient; children: ReactNode }) {
  const [people, setPeople] = useState<{ people: Someone[]; you: string } | null>(null);
  useEffect(() => {
    let current = true;
    // Failing to learn names only leaves acts unsigned; the screens say so.
    void client.listPeople().then((p) => current && setPeople(p), () => {});
    return () => {
      current = false;
    };
  }, [client]);
  const refresh = useCallback(async (): Promise<People> => {
    try {
      const fresh = await client.listPeople();
      setPeople(fresh);
      return build(fresh, refresh);
    } catch {
      return people ? build(people, refresh) : EMPTY;
    }
  }, [client, people]);
  const value = useMemo<People>(() => (people ? build(people, refresh) : { ...EMPTY, refresh }), [people, refresh]);
  return <PeopleContext.Provider value={value}>{children}</PeopleContext.Provider>;
}

function build(p: { people: Someone[]; you: string }, refresh: People["refresh"]): People {
  const byId = new Map(p.people.map((x) => [x.id, x]));
  return { you: p.you, all: p.people, byId, names: new Map(p.people.map((x) => [x.id, x.name])), refresh };
}

export function usePeople(): People {
  return useContext(PeopleContext);
}

export { firstName } from "@dude/design-system";
