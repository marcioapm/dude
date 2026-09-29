/**
 * Servers fixtures: the mockups' scenarios a–e as the API would return
 * them (`TaskServers`, contract §C), so the gallery and the web app's dev
 * mode draw the same thing without a backend. Deterministic, anchored to
 * now so ages read sensibly.
 */

import type { Recipe, RunServer, TaskServers } from "@dude/domain";
import type { ServerLogLine, ServersRun } from "../util/servers.ts";

export const NOW = Date.now();
export const MIN = 60_000;
export const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

export const PREVIEW_DOMAIN = "lux.example.com";
export const RUN_SUFFIX = "k3jq7x2mfa9vbn4z";
const PREVIEW_SUFFIX = "p8d2wq5nzr7m4kx1";
export const serverUrl = (name: string, suffix = RUN_SUFFIX) => `https://${name}-${suffix}.${PREVIEW_DOMAIN}`;
export const previewEgress = ["registry.npmjs.org", "proxy.golang.org", "sum.golang.org", "api.example.com", "sandbox.example.com"];

const ana = { id: "u_ana", name: "Ana Ribeiro" };
const marcio = { id: "u_marcio", name: "Márcio Martins" };

export const serverRecipes: Recipe[] = [
  { name: "web", port: 3000, command: "npm run dev -- --host 0.0.0.0 --port 3000", workdir: "apps/web", setup: "npm ci", env: [{ name: "VITE_API_URL", value: "http://localhost:8080" }, { name: "VITE_EXAMPLE_ENV", value: "preview" }], autostartInPreviews: true, updatedAt: iso(2 * 24 * 60 * MIN), updatedBy: ana },
  { name: "api", port: 8080, command: "go run ./cmd/api --port 8080 --dev", workdir: "services/api", setup: "make deps", env: [{ name: "DATABASE_URL", value: "postgres://dev@localhost:5432/console" }, { name: "LOG_LEVEL", value: "debug" }], autostartInPreviews: true, updatedAt: iso(2 * 24 * 60 * MIN), updatedBy: ana },
  { name: "storybook", port: 6006, command: "npm run storybook -- --ci --port 6006", workdir: "apps/web", setup: null, env: [], autostartInPreviews: false, updatedAt: iso(9 * 24 * 60 * MIN), updatedBy: marcio },
];

export const agentRun: ServersRun = {
  id: "run_01j9x5m2q7k8e4t1",
  luxRunId: `run_${RUN_SUFFIX}`,
  kind: "agent",
  label: "Implementer run",
  state: "running",
  luxState: "running",
  host: "lux-c7 (eu-west-1)",
  startedAt: iso(38 * MIN),
  startedBy: marcio,
  branch: "dude/tsk_01j9x4kq/checkout-v2-split-payment",
  commit: "3f2a9c1",
  previewStage: null,
  parksAfterMinutes: null,
  terminalUrl: `https://lux.example.com/runs/run_${RUN_SUFFIX}/terminal`,
};

export const previewRun: ServersRun = {
  id: "run_01j9x7p0v3n6a8c2",
  luxRunId: `run_${PREVIEW_SUFFIX}`,
  kind: "preview",
  label: "Branch preview",
  state: "running",
  luxState: "starting",
  host: "lux-c3",
  startedAt: iso(40_000),
  startedBy: marcio,
  branch: "feature/checkout-v2",
  commit: "3f2a9c1",
  previewStage: "setup",
  parksAfterMinutes: 30,
  terminalUrl: `https://lux.example.com/runs/run_${PREVIEW_SUFFIX}/terminal`,
};

/** A Server as lux reports it, from a recipe's command and directory when it has one; `patch` says the rest. */
export function server(name: string, port: number, patch: Partial<RunServer> & { state: RunServer["state"] }, suffix = RUN_SUFFIX): RunServer {
  return {
    name,
    port,
    command: ["sh", "-c", `${serverRecipes.find((r) => r.name === name)?.command ?? "true"}`],
    workdir: serverRecipes.find((r) => r.name === name)?.workdir ?? "",
    env: {},
    fromSpec: false,
    since: iso(0),
    readySince: null,
    stopReason: null,
    stoppedEpoch: null,
    epoch: 1,
    url: serverUrl(name, suffix),
    ...patch,
  };
}

const webReady = server("web", 3000, { state: "ready", since: iso(12 * MIN), readySince: iso(12 * MIN) });
const apiStarting = server("api", 8080, { state: "starting", since: iso(9_000) });
const storybookOff = server("storybook", 6006, { state: "stopped" });

/** a: the implementer's run, web ready, api starting, storybook never started. */
export const serversRunning: TaskServers = { run: agentRun, servers: [webReady, apiStarting, storybookOff], moved: null, recipes: serverRecipes, preview: null };

/** b: the run moved host; every server stopped with the old placement. */
const movedAt = iso(11 * MIN);
export const serversMigrated: TaskServers = {
  run: { ...agentRun, host: "lux-c9 (eu-west-1)" },
  servers: [
    server("web", 3000, { state: "stopped", since: movedAt, readySince: iso(52 * MIN), stopReason: "migrated", stoppedEpoch: 1, epoch: 2 }),
    server("api", 8080, { state: "stopped", since: movedAt, readySince: iso(51 * MIN), stopReason: "migrated", stoppedEpoch: 1, epoch: 2 }),
    { ...storybookOff, epoch: 2 },
  ],
  moved: { at: movedAt, fromHost: "lux-c7", toHost: "lux-c9" },
  recipes: serverRecipes,
  preview: null,
};

