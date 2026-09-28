/**
 * The mockups' world, as the API would return it: one project, one epic,
 * the task WC-214 and its runs, the implementer's conversation, and the
 * servers in each of the scenarios a–f. For `FixtureClient`, so the
 * screens can be seen without a backend. Deterministic, anchored to now.
 */

import type { NavProject } from "@dude/design-system";
import { MIN, iso, serverLogs, serverLogsExited, serverScenarios, serverRecipes, type ServerScenario } from "@dude/design-system/fixtures/servers";
import type { ServerLogLine } from "@dude/design-system";
import type { Finding, PersistedEvent, PullRequest, Run, SettingsResponse, Task, TaskServers } from "@dude/domain";
import type { Member, ProjectDetail, RunDetail, TaskDetail, TaskMetrics } from "../api/client.ts";

export const ORG = { id: "org_example", name: "Example" };

export const PEOPLE: Member[] = [
  { id: "u_marcio", name: "Márcio Martins", photoUrl: null, online: true, email: "marcio@example.com", role: "admin", lastSeenAt: iso(0), lastSeenWhere: "WC-214" },
  { id: "u_ana", name: "Ana Ribeiro", photoUrl: null, online: true, email: "ana@example.com", role: "member", lastSeenAt: iso(3 * MIN), lastSeenWhere: "WC-212" },
  { id: "u_tom", name: "Tom Okafor", photoUrl: null, online: true, email: "tom@example.com", role: "member", lastSeenAt: iso(8 * MIN), lastSeenWhere: null },
  { id: "u_lin", name: "Lin Zhao", photoUrl: null, online: false, email: "lin@example.com", role: "member", lastSeenAt: iso(3 * 60 * MIN), lastSeenWhere: null },
  { id: "u_sam", name: "sam.delgado", photoUrl: null, online: false, email: "sam@example.com", role: "member", lastSeenAt: iso(26 * 60 * MIN), lastSeenWhere: null },
  { id: "u_kai", name: "Kai Nakamura", photoUrl: null, online: false, email: "kai@example.com", role: "member", lastSeenAt: iso(2 * 60 * MIN), lastSeenWhere: null },
];
export const YOU = "u_marcio";
const ref = (id: string) => {
  const p = PEOPLE.find((x) => x.id === id)!;
  return { id: p.id, name: p.name, photoUrl: p.photoUrl, online: p.online };
};

export const PROJECT: ProjectDetail = {
  id: "p_webconsole",
  organizationId: ORG.id,
  name: "web-console",
  slug: "web-console",
  description: "",
  repositories: [
    { id: "repo_wc", projectId: "p_webconsole", name: "web-console", url: "https://github.com/example/web-console.git", defaultBranch: "main", trust: "trusted_internal" },
    { id: "repo_sdk", projectId: "p_webconsole", name: "sdk-js", url: "https://github.com/example/sdk-js.git", defaultBranch: "main", trust: "trusted_internal" },
  ],
  agentModels: {},
  runtimeImage: "ghcr.io/example/runner:node22-go1.23",
  deliveryPolicy: {},
  createdAt: iso(60 * 24 * 60 * MIN),
  imageUrl: null,
};

export const EPIC = { id: "e_checkout", title: "Checkout v2" };
export const TASK_ID = "tsk_01j9x4kqf8b2m7e3";
export const RUN_ID = "run_01j9x5m2q7k8e4t1";
const BRANCH = "dude/tsk_01j9x4kq/checkout-v2-split-payment";

