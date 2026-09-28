/**
 * Servers fixtures: the mockups' scenarios a–f as the API would return
 * them (`TaskServers`, contract §C), so the gallery and the web app's dev
 * mode draw the same thing without a backend. Deterministic, anchored to
 * now so ages read sensibly.
 */

import type { Server, ServerLogLine, ServerRecipe, ServersRun, TaskServers } from "@dude/domain";

const NOW = Date.now();
const MIN = 60_000;
const iso = (agoMs: number) => new Date(NOW - agoMs).toISOString();

export const PREVIEW_DOMAIN = "lux.example.com";
const RUN_SUFFIX = "k3jq7x2mfa9vbn4z";
const PREVIEW_SUFFIX = "p8d2wq5nzr7m4kx1";
export const serverUrl = (name: string, suffix = RUN_SUFFIX) => `https://${name}-${suffix}.${PREVIEW_DOMAIN}`;

const ana = { id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: true };
const marcio = { id: "u_marcio", name: "Márcio Martins", photoUrl: null, online: true };

export const serverRecipes: ServerRecipe[] = [
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

function server(name: string, port: number, patch: Partial<Server> & { state: Server["state"] }, suffix = RUN_SUFFIX): Server {
  return {
    name,
    port,
    command: ["sh", "-c", `exec ${serverRecipes.find((r) => r.name === name)?.command ?? "true"}`],
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
export const serversRunning: TaskServers = { run: agentRun, servers: [webReady, apiStarting, storybookOff], moved: null, recipes: serverRecipes };

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
};

/** d: no run serves the task: the implementer finished and its run ended. */
export const serversNoRun: TaskServers = { run: null, servers: [], moved: null, recipes: serverRecipes };

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
};

/** f: a, with the preview of web open. */
export const serversPreviewOpen = serversRunning;

export const serverScenarios = {
  a: serversRunning,
  b: serversMigrated,
  c: serversExited,
  d: serversNoRun,
  e: serversPreviewBooting,
  f: serversPreviewOpen,
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

/** A page for the preview frame, so the gallery needs no server. */
export const PREVIEW_PAGE = `<!doctype html><html><head><meta charset="utf-8"><style>
  body{margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Inter,sans-serif;color:#1c2430;background:#f6f7f9}
  .top{display:flex;align-items:center;gap:16px;padding:0 28px;height:56px;background:#fff;border-bottom:1px solid #e6e8ec}
  .logo{font-weight:700;letter-spacing:-.01em;color:#0f172a;display:flex;align-items:center;gap:8px}.logo i{display:inline-block;width:22px;height:22px;border-radius:6px;background:linear-gradient(135deg,#2779bc,#63d99b)}
  .top nav{display:flex;gap:18px;color:#5b6472;font-size:14px}.top nav b{color:#0f172a;font-weight:600}
  .top .me{margin-left:auto;display:flex;align-items:center;gap:10px;font-size:13px;color:#5b6472}.me span{display:inline-grid;place-items:center;width:28px;height:28px;border-radius:50%;background:#d8ebfb;color:#19517b;font-weight:600;font-size:11px}
  .wrap{max-width:1040px;margin:32px auto;padding:0 28px;display:grid;grid-template-columns:1fr 360px;gap:28px}
  h1{font-size:22px;margin:0 0 4px}.sub{color:#5b6472;font-size:14px;margin:0 0 22px}
  .steps{display:flex;gap:8px;margin:0 0 22px;font-size:13px}.steps span{padding:6px 12px;border-radius:999px;background:#eef0f3;color:#5b6472}.steps span.on{background:#0f172a;color:#fff}.steps span.done{background:#d9f3e2;color:#06786c}
  .card{background:#fff;border:1px solid #e6e8ec;border-radius:10px;padding:20px 22px;margin-bottom:16px}
  .card h2{font-size:15px;margin:0 0 14px}
  .row{display:grid;grid-template-columns:1fr 1fr;gap:12px}.f{display:flex;flex-direction:column;gap:6px;font-size:12px;color:#5b6472}
  .f input{height:36px;border:1px solid #cfd4db;border-radius:6px;padding:0 10px;font:inherit;font-size:14px;color:#1c2430;background:#fff}
  .f input.err{border-color:#d33944;background:#fff7f7}.help{font-size:12px;color:#d33944}
  .split{display:flex;gap:10px;margin-bottom:14px}.split label{flex:1;display:flex;gap:10px;align-items:center;padding:12px 14px;border:1px solid #cfd4db;border-radius:8px;font-size:14px;cursor:pointer}.split label.on{border-color:#2779bc;box-shadow:0 0 0 2px #dceeff}
  .split small{display:block;color:#5b6472;font-size:12px}
  .btn{height:40px;padding:0 18px;border:0;border-radius:8px;background:#166daf;color:#fff;font:inherit;font-weight:600;font-size:14px}
  .ghost{background:transparent;color:#166daf}
  .sum dl{display:grid;grid-template-columns:1fr auto;gap:10px 16px;margin:0;font-size:14px}.sum dt{color:#5b6472}.sum dd{margin:0;text-align:right}.sum .tot{font-weight:700;font-size:16px;border-top:1px solid #e6e8ec;padding-top:12px}
  .tag{display:inline-block;font-size:11px;padding:2px 6px;border-radius:4px;background:#fae9ce;color:#7e580c;margin-left:8px;vertical-align:middle}
  .foot{display:flex;justify-content:flex-end;gap:10px;align-items:center}
  @media (max-width:700px){.wrap{grid-template-columns:1fr}}
</style></head><body>
<div class="top"><div class="logo"><i></i>Example</div><nav><span>Experiments</span><span>Goals</span><span>Segments</span><b>Billing</b></nav><div class="me">preview · marcio@example.com <span>MM</span></div></div>
<div class="wrap"><div>
  <h1>Upgrade to Scale <span class="tag">checkout v2</span></h1><p class="sub">Step 2 of 3 — Payment</p>
  <div class="steps"><span class="done">✓ Plan</span><span class="on">Payment</span><span>Review</span></div>
  <div class="card"><h2>Payment method</h2>
    <div class="split"><label class="on"><input type="radio" checked> <span>Card<small>Visa, Mastercard, Amex</small></span></label><label><input type="radio"> <span>SEPA direct debit<small>EUR accounts, 2–3 days</small></span></label><label><input type="radio"> <span>Invoice<small>Annual plans only</small></span></label></div>
    <div class="row"><div class="f">Name on card<input value="Márcio Martins"></div><div class="f">Card number<input class="err" value="4242 4242 4242 42"><span class="help">Card number is incomplete.</span></div></div>
    <div class="row" style="margin-top:12px"><div class="f">Expiry<input value="08 / 28"></div><div class="f">CVC<input value="•••"></div></div>
  </div>
  <div class="card"><h2>Billing address</h2><div class="row"><div class="f">Company<input value="Example, Lda"></div><div class="f">VAT number<input value="PT 515 0…"></div></div></div>
  <div class="foot"><button class="btn ghost">← Back to plan</button><button class="btn">Continue to review</button></div>
</div>
<div class="card sum"><h2>Order summary</h2><dl><dt>Scale · monthly</dt><dd>€1,200.00</dd><dt>Extra seats × 4</dt><dd>€160.00</dd><dt>VAT 23%</dt><dd>€312.80</dd><dt class="tot">Due today</dt><dd class="tot">€1,672.80</dd></dl><p style="font-size:12px;color:#5b6472;margin:14px 0 0">Renews 28 Oct 2025. Cancel any time.</p></div>
</div></body></html>`;
