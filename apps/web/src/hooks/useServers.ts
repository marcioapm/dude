/**
 * What a task or a run serves: the run its servers live on, the servers,
 * the project's recipes — read from the API, and read again when the
 * caller's `version` moves (its own event stream saw a `servers.changed`)
 * or after an action here. Actions go through one `act`, so the busy
 * state and the reason for a refusal live in one place.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { TERMINAL_RUN_STATUSES, type AddServer, type RunStatus, type TaskServers } from "@dude/domain";
import type { ServersRun } from "@dude/design-system";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "./useSave.tsx";

/** Whether the run the servers live on is still going: dude's status, as a string on the wire. */
export function runIsLive(run: Pick<ServersRun, "state">): boolean {
  return !TERMINAL_RUN_STATUSES.includes(run.state as RunStatus);
}

export type ServersScope = { taskId: string; runId?: undefined } | { runId: string; taskId?: undefined };

export interface ServersState {
  data: TaskServers | null;
  problem: string | null;
  /** A name while an action on that server is in flight; "*" for one on all of them. */
  busy: string | null;
  /** Run an action, then re-read. Resolves whether it succeeded; a refusal is `problem`. */
  act: (what: string, action: () => Promise<unknown>) => Promise<boolean>;
  start: (name: string) => Promise<boolean>;
  stop: (name: string) => Promise<boolean>;
  restart: (name: string) => Promise<boolean>;
  remove: (name: string) => Promise<boolean>;
  startAll: () => Promise<boolean>;
  stopAll: () => Promise<boolean>;
  add: (input: AddServer) => Promise<boolean>;
  clearProblem: () => void;
}

export function useServers(client: ApiClient, scope: ServersScope, version = 0): ServersState {
  const [data, setData] = useState<TaskServers | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const key = scope.taskId ? `task:${scope.taskId}` : `run:${scope.runId}`;
  // Only the latest read may land: an action's re-read and an event's can
  // overlap. And one read at a time: a burst of events (a replayed history)
  // asks once more when the read in flight lands, not once per event — and
  // that once more is the newest `reload`, in case the scope moved meanwhile.
  // A caller that asked during a read waits for that once more, so an action
  // resolves with the state it left, not the one before it.
  const latest = useRef(0);
  const inFlight = useRef<Promise<void> | null>(null);
  const again = useRef<Promise<void> | null>(null);
  const newest = useRef<() => Promise<void>>(async () => undefined);

  const reload = useCallback((): Promise<void> => {
    if (inFlight.current) {
      // One follow-up read serves everyone who asks during the one in flight.
      again.current ??= inFlight.current.then(() => newest.current());
      return again.current;
    }
    const mine = ++latest.current;
    const read = (async () => {
      try {
        const fresh = await (scope.taskId !== undefined ? client.taskServers(scope.taskId) : client.runServers(scope.runId));
        if (mine !== latest.current) return;
        setData(fresh);
        setProblem(null);
      } catch (err) {
        if (mine === latest.current) setProblem(errorText(err));
      } finally {
        inFlight.current = null;
        again.current = null;
      }
    })();
    inFlight.current = read;
    return read;
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `key` stands for the scope
  }, [client, key]);
  newest.current = reload;

  useEffect(() => {
    setData(null);
    setProblem(null);
    // A read in flight is the old scope's: it may finish, but not land.
    latest.current++;
    void reload();
  }, [reload]);

  // The caller's stream said the servers changed: read them again. Only on
  // the version moving — the scope's own effect above reads on a new scope.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    void newest.current();
  }, [version]);

  const act = useCallback(
    async (what: string, action: () => Promise<unknown>) => {
      setBusy(what);
      setProblem(null);
      try {
        await action();
        // Resolves once a read started after the action has landed.
        await reload();
        return true;
      } catch (err) {
        setProblem(errorText(err));
        return false;
      } finally {
        setBusy(null);
      }
    },
    [reload],
  );

  // The run the actions go to: the one the data names, which for a task
  // is whichever run serves it now.
  const runId = data?.run?.id ?? scope.runId ?? null;
  const on = useCallback(
    (name: string, action: "start" | "stop" | "restart") => (runId ? act(name, () => client.serverAction(runId, name, action)) : Promise.resolve(false)),
    [act, client, runId],
  );

  // One object per change, so a screen that memoises on it re-renders for
  // the servers, not for every frame the stream delivers.
  return useMemo<ServersState>(() => ({
    data,
    problem,
    busy,
    act,
    start: (name) => on(name, "start"),
    stop: (name) => on(name, "stop"),
    restart: (name) => on(name, "restart"),
    remove: (name) => (runId ? act(name, () => client.removeRunServer(runId, name)) : Promise.resolve(false)),
    startAll: () => (runId ? act("*", () => client.serversAll(runId, "start-all")) : Promise.resolve(false)),
    stopAll: () => (runId ? act("*", () => client.serversAll(runId, "stop-all")) : Promise.resolve(false)),
    add: (input) => (runId ? act("*", () => client.addRunServer(runId, input)) : Promise.resolve(false)),
    clearProblem: () => setProblem(null),
  }), [data, problem, busy, act, on, runId, client]);
}