function run(patch: Partial<Run> & { id: string; phase: Run["phase"]; role: Run["role"]; status: Run["status"] }): Run {
  return {
    organizationId: ORG.id, projectId: PROJECT.id, taskId: TASK_ID, attempt: 1, workerId: null, workspacePath: null, error: null,
    category: null, parentRunId: null, baseRefs: { "web-console": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0" }, heads: {},
    branch: BRANCH, harness: "opencode", model: "claude-sonnet-4.5", dudePause: null,
    tokens: { input: 380_000, output: 32_000, cacheRead: 0, cacheWrite: 0, context: 118_200 },
    createdAt: iso(40 * MIN), startedAt: iso(38 * MIN), endedAt: null,
    ...patch,
  };
}

/** The task with its implementer at work (a, b, c, e, f). */
export const RUN_IMPLEMENT = run({ id: RUN_ID, phase: "implement", role: "implementer", status: "running", heads: { "web-console": "3f2a9c1d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b" } });

/** The task in review with a pull request open (d): every phase done. */
const RUNS_REVIEW: Run[] = [
  run({ id: "run_d1", phase: "implement", role: "implementer", status: "completed", createdAt: iso(200 * MIN), startedAt: iso(199 * MIN), endedAt: iso(147 * MIN), heads: { "web-console": "3f2a9c1d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b" } }),
  run({ id: "run_d2", phase: "review", role: "reviewer", category: "correctness", status: "completed", createdAt: iso(146 * MIN), startedAt: iso(146 * MIN), endedAt: iso(136 * MIN), heads: { "web-console": "3f2a9c1d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b" } }),
  run({ id: "run_d3", phase: "fix", role: "implementer", status: "completed", createdAt: iso(135 * MIN), startedAt: iso(135 * MIN), endedAt: iso(121 * MIN), heads: { "web-console": "8be1d40a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e" } }),
  run({ id: "run_d4", phase: "review", role: "reviewer", category: "correctness", status: "completed", createdAt: iso(120 * MIN), startedAt: iso(120 * MIN), endedAt: iso(114 * MIN), heads: { "web-console": "8be1d40a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e" } }),
  run({ id: "run_d5", phase: "simplify", role: "simplifier", status: "completed", createdAt: iso(113 * MIN), startedAt: iso(113 * MIN), endedAt: iso(105 * MIN), heads: { "web-console": "c91f7e2b3c4d5e6f7a8b9c0d1e2f3a4b5c6d7e8f" } }),
];

const TASK_BASE = {
  id: TASK_ID,
  organizationId: ORG.id,
  projectId: PROJECT.id,
  epicId: EPIC.id,
  key: "WC-214",
  repositories: [{ id: "repo_wc", access: "write" as const }],
  title: "Checkout v2: split payment step",
  goal: "Split the checkout's single \"Details & payment\" step into two: billing details first, then payment, so a customer can come back to a half-finished upgrade without re-entering their card.",
  acceptanceCriteria: [
    "Payment step keeps Card, SEPA and Invoice, with the invoice option only for annual plans.",
    "The old single-step flow stays behind checkout_v2 for a week.",
    "Funnel events fire per step.",
  ],
  requestedBy: YOU,
  owner: ref("u_marcio"),
  people: [ref("u_marcio"), ref("u_ana")],
  createdAt: iso(2 * 24 * 60 * MIN),
  updatedAt: iso(5 * MIN),
};

export function taskFor(scenario: ServerScenario): TaskDetail {
  const inReview = scenario === "d";
  const status: Task["status"] = inReview ? "review" : "running";
  return { ...TASK_BASE, status, runs: inReview ? RUNS_REVIEW : [RUN_IMPLEMENT], escalation: null } as TaskDetail;
}

export const PULL_REQUEST: PullRequest = {
  id: "pr_482",
  taskId: TASK_ID,
  runId: "run_d5",
  repositoryId: "repo_wc",
  repositoryName: "example/web-console",
  number: 482,
  url: "https://github.com/example/web-console/pull/482",
  headBranch: BRANCH,
  baseBranch: "main",
  title: "WC-214: split payment step",
  state: "open",
  checks: [
    { name: "build", status: "completed", conclusion: "success", durationMs: 184_000 },
    { name: "unit", status: "completed", conclusion: "success", durationMs: 92_000 },
    { name: "e2e", status: "in_progress", conclusion: null },
    { name: "lint", status: "queued", conclusion: null },
  ],
  checkState: "pending",
  review: "pending",
  reviews: [{ login: "ana-ribeiro", state: "REQUESTED", submittedAt: null }],
  mergeable: "clean",
  behindBy: 0,
  unresolvedThreads: 0,
  display: "ci_running",
  createdAt: iso(100 * MIN),
  updatedAt: iso(4 * MIN),
};

export const FINDINGS: Finding[] = [
  { id: "f1", taskId: TASK_ID, runId: "run_d2", category: "correctness", severity: "blocking", status: "resolved", file: "apps/web/src/checkout/PaymentStep.tsx", line: 48, title: "Invoice option shown on monthly plans", description: "The gate reads plan.interval before it is loaded.", suggestedFix: "Gate on the resolved plan.", resolutionNote: "Fixed in 8be1d40.", resolvedByRunId: "run_d4", fixAttempts: 1, createdAt: iso(140 * MIN) },
  { id: "f2", taskId: TASK_ID, runId: "run_d2", category: "correctness", severity: "medium", status: "resolved", file: "apps/web/src/checkout/useTotals.ts", line: 12, title: "VAT recomputed on every render", description: "useTotals recalculates without memoising.", suggestedFix: "useMemo on the inputs.", resolutionNote: "", resolvedByRunId: "run_d4", fixAttempts: 1, createdAt: iso(140 * MIN) },
  { id: "f3", taskId: TASK_ID, runId: "run_d2", category: "correctness", severity: "low", status: "accepted", file: null, line: null, title: "Funnel event names differ from the analytics plan", description: "step_2 vs payment_step.", suggestedFix: "", resolutionNote: "Analytics will map them.", resolvedByRunId: null, fixAttempts: 0, createdAt: iso(140 * MIN) },
  { id: "f4", taskId: TASK_ID, runId: "run_d2", category: "correctness", severity: "note", status: "open", file: "apps/web/src/checkout/PaymentStep.module.css", line: 3, title: "Unused class .legacy", description: "", suggestedFix: "", resolutionNote: "", resolvedByRunId: null, fixAttempts: 0, createdAt: iso(140 * MIN) },
];

export function navigationFor(scenario: ServerScenario): NavProject[] {
  const inReview = scenario === "d";
  const task = taskFor(scenario);
  const current = {
    id: task.runs[0]!.id,
    attempt: 1,
    status: inReview ? ("completed" as const) : ("running" as const),
    sessions: task.runs.map((r) => ({ id: r.id, role: r.role ?? "orchestrator", status: r.status === "running" ? ("running" as const) : ("completed" as const), title: r.phase === "review" ? "Review" : r.phase ? r.phase[0]!.toUpperCase() + r.phase.slice(1) : "Agent", ...(r.status === "running" ? { activity: "Running npm test" } : {}) })),
  };
  const P = Object.fromEntries(PEOPLE.map((p) => [p.id, { id: p.id, name: p.name, online: p.online }]));
  return [
    {
      id: PROJECT.id,
      name: PROJECT.name,
      colorSlot: 5,
      epics: [
        {
          id: EPIC.id,
          title: EPIC.title,
          tasks: [
            { id: "t_211", key: "WC-211", title: "Cart summary: show tax breakdown", status: "done", people: [P["u_ana"]!], statusSince: iso(3 * 24 * 60 * MIN), costUsd: 1.2 },
            { id: "t_212", key: "WC-212", title: "Address form: autocomplete with Places", status: "review", people: [P["u_tom"]!, P["u_ana"]!], statusSince: iso(5 * 60 * MIN), costUsd: 2.9,
              pullRequests: [{ ...PULL_REQUEST, number: 480, display: "ci_running" }] },
            { id: TASK_ID, key: "WC-214", title: task.title, status: task.status, people: [P["u_marcio"]!, P["u_ana"]!], statusSince: iso(40 * MIN), costUsd: 2.41, runs: [current],
              ...(inReview ? { pullRequests: [PULL_REQUEST] } : {}) },
            { id: "t_215", key: "WC-215", title: "Payment step: Apple Pay fallback", status: "awaiting_input", people: [P["u_lin"]!], statusSince: iso(20 * MIN), costUsd: 0.8, waitingFor: "the implementer asked which wallet to prefer" },
            { id: "t_216", key: "WC-216", title: "Order confirmation e-mail template", status: "queued", people: [P["u_sam"]!], statusSince: iso(2 * 60 * MIN) },
            { id: "t_217", key: "WC-217", title: "Analytics: checkout funnel events", status: "received", statusSince: iso(30 * MIN) },
          ],
        },
        { id: "e_experiments", title: "Experiments UI", tasks: [
          { id: "t_203", key: "WC-203", title: "Variant editor: JSON view", status: "running", people: [P["u_kai"]!], statusSince: iso(12 * MIN), costUsd: 0.4 },
          { id: "t_204", key: "WC-204", title: "Segment picker: search", status: "done", people: [P["u_kai"]!], statusSince: iso(3 * 24 * 60 * MIN) },
          { id: "t_205", key: "WC-205", title: "Goal weights", status: "queued", statusSince: iso(6 * 60 * MIN) },
          { id: "t_206", key: "WC-206", title: "Experiment archive", status: "received", statusSince: iso(60 * MIN) },
        ] },
      ],
      tasks: [],
    },
    { id: "p_sdkjs", name: "sdk-js", colorSlot: 2, epics: [], tasks: [{ id: "t_s1", key: "SDK-31", title: "Retry on 429", status: "running", people: [P["u_tom"]!], statusSince: iso(50 * MIN) }] },
    { id: "p_api", name: "api", colorSlot: 3, epics: [], tasks: [{ id: "t_a1", key: "API-9", title: "Rate limits per key", status: "awaiting_input", people: [P["u_lin"]!, P["u_ana"]!], statusSince: iso(15 * MIN), waitingFor: "the plan needs confirming" }] },
  ];
}

export const METRICS: TaskMetrics = {
  leadMs: 2 * 24 * 60 * MIN,
  activeMs: 38 * MIN,
  humanWaitMs: 0,
  reviewMs: 0,
  costUsd: 2.41,
  cost: { totalUsd: 2.41, tokensUsd: 2.28, machineUsd: 0.13 },
  tokens: { input: 380_000, output: 32_000 },
  runs: [{ id: RUN_ID, phase: "implement", role: "implementer", category: null, status: "running", activeMs: 38 * MIN, parkedMs: 0, costUsd: 2.41, cost: { totalUsd: 2.41, tokensUsd: 2.28, machineUsd: 0.13 }, tokens: { input: 380_000, output: 32_000 } }],
};

export const SETTINGS: SettingsResponse = {
  organization: ORG,
  project: { id: PROJECT.id, name: PROJECT.name },
  roles: Object.fromEntries((["implementer", "reviewer", "fixer", "simplifier", "qa_browser"] as const).map((role) => [role, {
    model: { value: "anthropic/claude-sonnet-4.5", source: "organization" },
    effort: { value: null, source: "organization" },
    timeLimitMinutes: { value: null, source: "organization" },
    enabled: role === "simplifier" || role === "qa_browser" ? { value: role === "simplifier", source: "organization" } : null,
    prompt: { organization: { versionId: null, body: "", updatedAt: null, updatedBy: null, versions: 0 }, project: { versionId: null, body: "", updatedAt: null, updatedBy: null, versions: 0, mode: "inherit" } },
  }])) as SettingsResponse["roles"],
  delivery: {
    requiredReviewers: { value: ["correctness"], source: "organization" },
    blockingSeverities: { value: ["blocking", "high"], source: "organization" },
    maxReviewIterations: { value: 5, source: "organization" },
    maxAttemptsPerFinding: { value: 3, source: "organization" },
    maxPrFixIterations: { value: 5, source: "organization" },
    simplify: { value: true, source: "organization" },
    test: { value: false, source: "organization" },
    parkAfterMinutes: { value: 10, source: "organization" },
    idleNudgeMinutes: { value: 0, source: "organization" },
  },
  canEdit: true,
};

export function runDetailFor(scenario: ServerScenario): RunDetail {
  const r = scenario === "d" ? RUNS_REVIEW[0]! : RUN_IMPLEMENT;
  return { ...r, sessions: [{ id: `${r.id}-s`, organizationId: ORG.id, runId: r.id, parentSessionId: null, role: "implementer", harness: "opencode", model: "claude-sonnet-4.5", status: r.status === "running" ? "running" : "completed", externalSessionId: null, createdAt: r.createdAt, endedAt: r.endedAt }] };
}

// -- The implementer's conversation, as the ledger has it -------------------

let cursor = 0;
function event(agoMs: number, eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = { type: "agent", id: "implementer" }, runId: string | null = RUN_ID): PersistedEvent {
  cursor += 1;
  return {
    eventId: `evt_${cursor.toString().padStart(4, "0")}`,
    eventType,
    occurredAt: iso(agoMs),
    organizationId: ORG.id,
    projectId: PROJECT.id,
    taskId: TASK_ID,
    runId,
    sessionId: runId ? `${runId}-s` : null,
    workflowRunId: "wf_01",
    actor,
    source: actor.type === "human" ? "web" : actor.type === "system" ? "control-plane" : "harness",
    correlationId: null,
    causationId: null,
    payload,
    cursor,
  };
}
const me = { type: "human" as const, id: YOU, name: "Márcio Martins" };
const dude = { type: "system" as const, id: "dude" };

const PROMPT = `You are the **implementer** for task **WC-214** in \`example/web-console\`.

**Goal.** Split the checkout's single "Details & payment" step into two: billing details first, then payment, so a customer can come back to a half-finished upgrade without re-entering their card.

**Acceptance criteria.** Payment step keeps Card, SEPA and Invoice, with the invoice option only for annual plans. The old flow stays behind \`checkout_v2\` for a week. Funnel events fire per step.

**How to work.** Read before you write. Make the smallest change that fully does the task. Run \`npm test -- checkout\` before you finish.`;

const PLAN = (done: number) => ({
  todos: [
    { content: "Map DetailsAndPayment and the totals hook", status: done > 0 ? "completed" : "in_progress", priority: "high" },
    { content: "Split the form state into BillingStep and PaymentStep", status: done > 1 ? "completed" : done === 1 ? "in_progress" : "pending", priority: "high" },
    { content: "Gate the route on checkout_v2", status: done > 2 ? "completed" : done === 2 ? "in_progress" : "pending", priority: "medium" },
    { content: "Wire PaymentStep to the new totals hook", status: done > 3 ? "completed" : done === 3 ? "in_progress" : "pending", priority: "medium" },
    { content: "Funnel events per step, then npm test -- checkout", status: "pending", priority: "medium" },
  ],
});

export function eventsFor(scenario: ServerScenario): PersistedEvent[] {
  cursor = 0;
  const apiExited = scenario === "c";
  const out: PersistedEvent[] = [
    event(2 * 24 * 60 * MIN, "task.created", { title: TASK_BASE.title }, me, null),
    event(40 * MIN, "run.created", { phase: "implement", role: "implementer" }, dude),
    event(38 * MIN, "run.started", {}, dude),
    event(38 * MIN - 2000, "agent.prompt.delivered", { text: PROMPT }, dude),
    event(37.5 * MIN, "agent.plan.updated", PLAN(0)),
    event(37.4 * MIN, "agent.message", { text: "Reading the current step first. `apps/web/src/checkout/DetailsAndPayment.tsx` holds both forms in one `useForm`; the totals come from `useCart()` directly. I will split the form state, add a `PaymentStep` and put the route behind the flag.", contextTokens: 24_100 }),
    event(37.3 * MIN, "agent.thought", { text: "The flag should gate the route, not the component: a component-level gate would leave the old step mounted under the new URL and double the funnel events." }),
    event(37 * MIN, "agent.tool.called", { tool: "bash", callId: "c1", input: { command: "npm test -- checkout" }, title: "npm test -- checkout" }),
    event(36.2 * MIN, "agent.tool.completed", { tool: "bash", callId: "c1", status: "completed", title: "npm test -- checkout", exitCode: 0, output: { head: "\n> web-console@2.14.0 test\n> vitest run checkout\n\n ✓ src/checkout/DetailsAndPayment.test.tsx (6)\n ✓ src/checkout/useCart.test.ts (3)\n\n Test Files  2 passed (2)\n      Tests  9 passed (9)\n   Duration  4.12s\n" } }),
    event(36 * MIN, "agent.tool.called", { tool: "edit", callId: "c2", input: { file_path: "apps/web/src/checkout/PaymentStep.tsx" }, title: "PaymentStep.tsx" }),
    event(36 * MIN - 1200, "agent.tool.completed", { tool: "edit", callId: "c2", status: "completed", title: "PaymentStep.tsx" }),
    event(35.9 * MIN, "agent.model.request.completed", { costUsd: 0.41, contextTokens: 31_200, contextWindow: 200_000, tokens: { input: 31_000, output: 1_400 }, turn: true }),
    event(35 * MIN, "agent.plan.updated", PLAN(2)),
    event(14.5 * MIN, "run.steered", { text: "Start the web dev server so I can click through the new step while you work. Keep the invoice option hidden on monthly plans.", directiveId: "dir_1" }, me),
    event(14.5 * MIN - 2000, "run.directive.delivered", { directiveId: "dir_1" }, dude),
    event(14.3 * MIN, "agent.tool.called", { tool: "lux.server.start", callId: "c3", input: { name: "web", command: "npm run dev -- --host 0.0.0.0 --port 3000" }, title: "web" }),
    event(14.3 * MIN - 1400, "agent.tool.completed", { tool: "lux.server.start", callId: "c3", status: "completed", title: "web", output: { head: "web: starting in apps/web\nweb: ready at :3000 after 1.4s\n" } }),
    event(14.2 * MIN, "agent.tool.called", { tool: "lux.server.start", callId: "c4", input: { name: "api", command: "go run ./cmd/api --port 8080 --dev" }, title: "api" }),
    event(14.2 * MIN - 1300, "agent.tool.completed", { tool: "lux.server.start", callId: "c4", status: apiExited ? "error" : "completed", title: "api", ...(apiExited ? { exitCode: 1, output: { head: "api: starting in services/api\nlisten tcp :8080: bind: address already in use\nexit status 1\n" } } : { output: { head: "api: starting in services/api\n" } }) }),
    event(14.1 * MIN, "agent.message", { text: `Started \`web\` on this run — it is at ${serverScenarios.a.servers[0]!.url}, the new step is under \`/billing/upgrade/payment\`. \`api\` is ${apiExited ? "not up: its port is taken by my earlier `go run`, which I will stop" : "starting too so the totals are real"}. The invoice option is now gated on \`plan.interval === "year"\`.`, contextTokens: 118_200 }),
    event(14 * MIN, "agent.model.request.completed", { costUsd: 0.62, contextTokens: 118_200, contextWindow: 200_000, tokens: { input: 118_000, output: 1_900 }, turn: true }),
    event(13.9 * MIN, "agent.plan.updated", PLAN(3)),
    event(0.4 * MIN, "agent.message", { text: "Wiring `PaymentStep` to the new `useTotals` hook so VAT updates when the billing country changes. Running the checkout tests again after.", contextTokens: 121_000 }),
    event(0.3 * MIN, "agent.tool.called", { tool: "bash", callId: "c5", input: { command: "npm test -- checkout --watch=false" }, title: "npm test -- checkout" }),
  ];
  if (scenario === "d") {
    // Everything finished: the run ended, a pull request opened.
    return [
      out[0]!,
      event(200 * MIN, "run.created", { phase: "implement", role: "implementer" }, dude, "run_d1"),
      event(147 * MIN, "run.completed", {}, dude, "run_d1"),
      event(146 * MIN, "run.created", { phase: "review", role: "reviewer" }, dude, "run_d2"),
      event(136 * MIN, "run.completed", {}, dude, "run_d2"),
      event(135 * MIN, "run.created", { phase: "fix", role: "implementer" }, dude, "run_d3"),
      event(121 * MIN, "run.completed", {}, dude, "run_d3"),
      event(120 * MIN, "run.created", { phase: "review", role: "reviewer" }, dude, "run_d4"),
      event(114 * MIN, "run.completed", {}, dude, "run_d4"),
      event(113 * MIN, "run.created", { phase: "simplify", role: "simplifier" }, dude, "run_d5"),
      event(105 * MIN, "run.completed", {}, dude, "run_d5"),
      event(100 * MIN, "pull_request.opened", { number: 482, repo: "example/web-console", url: PULL_REQUEST.url, title: PULL_REQUEST.title }, dude, null),
    ];
  }
  return out;
}

// -- Servers: the scenarios, copied so the fixture client can change them ------

export function serversFor(scenario: ServerScenario): TaskServers {
  const base = serverScenarios[scenario];
  return { ...base, run: base.run ? { ...base.run, id: RUN_ID } : null, servers: base.servers.map((s) => ({ ...s })), recipes: [...serverRecipes] };
}

export function logsFor(scenario: ServerScenario): Record<string, ServerLogLine[]> {
  return scenario === "c" ? serverLogsExited : serverLogs;
}
