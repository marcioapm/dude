/**
 * What a server's state says in words, from lux's Server object: the
 * display state (lux's five, plus "waiting" for a preview's spec server
 * that has not had its turn yet), the mark's word when it differs from the
 * vocabulary's ("Exited 1"), and the detail beside it ("ready for 12m",
 * "stopped at 14:32 · was ready for 41m"). One place, so the panel, the
 * summary and the preview's foot never disagree.
 */

import type { PreviewStage, Server, ServersRunKind } from "@dude/domain";
import type { ServerDisplayState } from "../tokens/servers.ts";
import { formatDuration, formatTimestamp } from "./format.ts";
import { toMs } from "./useNow.ts";

export interface ServerWords {
  readonly state: ServerDisplayState;
  /** The mark's word, when it is not the state's own. */
  readonly label?: string | undefined;
  readonly detail: string;
}

export interface ServerContext {
  /** The run the server is on: a preview's spec servers wait for setup. */
  readonly runKind?: ServersRunKind | null | undefined;
  readonly previewStage?: PreviewStage | null | undefined;
}

/** Before "starting servers", a preview's spec servers are waiting, not stopped. */
const BEFORE_SERVERS: ReadonlySet<PreviewStage> = new Set(["scheduling", "cloning", "setup"]);

export function describeServer(server: Server, now: number, context: ServerContext = {}): ServerWords {
  const since = toMs(server.since) ?? now;
  const ready = toMs(server.readySince);
  const at = formatTimestamp(since, "time-short");
  const wasReady = ready !== null && ready <= since ? ` · was ready for ${formatDuration(Math.max(0, since - ready), { style: "age" })}` : "";
  switch (server.state) {
    case "ready":
      return { state: "ready", detail: `ready for ${formatDuration(Math.max(0, now - (ready ?? since)), { style: "age" })}` };
    case "starting":
      return { state: "starting", detail: `starting · ${formatDuration(Math.max(0, now - since))}` };
    case "unreachable":
      return { state: "unreachable", detail: `unreachable since ${at}${wasReady}` };
    case "exited": {
      const code = server.exitCode ?? null;
      return { state: "exited", label: code === null ? undefined : `Exited ${code}`, detail: `exited ${at}${wasReady}` };
    }
    case "stopped": {
      if (server.stoppedEpoch === null) {
        if (context.runKind === "preview") {
          if (server.fromSpec && context.previewStage && BEFORE_SERVERS.has(context.previewStage)) return { state: "waiting", detail: "starts after setup" };
          if (!server.fromSpec) return { state: "stopped", detail: "manual" };
        }
        return { state: "stopped", detail: "not started" };
      }
      return { state: "stopped", detail: `stopped at ${at}${wasReady}` };
    }
  }
}

/** The state a list of servers puts on its tab: the first bad one, else how many are ready. */
export function summarizeServers(servers: ReadonlyArray<Pick<Server, "name" | "state">>): { bad: Pick<Server, "name" | "state"> | null; ready: number } {
  return {
    bad: servers.find((s) => s.state === "exited" || s.state === "unreachable") ?? null,
    ready: servers.filter((s) => s.state === "ready").length,
  };
}

/** Whether "Start all" / "Stop all" would do anything. */
export function canStartAny(servers: ReadonlyArray<Pick<Server, "state">>): boolean {
  return servers.some((s) => s.state === "stopped" || s.state === "exited" || s.state === "unreachable");
}
export function canStopAny(servers: ReadonlyArray<Pick<Server, "state">>): boolean {
  return servers.some((s) => s.state === "ready" || s.state === "starting" || s.state === "unreachable");
}

/** A URL without its scheme, as a row shows it. */
export function bareUrl(url: string): string {
  return url.replace(/^https?:\/\//, "");
}