/** c: api exited 1 — the port was taken. */
export const serversExited: TaskServers = {
  run: agentRun,
  servers: [
    webReady,
    server("api", 8080, { state: "exited", exitCode: 1, error: "listen tcp :8080: bind: address already in use", since: iso(14 * MIN), stoppedEpoch: 1 }),
    storybookOff,
  ],
  moved: null,
  recipes: serverRecipes,
  preview: null,
};

/** d: no run serves the task: the implementer finished and its run ended. */
export const serversNoRun: TaskServers = { run: null, servers: [], moved: null, recipes: serverRecipes, preview: null };

/** e: a branch preview coming up, in setup. */
export const serversPreviewBooting: TaskServers = {
  run: previewRun,
  servers: [
    server("web", 3000, { state: "stopped", fromSpec: true }, PREVIEW_SUFFIX),
    server("api", 8080, { state: "stopped", fromSpec: true }, PREVIEW_SUFFIX),
    server("storybook", 6006, { state: "stopped" }, PREVIEW_SUFFIX),
  ],
  moved: null,
  recipes: serverRecipes,
  preview: null,
};

export const serverScenarios = {
  a: serversRunning,
  b: serversMigrated,
  c: serversExited,
  d: serversNoRun,
  e: serversPreviewBooting,
} as const;
export type ServerScenario = keyof typeof serverScenarios;

// -- Logs, as lux streams them --------------------------------------------

const RED = "\u001b[31m";
const GREEN = "\u001b[32m";
const CYAN = "\u001b[36m";
const BLUE = "\u001b[34m";
const BOLD = "\u001b[1m";
const DIM = "\u001b[2m";
const R = "\u001b[0m";

function lines(at: number, items: ReadonlyArray<readonly [string, ServerLogLine["stream"]?]>): ServerLogLine[] {
  return items.map(([text, stream], i) => ({ t: NOW - at + i * 700, stream: stream ?? "stdout", text }));
}

/** server:web — Vite. */
export const webLog: ServerLogLine[] = lines(12.5 * MIN, [
  [`${DIM}[lux]${R} starting ${BOLD}web${R} in apps/web: npm run dev -- --host 0.0.0.0 --port 3000`],
  [""],
  ["> web-console@2.14.0 dev"],
  ["> vite --host 0.0.0.0 --port 3000"],
  [""],
  [`  ${GREEN}${BOLD}VITE${R} ${GREEN}v6.0.7${R}  ready in ${BOLD}812${R} ms`],
  [""],
  [`  ${GREEN}➜${R}  ${BOLD}Local:${R}   ${CYAN}http://localhost:3000/${R}`],
  [`  ${GREEN}➜${R}  ${BOLD}Network:${R} ${CYAN}http://10.42.7.19:3000/${R}`],
  [`  ${GREEN}➜${R}  press ${BOLD}h + enter${R} to show help`],
  [`${DIM}[lux]${R} ${BOLD}web${R} is ready: tcp :3000 answered after 1.4s`],
  [`${DIM}14:31:07${R} ${CYAN}${BOLD}[vite]${R} ${GREEN}hmr update${R} ${DIM}/src/checkout/PaymentStep.tsx${R}`],
  [`${DIM}14:31:09${R} ${CYAN}${BOLD}[vite]${R} ${GREEN}hmr update${R} ${DIM}/src/checkout/PaymentStep.module.css${R}`],
  [`${DIM}14:42:51${R} ${CYAN}${BOLD}[vite]${R} ${GREEN}hmr update${R} ${DIM}/src/checkout/useTotals.ts, /src/checkout/PaymentStep.tsx${R}`],
]);

/** server:api — Go, exited 1: the port was taken. */
export const apiExitedLog: ServerLogLine[] = lines(14 * MIN, [
  [`${DIM}[lux]${R} starting ${BOLD}api${R} in services/api: go run ./cmd/api --port 8080 --dev`],
  ["go: downloading github.com/jackc/pgx/v5 v5.7.2"],
  ["go: downloading github.com/go-chi/chi/v5 v5.2.0"],
  [`2025/09/28 14:29:41 ${BLUE}INFO${R} api starting ${DIM}port=8080 env=dev${R}`],
  [`2025/09/28 14:29:41 ${BLUE}INFO${R} migrations up to date ${DIM}version=0412${R}`],
  [`2025/09/28 14:29:42 ${RED}${BOLD}ERROR${R} listen tcp :8080: bind: address already in use`, "stderr"],
  ["exit status 1", "stderr"],
  [`${DIM}[lux]${R} ${BOLD}api${R} exited with code 1 after 1.3s`],
]);

export const apiStartingLog: ServerLogLine[] = lines(9_000, [
  [`${DIM}[lux]${R} starting ${BOLD}api${R} in services/api: go run ./cmd/api --port 8080 --dev`],
  ["go: downloading github.com/jackc/pgx/v5 v5.7.2"],
  ["go: downloading github.com/go-chi/chi/v5 v5.2.0"],
  [`2025/09/28 14:43:58 ${BLUE}INFO${R} api starting ${DIM}port=8080 env=dev${R}`],
  [`2025/09/28 14:43:58 ${BLUE}INFO${R} running migrations ${DIM}from=0409 to=0412${R}`],
]);

export const serverLogs: Record<string, ServerLogLine[]> = { web: webLog, api: apiStartingLog, storybook: [] };
export const serverLogsExited: Record<string, ServerLogLine[]> = { web: webLog, api: apiExitedLog, storybook: [] };
