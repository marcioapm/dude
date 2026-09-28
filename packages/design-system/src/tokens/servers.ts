/**
 * Server state vocabulary — how a server on a Run is presented, in the
 * grammar of `status.ts`: a tone, a glyph and a word, so the state reads
 * in grayscale and to a screen reader.
 *
 * The five states are lux's (`ServerState`); `waiting` is a display state
 * for a spec server on a branch preview that has not reached "starting
 * servers" yet — stopped on the wire, but nobody stopped it.
 */

import type { ServerState } from "@dude/domain";
import type { ToneName } from "./palette.ts";
import type { StatusGlyph } from "./status.ts";

/** lux's states, in a server's life order. */
export const SERVER_STATES = ["stopped", "starting", "ready", "unreachable", "exited"] as const satisfies readonly ServerState[];

export type ServerDisplayState = ServerState | "waiting";

export interface ServerStateSpec {
  readonly label: string;
  readonly tone: ToneName;
  readonly glyph: StatusGlyph | "warning";
  /** In progress: may animate. */
  readonly live: boolean;
  readonly description: string;
}

export const SERVER_STATE_SPECS: Record<ServerDisplayState, ServerStateSpec> = {
  stopped: { label: "Stopped", tone: "neutral", glyph: "stop", live: false, description: "Not running; a person starts it." },
  starting: { label: "Starting", tone: "info", glyph: "circle-half", live: true, description: "Its command runs; the port does not answer yet." },
  ready: { label: "Ready", tone: "success", glyph: "check", live: false, description: "The port answers; its URL serves." },
  unreachable: { label: "Unreachable", tone: "attention", glyph: "warning", live: false, description: "It was ready and the port stopped answering." },
  exited: { label: "Exited", tone: "danger", glyph: "cross", live: false, description: "Its command ended, with a code." },
  waiting: { label: "Waiting", tone: "neutral", glyph: "clock", live: false, description: "Starts once the preview's setup is done." },
};

export const SERVER_DISPLAY_STATES: readonly ServerDisplayState[] = [...SERVER_STATES, "waiting"];

export function serverStateSpec(state: ServerDisplayState): ServerStateSpec {
  return SERVER_STATE_SPECS[state];
}
