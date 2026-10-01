import { z } from "zod";

/**
 * Servers: what a Run can serve, through lux. A project defines its
 * servers (recipes); a person adds them to a task's Run, and a branch
 * preview starts those marked to start in previews. Shared by the API and
 * the web app; the orchestrator answers with the same shapes
 * (orchestrator/internal/servers).
 */

/**
 * A server's name, as lux takes it: part of its URL
 * (`<name>-<run>.<domain>`), so a DNS label's start, at most 30, never
 * ending in '-'.
 */
export const SERVER_NAME = /^[a-z][a-z0-9-]{0,29}$/;

export const serverNameSchema = z
  .string()
  .regex(SERVER_NAME, "lowercase letters, digits and '-', starting with a letter, at most 30")
  .refine((n) => !n.endsWith("-"), "must not end in '-'");

export const serverPortSchema = z.number().int().min(1).max(65535);

/** Inside the checkout: relative, never climbing out of it. '' is its root. */
export const serverWorkdirSchema = z
  .string()
  .max(500)
  .refine((w) => !w.startsWith("/") && !w.split("/").includes(".."), "relative to the repository, inside it");

const envNameSchema = z.string().regex(/^[A-Za-z_][A-Za-z0-9_]*$/, "an environment variable's name")
  .refine((n) => !n.startsWith("LUX_"), "the LUX_ prefix is reserved");

/** A recipe as a maintainer writes it (`PUT /v1/projects/:id/servers/:name`). */
export const recipeInputSchema = z.object({
  name: serverNameSchema,
  port: serverPortSchema,
  command: z.string().trim().min(1).max(4000),
  workdir: serverWorkdirSchema.default(""),
  setup: z.string().max(4000).nullable().default(null),
  env: z
    .array(z.object({ name: envNameSchema, value: z.string().max(4000) }))
    .max(100)
    .default([])
    .refine((env) => new Set(env.map((e) => e.name)).size === env.length, "each variable once"),
  autostartInPreviews: z.boolean().default(false),
});
export type RecipeInput = z.infer<typeof recipeInputSchema>;

/** A project's server, as the API shows it. */
export interface Recipe extends RecipeInput {
  updatedAt: string;
  updatedBy: { id: string; name: string } | null;
}

/** A concrete hostname: labels of letters, digits, '-' and '_'; no wildcards. */
const HOSTNAME = /^[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]{0,61}[a-z0-9_])?)*\.?$/i;
const IPV4 = /^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$/;

function isIPv6(s: string): boolean {
  if (!/^[0-9a-f:.]+$/i.test(s) || !s.includes(":")) return false;
  try {
    new URL(`http://[${s}]/`);
    return true;
  } catch {
    return false;
  }
}

/**
 * Why lux would refuse an egress entry, or null if it takes it: "*"
 * (anywhere), an address, a CIDR range, or a concrete hostname. lux
 * resolves each host it allows, so a wildcard host is refused (and would
 * fail every preview); list the hosts themselves. Mirrored by the
 * orchestrator's servers.EgressRule.
 */
export function egressProblem(entry: string): string | null {
  const e = entry.trim();
  if (e === "*" || IPV4.test(e) || isIPv6(e)) return null;
  if (e.includes("/")) {
    const [ip, bits, ...rest] = e.split("/");
    const max = IPV4.test(ip!) ? 32 : isIPv6(ip!) ? 128 : -1;
    if (rest.length === 0 && max > 0 && /^\d{1,3}$/.test(bits!) && Number(bits) <= max) return null;
    return `${e}: not a CIDR range (an address, '/', and a prefix length up to ${max > 0 ? max : 32})`;
  }
  if (e.includes("*")) return `${e}: wildcards cannot be resolved; list each host (or "*" for anywhere)`;
  if (e.length > 253 || !HOSTNAME.test(e)) return `${e}: not a hostname, address or CIDR range`;
  return null;
}

