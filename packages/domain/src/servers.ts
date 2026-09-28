import { z } from "zod";
import type { PersonRef } from "./hierarchy.ts";

/**
 * Servers: named ports of a Run, with the command that serves each, and the
 * project's recipes for them. The Server object is lux's, passed through by
 * the control plane unchanged (servers contract §A2, §C); the rest is dude's.
 */

// ---------------------------------------------------------------------------
// A project's recipes and its preview settings
// ---------------------------------------------------------------------------

/** Lowercase letters, digits and dashes, starting with a letter: the first label of the URL. */
export const SERVER_NAME_PATTERN = /^[a-z][a-z0-9-]{0,29}$/;

/** Why a server name is not one, in words; null when it is. */
export function serverNameProblem(name: string): string | null {
  if (!SERVER_NAME_PATTERN.test(name) || name.endsWith("-")) {
    return "Lowercase letters, digits and dashes, starting with a letter: it becomes the first label of the URL.";
  }
  return null;
}

/** Ports below 1024 are the container's services'; nothing a recipe should take. */
export const SERVER_PORT_MIN = 1024;
export const SERVER_PORT_MAX = 65535;

export function serverPortProblem(port: number): string | null {
  return Number.isInteger(port) && port >= SERVER_PORT_MIN && port <= SERVER_PORT_MAX ? null : `Between ${SERVER_PORT_MIN} and ${SERVER_PORT_MAX}.`;
}

export const serverEnvVarSchema = z.object({ name: z.string().min(1), value: z.string() });
export type ServerEnvVar = z.infer<typeof serverEnvVarSchema>;

/** What a person writes to define a server: `PUT /v1/projects/{id}/servers/{name}`. */
export const serverRecipeInputSchema = z.object({
  name: z.string().regex(SERVER_NAME_PATTERN).refine((n) => !n.endsWith("-")),
  port: z.number().int().min(SERVER_PORT_MIN).max(SERVER_PORT_MAX),
  command: z.string().min(1),
  /** Relative to the repository. */
  workdir: z.string().default(""),
  /** Run once before the command, in the same directory. */
  setup: z.string().nullable().default(null),
  env: z.array(serverEnvVarSchema).default([]),
  autostartInPreviews: z.boolean().default(true),
});
export type ServerRecipeInput = z.infer<typeof serverRecipeInputSchema>;

/** A recipe as the project lists it. */
export interface ServerRecipe extends ServerRecipeInput {
  updatedAt: string;
  updatedBy: PersonRef;
}

export const PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES = 30;

export const previewSettingsSchema = z.object({
  /** The container a preview run starts in; null is the project runner's image. */
  image: z.string().nullable().default(null),
  /** Hosts a preview run may reach, beyond the repository. */
  egress: z.array(z.string().min(1)).default([]),
  /** With no request for this long, the preview run is parked. */
  idleTimeoutMinutes: z.number().int().positive().default(PREVIEW_IDLE_TIMEOUT_DEFAULT_MINUTES),
  /**
   * The preview domain (`lux.absmartly.dev`), for showing what URL a name
   * makes before a server exists. Not in the build contract: read when the
   * backend sends it, a placeholder otherwise.
   */
  domain: z.string().nullable().optional(),
});
export type PreviewSettings = z.infer<typeof previewSettingsSchema>;

// ---------------------------------------------------------------------------
// lux's Server, passed through
// ---------------------------------------------------------------------------

export const SERVER_STATES = ["stopped", "starting", "ready", "unreachable", "exited"] as const;
export type ServerState = (typeof SERVER_STATES)[number];

export const SERVER_STOP_REASONS = ["stopped", "run stopped", "migrated", "host lost"] as const;
export type ServerStopReason = (typeof SERVER_STOP_REASONS)[number];

/** A server on a Run, as lux reports it (contract §A2). */
export interface Server {
  name: string;
  port: number;
  command: string[] | null;
  workdir?: string | null | undefined;
  env?: Record<string, string> | undefined;
  /** Declared in the Run's spec (a preview's autostart recipes) rather than added while it ran. */
  fromSpec: boolean;
  state: ServerState;
  /** When exited. */
  exitCode?: number | null | undefined;
  /** The last stderr line on exit, if any. */
  error?: string | null | undefined;
  /** When the state last changed. */
  since: string;
  /**
   * When it became ready. lux keeps it only while the server is ready
   * (null otherwise); a backend that leaves the last value on a stopped
   * server lets the UI say how long it had been up.
   */
  readySince: string | null;
  /** Why it is stopped. */
  stopReason: ServerStopReason | null;
  /** The placement epoch it stopped in; null if it never started. */
  stoppedEpoch: number | null;
  /** The placement epoch of the current state. */
  epoch: number;
  /** Null when previews are not configured. */
  url: string | null;
  lastRequestAt?: string | null | undefined;
}

/** One line of a server's output: `GET …/servers/{name}/log`. */
export interface ServerLogLine {
  /** Unix milliseconds. */
  t: number;
  stream: "stdout" | "stderr";
  text: string;
}

// ---------------------------------------------------------------------------
// A task's servers: the run they live on, and the project's recipes
// ---------------------------------------------------------------------------

export const PREVIEW_STAGES = ["scheduling", "cloning", "setup", "starting", "ready"] as const;
export type PreviewStage = (typeof PREVIEW_STAGES)[number];

export type ServersRunKind = "agent" | "preview";

/** The run a task's servers live on: its agent's, or a branch preview's. */
export interface ServersRun {
  /** dude's run id. */
  id: string;
  luxRunId: string;
  kind: ServersRunKind;
  /** "Implementer run", "Branch preview". */
  label: string;
  /** dude's run status. */
  state: string;
  luxState: string;
  host: string | null;
  startedAt: string | null;
  startedBy: PersonRef | null;
  branch: string | null;
  commit: string | null;
  /** A preview run's progress; null for an agent's. */
  previewStage: PreviewStage | null;
  /** A preview run is parked after this long without a request. */
  parksAfterMinutes: number | null;
  /** `{LUX_CONSOLE_URL}/runs/{luxRunId}/terminal`. */
  terminalUrl: string;
}

/** `GET /v1/tasks/{id}/servers`, `GET /v1/runs/{id}/servers`. */
export interface TaskServers {
  run: ServersRun | null;
  servers: Server[];
  /** Set when every server stopped because the run moved host. */
  moved: { at: string; fromHost: string | null; toHost: string | null } | null;
  /** The project's, for "Add server". */
  recipes: ServerRecipe[];
}

/** What adds a server to a run: one of the project's, or one just for this run. */
export type RunServerInput =
  | { recipe: string }
  | { name: string; port: number; command?: string | undefined; workdir?: string | undefined; env?: Record<string, string> | undefined };
