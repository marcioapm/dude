/**
 * What the Run page reads of a Run from its servers (the one place dude
 * reads lux's Run for the page): the lux terminal's URL and the memory
 * limit lux gave the container, and why lux has not placed it yet.
 */

import { useEffect, useRef, useState } from "react";
import type { RunStatus } from "@dude/domain";
import type { ApiClient } from "./api/client.ts";

/** Statuses in which lux is to run the Run: it may be waiting for a host (a first placement, a resume, a move). */
export const placingStatuses: readonly RunStatus[] = ["pending", "scheduled", "starting", "running"];

export interface RunServersRead {
  /** The terminal's URL once known: it names the lux Run, which a resume keeps. */
  terminalUrl: string | null;
  memoryLimit: number | null;
  /** lux's reason the Run has no host yet; null when lux says it is not waiting, or before the first answer. */
  waitingReason: string | null;
}

/**
 * The Run's terminal and waiting reason, read with its servers.
 *
 * The terminal is read while the Run is `running` and its URL unknown,
 * again whenever `askAgain` moves (any servers.changed, a stream that came
 * back). The waiting reason is read while the Run is placing, again
 * whenever `waitAsk` moves (lux's state changed: servers.changed with
 * change "state") or the status does. One read serves both, one at a time
 * per client and Run: a trigger during a read is folded into one follow-up
 * after it, made only if something still wants it. Only a new client or
 * Run, or unmounting, discards an answer.
 */
export function useRunServers(client: ApiClient, runId: string, status: RunStatus | undefined, askAgain: number, waitAsk: number): RunServersRead {
  const [terminal, setTerminal] = useState<{ runId: string; url: string; memoryLimit: number | null } | null>(null);
  const [waiting, setWaiting] = useState<{ runId: string; reason: string | null } | null>(null);
  const known = terminal?.runId === runId ? terminal.url : null;
  const placing = status !== undefined && placingStatuses.includes(status);
  const running = status === "running";
  const reader = useRef<RunServersReader | null>(null);
  const waitAsked = useRef<string | null>(null);
  useEffect(() => {
    const r = new RunServersReader(client, runId,
      (url, memoryLimit) => setTerminal({ runId, url, memoryLimit }),
      (reason) => setWaiting({ runId, reason }));
    reader.current = r;
    waitAsked.current = null;
    return () => {
      r.dead = true;
    };
  }, [client, runId]);
  useEffect(() => {
    const r = reader.current;
    if (!r) return;
    r.terminalWanted = running && !known;
    const key = `${status}:${waitAsk}`;
    if (!placing) r.waitWanted = false;
    else if (waitAsked.current !== key) r.waitWanted = true;
    waitAsked.current = key;
    if (r.terminalWanted || r.waitWanted) r.ask();
  }, [client, runId, status, running, placing, known, askAgain, waitAsk]);
  return {
    terminalUrl: known,
    memoryLimit: terminal?.runId === runId ? terminal.memoryLimit : null,
    waitingReason: placing && waiting?.runId === runId ? waiting.reason : null,
  };
}

class RunServersReader {
  dead = false;
  /** Running and the URL still unknown. */
  terminalWanted = false;
  /** A placing Run's state changed since the last read began. */
  waitWanted = false;
  private inFlight = false;
  private again = false;
  constructor(
    private readonly client: ApiClient,
    private readonly runId: string,
    private readonly foundTerminal: (url: string, memoryLimit: number | null) => void,
    private readonly foundWaiting: (reason: string | null) => void,
  ) {}

  ask(): void {
    if (this.dead) return;
    if (this.inFlight) {
      this.again = true;
      return;
    }
    this.inFlight = true;
    this.again = false;
    this.waitWanted = false;
    this.client
      .runServers(this.runId)
      .then((s) => {
        if (this.dead) return;
        this.foundWaiting(s.run?.waitingReason ?? null);
        const url = s.run?.terminalUrl;
        if (url) {
          this.terminalWanted = false;
          this.foundTerminal(url, s.run?.memoryLimit ?? null);
        }
      }, () => undefined)
      .finally(() => {
        this.inFlight = false;
        if (this.again && (this.terminalWanted || this.waitWanted)) this.ask();
      });
  }
}
