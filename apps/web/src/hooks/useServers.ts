/**
 * What a task or a run serves: the run its servers live on, the servers,
 * the project's recipes — read from the API, and read again when the
 * caller's `version` moves (its own event stream saw a `servers.changed`)
 * or after an action here. Actions go through one `act`, so the busy
 * state and the reason for a refusal live in one place.
 */

import { useCallback, useEffect, useRef, useState } from "react";
import { TERMINAL_RUN_STATUSES, type RunServerInput, type RunStatus, type ServersRun, type TaskServers } from "@dude/domain";
import type { ApiClient } from "../api/client.ts";
import { errorText } from "./useSave.tsx";

/** Whether the run the servers live on is still going: its status is dude's, and may be missing. */
export function runIsLive(run: Pick<ServersRun, "state">): boolean {
  return !TERMINAL_RUN_STATUSES.includes((run.state || "running") as RunStatus);
}

export type ServersScope = { taskId: string; runId?: undefined } | { runId: string; taskId?: undefined };

export interface ServersState {
  data: TaskServers | null;
  problem: string | null;
  /** A name while an action on that server is in flight; "*" for one on all of them. */
  busy: string | null;
  reload: () => Promise<void>;
  /** Run an action, then re-read. Resolves whether it succeeded; a refusal is `problem`. */
  act: (what: string, action: () => Promise<unknown>) => Promise<boolean>;
  start: (name: string) => Promise<boolean>;
  stop: (name: string) => Promise<boolean>;
  restart: (name: string) => Promise<boolean>;
  remove: (name: string) => Promise<boolean>;
  startAll: () => Promise<boolean>;
  stopAll: () => Promise<boolean>;
  add: (input: RunServerInput) => Promise<boolean>;
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
  const latest = useRef(0);
  const inFlight = useRef(false);
  const again = useRef(false);
  const newest = useRef<() => Promise<void>>(async () => undefined);

  const reload = useCallback(async () => {
    if (inFlight.current) {
      again.current = true;
      return;
    }
    inFlight.current = true;
    const mine = ++latest.current;
    try {
      const fresh = await (scope.taskId !== undefined ? client.taskServers(scope.taskId) : client.runServers(scope.runId));
      if (mine !== latest.current) return;
      setData(fresh);
      setProblem(null);
    } catch (err) {
      if (mine === latest.current) setProblem(errorText(err));
    } finally {
      inFlight.current = false;
      if (again.current) {
        again.current = false;
        void newest.current();
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- `key` stands for the scope
  }, [client, key]);
  newest.current = reload;

  useEffect(() => {
    setData(null);
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
        // Read what the action left, whatever read is in flight: `reload`
        // folds a second ask into one more read.
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

  return {
    data,
    problem,
    busy,
    reload,
    act,
    start: useCallback((name: string) => on(name, "start"), [on]),
    stop: useCallback((name: string) => on(name, "stop"), [on]),
    restart: useCallback((name: string) => on(name, "restart"), [on]),
    remove: useCallback((name: string) => (runId ? act(name, () => client.removeRunServer(runId, name)) : Promise.resolve(false)), [act, client, runId]),
    startAll: useCallback(() => (runId ? act("*", () => client.serversAll(runId, "start-all")) : Promise.resolve(false)), [act, client, runId]),
    stopAll: useCallback(() => (runId ? act("*", () => client.serversAll(runId, "stop-all")) : Promise.resolve(false)), [act, client, runId]),
    add: useCallback((input: RunServerInput) => (runId ? act("*", () => client.addRunServer(runId, input)) : Promise.resolve(false)), [act, client, runId]),
    clearProblem: useCallback(() => setProblem(null), []),
  };
}
