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

/** How a project's branch previews run (`PUT /v1/projects/:id/preview-settings`). */
export const previewSettingsSchema = z.object({
  /** null: the project's runtime image. */
  image: z.string().trim().min(1).max(500).nullable().default(null),
  /** Hosts (or addresses, CIDR ranges) a preview may reach; "*" for anywhere. */
  egress: z.array(z.string().trim().min(1).max(253)).max(200).default([]),
  idleTimeoutMinutes: z.number().int().min(1).max(7 * 24 * 60).default(30),
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
}

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
  };
  servers: RunServer[];
  moved: null | { at: string; fromHost: string | null; toHost: string | null };
  recipes: Recipe[];
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
    env: z.union([z.record(z.string()), z.array(z.object({ name: envNameSchema, value: z.string() }))]).optional(),
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
