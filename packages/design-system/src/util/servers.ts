/**
 * What a server's state says in words, from lux's Server object: the
 * display state (lux's five, plus "waiting" for a preview's spec server
 * that has not had its turn yet), the mark's word when it differs from the
 * vocabulary's ("Exited 1"), and the detail beside it ("ready for 12m",
 * "stopped at 14:32 · was ready for 41m"). One place, so the panel, the
 * summary and the preview's foot never disagree — and one place for what
 * a state allows, so the row, the summary and Start all agree with lux.
 */

import { serverNameSchema, serverPortSchema, TERMINAL_RUN_STATUSES, type RunServer, type RunStatus, type TaskServers } from "@dude/domain";
import type { LogLine } from "../components/LogStream.tsx";
import type { ServerDisplayState } from "../tokens/servers.ts";
import { formatDuration, formatTimestamp } from "./format.ts";
import { toMs } from "./useNow.ts";

/** The run a task's servers live on, as `TaskServers` names it. */
export type ServersRun = NonNullable<TaskServers["run"]>;

/** One line of a server's output: `GET …/servers/{name}/log`. */
export interface ServerLogLine {
  /** Unix milliseconds. */
  readonly t: number;
  readonly stream: "stdout" | "stderr";
  readonly text: string;
}

/** What the preview settings say when nothing has been chosen. */
export const PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES = 15;

/** Why a server name is not one, in words a form shows; null when it is. */
export function serverNameProblem(name: string): string | null {
  return serverNameSchema.safeParse(name).success ? null : "Lowercase letters, digits and dashes, starting with a letter: it becomes the first label of the URL.";
}

export function serverPortProblem(port: number): string | null {
  return serverPortSchema.safeParse(port).success ? null : "Between 1 and 65535.";
}

export interface ServerWords {
  readonly state: ServerDisplayState;
  /** The mark's word, when it is not the state's own. */
  readonly label?: string | undefined;
  readonly detail: string;
}

/** The run a server is on, as far as its words depend on it: a preview's spec servers wait for setup. */
export type ServerRunContext = Pick<ServersRun, "kind" | "previewStage"> | null | undefined;

/** Before "starting servers", a preview's spec servers are waiting, not stopped. */
const BEFORE_SERVERS: ReadonlySet<ServersRun["previewStage"]> = new Set(["scheduling", "image", "volumes", "cloning", "container", "setup"]);

export function describeServer(server: RunServer, now: number, run?: ServerRunContext): ServerWords {
  const since = toMs(server.since) ?? now;
  const ready = toMs(server.readySince);
  const at = formatTimestamp(since, "time-short");
  const wasReady = ready !== null && ready <= since ? ` · was ready for ${formatDuration(Math.max(0, since - ready), { style: "age" })}` : "";
  // A wakeable preview's server, while no process serves it: lux's word.
  if (server.state === "stopped" || server.state === "starting") {
    switch (server.serverState) {
      case "asleep":
        return { state: "stopped", label: "Asleep", detail: "wakes when its URL is opened" };
      case "waking":
        return { state: "starting", label: "Waking", detail: server.state === "starting" ? `starting · ${formatDuration(Math.max(0, now - since))}` : "waking up" };
      case "no answer":
        return { state: "unreachable", label: "No answer", detail: "asked to wake; nothing came up — opening its URL asks again" };
    }
  }
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
        if (run?.kind === "preview") {
          if (server.fromSpec && run.previewStage && BEFORE_SERVERS.has(run.previewStage)) return { state: "waiting", detail: "starts after setup" };
          if (!server.fromSpec) return { state: "stopped", detail: "manual" };
        }
        return { state: "stopped", detail: "not started" };
      }
      return { state: "stopped", detail: `stopped at ${at}${wasReady}` };
    }
    default:
      // A state lux added that this build does not know: shown with its own
      // word under the attention glyph, rather than nothing at all.
      return { state: "unreachable", label: String(server.state), detail: `since ${at}` };
  }
}

type Stateful = { readonly state: ServerDisplayState };

/**
 * What a state allows, as lux has it: start is a no-op while a server is
 * starting, ready or unreachable — an unreachable one is restarted, not
 * started — and stop means something only then.
 */