/** How a project's branch previews run (`PUT /v1/projects/:id/preview-settings`). */
export const previewSettingsSchema = z.object({
  /** null: the project's runtime image. */
  image: z.string().trim().min(1).max(500).nullable().default(null),
  /** Hosts (or addresses, CIDR ranges) a preview may reach; "*" for anywhere. */
  egress: z
    .array(z.string().trim().min(1).max(253).superRefine((e, ctx) => {
      const problem = egressProblem(e);
      if (problem) ctx.addIssue({ code: z.ZodIssueCode.custom, message: problem });
    }))
    .max(200)
    .default([]),
  idleTimeoutMinutes: z.number().int().min(1).max(7 * 24 * 60).default(15),
  /** The machine size a preview runs on (an organization's size id); null: the organization's default size. */
  machineSize: z.string().min(1).max(100).nullable().default(null),
});
export type PreviewSettings = z.infer<typeof previewSettingsSchema>;

export type ServerState = "stopped" | "starting" | "ready" | "unreachable" | "exited";

/** One of a Run's servers, as lux reports it, passed through. */
export interface RunServer {
  name: string;
  port: number;
  command: string[] | null;
  workdir: string;
  env: Record<string, string>;
  fromSpec: boolean;
  state: ServerState;
  exitCode?: number;
  error?: string;
  since: string;
  readySince: string | null;
  stopReason: "stopped" | "run stopped" | "migrated" | "host lost" | null;
  stoppedEpoch: number | null;
  epoch: number;
  url: string | null;
  lastRequestAt?: string | null;
  /**
   * A wakeable preview's server (lux's own resource): lux's server state,
   * beside `state`, its process's. Absent on a Run's own servers.
   */
  serverState?: WakeableServerState;
}

/** lux's state of a server that wakes on request (`/v1/servers`' `state`). */
export type WakeableServerState = "ready" | "waking" | "asleep" | "stopped" | "unreachable" | "exited" | "no answer";

export type PreviewStage = "scheduling" | "cloning" | "setup" | "starting" | "ready";

/** A task's (or a Run's) servers: `GET /v1/tasks/:id/servers`, `GET /v1/runs/:id/servers`. */
export interface TaskServers {
  run: null | {
    id: string;
    luxRunId: string;
    kind: "agent" | "preview";
    label: string;
    /** dude's status for the Run, and lux's state. */
    state: string;
    luxState: string;
    host: string | null;
    startedAt: string | null;
    startedBy: { id: string; name: string } | null;
    branch: string | null;
    commit: string | null;
    previewStage: PreviewStage | null;
    parksAfterMinutes: number | null;
    terminalUrl: string | null;
    /** A preview whose servers wake on request: opening a URL starts it. */
    wakeable?: boolean;
    /** A wakeable preview nothing serves and no wake is due. */
    asleep?: boolean;
    /** The memory limit lux gave its container, in bytes, when lux reports one. */
    memoryLimit?: number | null;
  };
  servers: RunServer[];
  moved: null | { at: string; fromHost: string | null; toHost: string | null };
  recipes: Recipe[];
  /**
   * The task's live branch preview, when the Run shown is its agent's (a
   * task's servers only): so it can still be stopped. null otherwise.
   */
  preview: null | { id: string; luxRunId: string; state: string };
}

/** What a person adds to a Run: a recipe, or a server of their own. */
export const addServerSchema = z.union([
  z.object({ recipe: serverNameSchema }).strict(),
  z.object({
    name: serverNameSchema,
    port: serverPortSchema,
    /** A shell command line, or argv. */
    command: z.union([z.string().max(4000), z.array(z.string()).min(1)]).nullish(),
    workdir: serverWorkdirSchema.optional(),
    env: z.union([z.record(envNameSchema, z.string()), z.array(z.object({ name: envNameSchema, value: z.string() }))]).optional(),
  }).strict(),
]);
export type AddServer = z.infer<typeof addServerSchema>;

/** The payload of a `servers.changed` event. */
export interface ServersChanged {
  taskId: string;
  runId: string;
  change: string;
  server?: string;
  state?: ServerState;
  exitCode?: number;
  luxState?: string;
  error?: string;
}