export const canStart = (s: Stateful): boolean => s.state === "stopped" || s.state === "exited";
export const canStop = (s: Stateful): boolean => s.state === "ready" || s.state === "starting" || s.state === "unreachable";
/** Its clock is running: a starting one counts up, a ready one has been ready for longer. */
export const isMoving = (s: Stateful): boolean => s.state === "ready" || s.state === "starting";

/** Whether "Start all" / "Stop all" would do anything. */
export const canStartAny = (servers: ReadonlyArray<Stateful>): boolean => servers.some(canStart);
export const canStopAny = (servers: ReadonlyArray<Stateful>): boolean => servers.some(canStop);
export const anyMoving = (servers: ReadonlyArray<Stateful>): boolean => servers.some(isMoving);

/** The state a list of servers puts on its tab: the first bad one, else how many are ready. */
export function summarizeServers<S extends Pick<RunServer, "name" | "state">>(servers: ReadonlyArray<S>): { bad: S | null; ready: number } {
  return {
    bad: servers.find((s) => s.state === "exited" || s.state === "unreachable") ?? null,
    ready: servers.filter((s) => s.state === "ready").length,
  };
}

/** A process is running: lux has it starting, answering, or gone quiet on its port. */
export const isOn = (s: Stateful): boolean => s.state === "starting" || s.state === "ready" || s.state === "unreachable";

export interface TaskServersSummary {
  /** The servers with a process running, in the run's order. */
  readonly on: ReadonlyArray<RunServer>;
  /** The rest: stopped or exited on the run, or every recipe (stopped) when no run serves the task. */
  readonly off: ReadonlyArray<Pick<RunServer, "name" | "state">>;
  /** The first exited or unreachable server (`summarizeServers`'s rule). */
  readonly bad: RunServer | null;
  /** A server is starting, or a branch preview is still coming up. */
  readonly starting: boolean;
  /** A branch preview is still coming up (its stages before ready). */
  readonly booting: boolean;
  /** The run moved host and its servers stopped with the old placement. */
  readonly moved: boolean;
}

/**
 * What the task's Servers tab says, its label and its tooltip from one
 * reading: which servers are on, which are off, the first bad one, and
 * whether something is starting. Null when there is nothing to say: no
 * run serves the task and the project defines no servers.
 */
export function summarizeTaskServers(data: Pick<TaskServers, "run" | "servers" | "recipes" | "moved"> | null): TaskServersSummary | null {
  if (!data) return null;
  if (!data.run) {
    if (data.recipes.length === 0) return null;
    return { on: [], off: data.recipes.map((r) => ({ name: r.name, state: "stopped" as const })), bad: null, starting: false, booting: false, moved: false };
  }
  const run = data.run;
  const live = !TERMINAL_RUN_STATUSES.includes(run.state as RunStatus);
  const booting = run.kind === "preview" && run.previewStage !== "ready" && live && !run.asleep;
  return {
    on: data.servers.filter(isOn),
    off: data.servers.filter((s) => !isOn(s)),
    bad: summarizeServers(data.servers).bad,
    starting: booting || data.servers.some((s) => s.state === "starting"),
    booting,
    moved: data.moved !== null,
  };
}

/** A URL without its scheme, as a row shows it. */
export function bareUrl(url: string): string {
  return url.replace(/^https?:\/\//, "");
}

/**
 * A server's URL fit for an `href`: lux's are https, always; anything else
 * (a `javascript:` URL, plain http) is shown as text and never followed.
 */
export function safeServerUrl(url: string | null | undefined): string | null {
  return url && /^https:\/\//i.test(url) ? url : null;
}

/**
 * lux's log lines as LogStream draws them: a `[lux]` line is the runtime's
 * own word, stderr keeps its channel. The sequence is the line's time, so
 * a re-read tail — the same window, slid on — keeps each line's key and
 * LogStream sees what is new; lines within one millisecond keep their order.
 */
export function serverLogLines(log: ReadonlyArray<ServerLogLine>): LogLine[] {
  let lastT = -1;
  let sameT = 0;
  return log.map((l) => {
    sameT = l.t === lastT ? sameT + 1 : 0;
    lastT = l.t;
    return {
      seq: l.t * 1000 + sameT,
      text: l.text,
      ts: l.t,
      channel: l.stream,
      ...(l.text.includes("[lux]") ? { level: "system" as const } : {}),
    };
  });
}
