/**
 * The API without a backend: the mockups' world, answered from memory.
 * For seeing the screens and the gallery's scenarios in the real app —
 * `?fixtures=a` … `e` (or `dude.fixtures` in localStorage), outside
 * production builds, which never load this module. Reads come from
 * `data.ts`; writes to servers change what the next read returns, so
 * Start, Stop and Preview branch move things as a backend would, if a
 * little faster. `dude.fixtures.run` in localStorage turns the task's run
 * paused, or into a branch preview (no agent), or stopped — aborted, failed
 * or started over — for the screens' other states.
 */

import type { ServerScenario } from "@dude/design-system/fixtures/servers";
import { PREVIEW_DOMAIN, RUN_SUFFIX, previewEgress, previewRun, server, serverRecipes } from "@dude/design-system/fixtures/servers";
import type { NavProject } from "@dude/design-system";
import { canStart, canStop } from "@dude/design-system";
import type { AddServer, PersistedEvent, PreviewSecret, PreviewSettings, Recipe, RecipeInput, RunServer, SettingsResponse, TaskServers } from "@dude/domain";
import { egressProblem, secretHint, secretNameProblem } from "@dude/domain";
import { RUN_KEY } from "./scenario.ts";
import type { ServerLogLine } from "@dude/design-system";
import { ApiClient, ApiError, type Artifact, type Member, type ProjectDetail, type RecoverAction, type RecoveryOptions, type ReviewerCandidate, type Run, type RunDetail, type TaskDetail, type TaskMetrics } from "../api/client.ts";
import { EPIC, FINDINGS, MACHINE_SIZES, METRICS, MODEL_TIERS, ORG, PEOPLE, PROJECT, PULL_REQUEST, RESTART, RESTARTED_ARTIFACTS, RESTARTED_FINDINGS, RESTARTED_PULL_REQUESTS, RESTARTED_RUNS, REVIEWERS, RUN_ID, RUN_IMPLEMENT, SETTINGS, TASK_ID, YOU, eventsFor, logsFor, navigationFor, restartedMetrics, runDetailFor, serversFor, taskFor } from "./data.ts";

type LedgerQuery = { runId?: string | undefined; taskId?: string | undefined; after?: number | undefined };
export type { LedgerQuery };

/**
 * An EventSource over the fixtures: on open it replays the scope's ledger
 * (the backfill the real stream sends), then stays quiet until the fixture
 * client changes something, when it sends the `servers.changed` a backend
 * would — so the screens re-read through the same path they do for real.
 */
const streams = new Set<QuietEventSource>();
let streamCursor = 1_000_000;
let ledger: ((params: LedgerQuery) => PersistedEvent[]) | null = null;
class QuietEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = 0;
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  #opening: ReturnType<typeof setTimeout>;
  constructor(readonly url: string) {
    super();
    streams.add(this);
    const q = new URL(url, window.location.origin).searchParams;
    // Opens a moment later, as a socket would; closed before then, it never
    // opens (StrictMode mounts, unmounts and mounts again).
    this.#opening = setTimeout(() => {
      if (this.readyState !== 0) return;
      this.readyState = 1;
      this.onopen?.(new Event("open"));
      if (q.get("live") || !ledger) return;
      const params = { ...(q.get("runId") ? { runId: q.get("runId")! } : {}), ...(q.get("taskId") ? { taskId: q.get("taskId")! } : {}), after: Number(q.get("after") ?? 0) };
      for (const e of ledger(params)) this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(e) }));
    }, 20);
  }
  close() {
    clearTimeout(this.#opening);
    this.readyState = 2;
    streams.delete(this);
  }
}

/** Send an event down every open fixture stream, as the backend would. */
export function emit(event: Omit<PersistedEvent, "cursor" | "eventId">): void {
  const cursor = ++streamCursor;
  const data = JSON.stringify({ ...event, cursor, eventId: `evt_fx_${cursor}` });
  for (const s of streams) if (s.readyState === 1) s.onmessage?.(new MessageEvent("message", { data }));
}

/** Install the quiet stream for the fixtures; call once, before the app mounts. */
export function installFixtureStream(): void {
  (window as unknown as { EventSource: unknown }).EventSource = QuietEventSource;
}

const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));
const later = (fn: () => void, ms: number) => void setTimeout(fn, ms);

export class FixtureClient extends ApiClient {
  #scenario: ServerScenario;
  #servers: TaskServers;
  #logs: Record<string, ServerLogLine[]>;
  #recipes: Recipe[];
  #previews: PreviewSettings;
  /** Names and hints only: the fixture forgets a value once it has its hint, as the API never returns one. */
  #secrets: PreviewSecret[];
  #task: TaskDetail;
  #events: PersistedEvent[];
  #nav: NavProject[];
  #runPatch: Partial<RunDetail> = {};

  constructor(scenario: ServerScenario) {
    super({ apiKey: "fixtures" });
    this.#scenario = scenario;
    this.#servers = serversFor(scenario);
    this.#logs = logsFor(scenario);
    this.#recipes = [...serverRecipes];
    this.#previews = { image: null, imageId: null, egress: [...previewEgress], idleTimeoutMinutes: 15, machineSize: null };
    this.#secrets = [
      { name: "DATABASE_SEED_URL", hint: "8a1c", updatedAt: new Date(Date.now() - 14 * 864e5).toISOString(), updatedBy: { id: PEOPLE[1]?.id ?? YOU, name: PEOPLE[1]?.name ?? PEOPLE[0]!.name } },
      { name: "SEED_LLM_KEY", hint: "3f9a", updatedAt: new Date(Date.now() - 2 * 36e5).toISOString(), updatedBy: { id: YOU, name: PEOPLE[0]!.name } },
    ];
    const base = taskFor(scenario);
    this.#task = base;
    this.#events = eventsFor(scenario);
    const as = localStorage.getItem(RUN_KEY);
    if (as === "paused" || as === "preview") {
      const patch: Partial<RunDetail> = as === "paused" ? { status: "paused" } : { kind: "preview", phase: null, role: null };
      this.#runPatch = patch;
      this.#task = { ...this.#task, runs: this.#task.runs.map((r) => (r.id === RUN_ID ? { ...r, ...patch } : r)) };
      // A preview has no agent: nothing of the implementer's conversation.
      if (as === "preview") this.#events = this.#events.filter((e) => e.runId !== RUN_ID || e.eventType === "run.created" || e.eventType === "run.started");
    }
    if (as === "aborted" || as === "failed" || as === "restarted") this.#stop(as);
    this.#nav = navigationFor(scenario);
    // The tree and the board say what the task's page does.
    if (this.#task.status !== base.status) this.#nav = this.#nav.map((p) => ({ ...p,
      epics: p.epics?.map((e) => ({ ...e, tasks: e.tasks.map((t) => (t.id === TASK_ID ? { ...t, status: this.#task.status, runs: undefined } : t)) })) }));
    ledger = (params) => this.ledgerFor(params);
  }

  /**
   * `dude.fixtures.run` = aborted | failed: the implementer stopped (by Ana,
   * or its host lost) and the task with it, kept to resume. `restarted`: a
   * task started over (data.ts's RESTART): attempt 1 ran its pipeline,
   * opened a pull request and was stopped at its fix by Ana; attempt 2's
   * fixer is at work, its own pull request open.
   */
  #stop(as: "aborted" | "failed" | "restarted") {
    if (as === "restarted") return this.#restart();
    const at = new Date(Date.now() - 95 * 60_000).toISOString();
    const stopped = { ...RUN_IMPLEMENT, status: as === "failed" ? "failed" : "aborted", endedAt: at, branch: "dude/task_wc214/attempt-1",
      error: as === "failed" ? "the agent's run ended before finishing its task: host lost" : null } as Run;
    this.#runPatch = { status: stopped.status, endedAt: at, error: stopped.error };
    this.#task = { ...this.#task, status: as === "failed" ? "failed" : "aborted", runs: [stopped] };
    this.#recovery = { taskId: TASK_ID, actions: ["resume", "retry", "restart"], attempt: 1,
      keptUntil: new Date(Date.now() + 6 * 24 * 3600_000).toISOString() };
    const ana = { type: "human" as const, id: "u_ana", name: "Ana Ribeiro" };
    const extra: PersistedEvent[] = [];
    const push = (eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"], runId: string | null) =>
      extra.push({ ...this.#events[0]!, cursor: 10_000 + extra.length, eventId: `evt_stop_${extra.length}`, eventType,
        occurredAt: at, runId, actor, payload });
    if (as === "failed") push("run.failed", { status: "failed", error: stopped.error }, { type: "system", id: "dude" }, RUN_ID);
    else push("run.aborted", { reason: "It's rewriting the checkout's routing — that's not what we asked for." }, ana, RUN_ID);
    this.#events = [...this.#events, ...extra];
  }

  /** The task started over: two attempts, each with its Runs, pull request, findings and files, and the ledger of both. */
  #restart() {
    this.#restarted = true;
    this.#task = { ...this.#task, status: "running", runs: RESTARTED_RUNS };
    const minAgo = (m: number) => new Date(Date.now() - m * 60_000).toISOString();
    const ana = { type: "human" as const, id: "u_ana", name: "Ana Ribeiro" };
    const me = { type: "human" as const, id: YOU, name: "Márcio Martins" };
    const dude = { type: "system" as const, id: "dude" };
    const agent = (id: string) => ({ type: "agent" as const, id });
    // [when, type, payload, actor, Run]: the persons' acts on no Run fall on either side of the start over.
    type Act = [string, string, Record<string, unknown>, PersistedEvent["actor"], string | null];
    const acts: Act[] = [
      ...RESTARTED_RUNS.map((r): Act => [r.createdAt, "run.created", { phase: r.phase, role: r.role }, dude, r.id]),
      [minAgo(170), "artifact.created", { artifactId: "art_a1_notes", name: "notes/routing.md" }, agent(RUN_ID), RUN_ID],
      [minAgo(141.5), "review.completed", { phase: "review", count: 3 }, agent("run_a1_review"), "run_a1_review"],
      [minAgo(129), "pull_request.opened", { number: 478, repo: "example/web-console", url: RESTARTED_PULL_REQUESTS[0]!.url }, dude, null],
      [minAgo(112), "task.decided", { action: "retry", note: "Fix the CI first." }, me, null],
      [minAgo(97), "run.aborted", { reason: RESTART.abortReason }, ana, "run_a1_fix"],
      [minAgo(RESTART.setAsideMin), "task.recovered", { action: "restart", attempt: 2, note: RESTART.note }, me, null],
      [minAgo(RESTART.setAsideMin - 0.1), "pull_request.closed", { number: 478, repo: "example/web-console" }, dude, null],
      [minAgo(60), "artifact.created", { artifactId: "art_a2_notes", name: "notes/form-split.md" }, agent("run_attempt2"), "run_attempt2"],
      [minAgo(50), "task.owner_changed", { to: YOU }, ana, null],
      [minAgo(41.5), "review.completed", { phase: "review", count: 0 }, agent("run_a2_review"), "run_a2_review"],
      [minAgo(31), "pull_request.opened", { number: 483, repo: "example/web-console", url: RESTARTED_PULL_REQUESTS[1]!.url }, dude, null],
    ];
    const extra = acts.sort((a, b) => a[0].localeCompare(b[0])).map(([occurredAt, eventType, payload, actor, runId], i): PersistedEvent => ({
      ...this.#events[0]!, cursor: 10_000 + i, eventId: `evt_restart_${i}`, eventType, occurredAt, runId, sessionId: runId ? `${runId}-s` : null, actor, payload }));
    // The task's creation, then both attempts: none of scenario a's single implementer.
    this.#events = [this.#events[0]!, ...extra];
  }

  #restarted = false;
  #recovery: RecoveryOptions | null = null;

  override recoveryOptions(): Promise<RecoveryOptions> {
    return Promise.resolve(this.#recovery ?? { taskId: TASK_ID, actions: [], attempt: 1, keptUntil: null });
  }

  override async recover(_taskId: string, action: RecoverAction): Promise<{ action: RecoverAction }> {
    await wait(150);
    this.#recovery = null;
    this.#task = { ...this.#task, status: "running" };
    return { action };
  }

  /** The scope's events after a cursor, as the API and the stream's backfill both answer. A test's client adds its own. */
  protected ledgerFor({ runId, taskId, after = 0 }: LedgerQuery): PersistedEvent[] {
    return this.#events.filter((e) => e.cursor > after && (!runId || e.runId === runId) && (!taskId || e.taskId === taskId));
  }

  /** After a change a backend would announce: the stream says `servers.changed`. */
  #changed() {
    emit({
      eventType: "servers.changed",
      occurredAt: new Date().toISOString(),
      organizationId: ORG.id,
      projectId: PROJECT.id,
      taskId: TASK_ID,
      runId: this.#servers.run?.id ?? RUN_ID,
      sessionId: null,
      workflowRunId: null,
      actor: { type: "system", id: "dude" },
      source: "control-plane",
      correlationId: null,
      causationId: null,
      payload: { taskId: TASK_ID, runId: this.#servers.run?.id ?? RUN_ID },
    });
  }

  // -- reads --------------------------------------------------------------

  override listProjects() {
    return Promise.resolve({ projects: [PROJECT] });
  }
  override navigation() {
    return Promise.resolve({ projects: this.#nav });
  }
  override recentPullRequests() {
    return Promise.resolve({ pullRequests: this.#restarted ? RESTARTED_PULL_REQUESTS : this.#scenario === "d" ? [PULL_REQUEST] : [] });
  }
  override listPullRequests(taskId: string) {
    if (this.#restarted) return Promise.resolve({ pullRequests: taskId === TASK_ID ? RESTARTED_PULL_REQUESTS : [] });
    return Promise.resolve({ pullRequests: this.#scenario === "d" && taskId === TASK_ID ? [PULL_REQUEST] : [] });
  }
  override getTask(id: string): Promise<TaskDetail> {
    return id === TASK_ID ? Promise.resolve(this.#task) : Promise.reject(new ApiError(404, "not_found", "No such task."));
  }
  override getRun(id: string): Promise<RunDetail> {
    const detail = runDetailFor(this.#scenario);
    return id === RUN_ID || this.#task.runs.some((r) => r.id === id)
      ? Promise.resolve({ ...detail, ...(id === RUN_ID ? this.#runPatch : {}), ...(this.#task.runs.find((r) => r.id === id) ?? {}) } as RunDetail)
      : Promise.reject(new ApiError(404, "not_found", "No such run."));
  }
  override getProject(id: string): Promise<ProjectDetail> {
    return id === PROJECT.id ? Promise.resolve(PROJECT) : Promise.reject(new ApiError(404, "not_found", "No such project."));
  }
  override events(params: LedgerQuery & { limit?: number }) {
    const events = this.ledgerFor(params);
    return Promise.resolve({ events, nextCursor: events.at(-1)?.cursor ?? params.after ?? 0 });
  }
  override listFindings(taskId: string) {
    if (this.#restarted) return Promise.resolve({ findings: taskId === TASK_ID ? RESTARTED_FINDINGS : [] });
    return Promise.resolve({ findings: this.#scenario === "d" && taskId === TASK_ID ? FINDINGS : [] });
  }
  override listArtifacts(): Promise<{ artifacts: Artifact[] }> {
    return Promise.resolve({ artifacts: this.#restarted ? RESTARTED_ARTIFACTS : [] });
  }
  override taskMetrics(_taskId: string, attempt?: number): Promise<TaskMetrics> {
    return Promise.resolve(this.#restarted ? restartedMetrics(attempt) : METRICS);
  }
  override githubSettings() {
    return Promise.resolve({ whoCanWake: "members" as const, openAs: "ready" as const, requestReviewFrom: "codeowners" as const, reviewLogins: [], mergeMethod: "squash" as const, whenBehind: "update" as const, fixRoundsPerPr: 5, ciStuckMinutes: 30 });
  }
  override reviewerCandidates(_id: string | null, q: string): Promise<ReviewerCandidate[]> {
    const words = q.trim().toLowerCase();
    if (!words) return Promise.resolve(REVIEWERS.filter((r) => r.reason));
    return Promise.resolve(REVIEWERS.filter((r) => `${r.login} ${r.name ?? ""}`.toLowerCase().includes(words)).map(({ reason: _, ...r }) => r));
  }
  override listPeople(): Promise<{ people: Member[]; you: string }> {
    return Promise.resolve({ people: PEOPLE, you: YOU });
  }
  override me() {
    return Promise.resolve({ person: PEOPLE[0]!, organization: ORG });
  }
  override projectSettings(): Promise<SettingsResponse> {
    return Promise.resolve(SETTINGS);
  }
  override projectOverview() {
    return Promise.resolve({ project: { id: PROJECT.id, name: PROJECT.name }, epics: [{ id: EPIC.id, title: EPIC.title, description: "", position: 0, state: "active" as const, stateSet: false, tasks: 6, lanes: { done: 1, review: 1, progress: 2, backlog: 2 }, prs: { ci_running: 1 }, owners: [], costUsd: 8.4, machineUsd: 0.4, lastActivity: new Date().toISOString(), needsYou: 1, createdAt: new Date().toISOString(), updatedAt: new Date().toISOString() }] });
  }
  override pushKey() {
    return Promise.reject(new ApiError(404, "not_found", "No push in the fixtures."));
  }
  override subscribePush() {
    return Promise.resolve({ subscribed: false });
  }
  override machineSizes() {
    return Promise.resolve({ sizes: MACHINE_SIZES, canEdit: true });
  }
  // The image library is empty in the fixtures: the real backend's browser
  // tests show it full. Builds are off (no dude layer), as on a dude without one.
  override imageChoices() {
    return Promise.resolve({ images: [], defaultImageId: null });
  }
  override images() {
    return Promise.resolve({ images: [], queue: [], defaultImageId: null, builder: { available: false, layer: null, cpus: 1.5, memoryMiB: 1536, lastSeenAt: null, offline: false }, canEdit: true });
  }
  override machinePools() {
    return Promise.resolve({ pools: [], readAt: new Date().toISOString(), problem: "no lux in the fixtures" });
  }
  override modelTiers() {
    return Promise.resolve({ tiers: MODEL_TIERS, canEdit: true, upgrade: [] });
  }
  override proxyModels() {
    return Promise.resolve({ models: ["claude-opus-5-5", "claude-fable-5-1", "gpt-5.6-sol"], source: "llm.example/v1", problem: null });
  }

  // -- servers --------------------------------------------------------------

  override projectServers(): Promise<{ servers: Recipe[]; previews: PreviewSettings }> {
    return Promise.resolve({ servers: [...this.#recipes], previews: this.#previews });
  }
  override async putProjectServer(_projectId: string, name: string, recipe: RecipeInput): Promise<Recipe> {
    await wait(150);
    if (recipe.name !== name && this.#recipes.some((r) => r.name === recipe.name)) throw new ApiError(409, "name_taken", `There is already a server called ${recipe.name}.`);
    const saved: Recipe = { ...recipe, updatedAt: new Date().toISOString(), updatedBy: { id: YOU, name: PEOPLE[0]!.name } };
    const at = this.#recipes.findIndex((r) => r.name === name);
    if (at >= 0) this.#recipes[at] = saved;
    else this.#recipes.push(saved);
    return saved;
  }
  override async removeProjectServer(_projectId: string, name: string): Promise<void> {
    await wait(100);
    this.#recipes = this.#recipes.filter((r) => r.name !== name);
  }
  override async updatePreviewSettings(_projectId: string, settings: PreviewSettings): Promise<PreviewSettings> {
    await wait(100);
    const refused = settings.egress.map(egressProblem).find((p) => p !== null);
    if (refused) throw new ApiError(400, "invalid_request", `egress: ${refused}`);
    this.#previews = { ...this.#previews, ...settings };
    return this.#previews;
  }
  override projectSecrets(): Promise<{ secrets: PreviewSecret[] }> {
    return Promise.resolve({ secrets: [...this.#secrets] });
  }
  override async addProjectSecret(_projectId: string, name: string, value: string): Promise<PreviewSecret> {
    await wait(120);
    const problem = secretNameProblem(name, { secrets: this.#secrets.map((s) => s.name), recipes: this.#recipes });
    if (problem) throw new ApiError(problem.kind === "conflict" ? 409 : 400, problem.kind, problem.message);
    const saved: PreviewSecret = { name, hint: secretHint(value), updatedAt: new Date().toISOString(), updatedBy: { id: YOU, name: PEOPLE[0]!.name } };
    this.#secrets = [...this.#secrets, saved].sort((a, b) => a.name.localeCompare(b.name));
    return saved;
  }
  override async replaceProjectSecret(_projectId: string, name: string, value: string): Promise<PreviewSecret> {
    await wait(120);
    const at = this.#secrets.findIndex((s) => s.name === name);
    if (at < 0) throw new ApiError(404, "not_found", `no secret ${name}`);
    const saved: PreviewSecret = { name, hint: secretHint(value), updatedAt: new Date().toISOString(), updatedBy: { id: YOU, name: PEOPLE[0]!.name } };
    this.#secrets[at] = saved;
    return saved;
  }
  override async removeProjectSecret(_projectId: string, name: string): Promise<void> {
    await wait(100);
    this.#secrets = this.#secrets.filter((s) => s.name !== name);
  }
  override taskServers(): Promise<TaskServers> {
    return Promise.resolve(this.#snapshot());
  }
  override runServers(): Promise<TaskServers> {
    return Promise.resolve(this.#snapshot());
  }
  /** What a read returns: copies, and the recipes as they are now. */
  #snapshot(): TaskServers {
    return { ...this.#servers, servers: this.#servers.servers.map((s) => ({ ...s })), recipes: [...this.#recipes] };
  }
  #find(name: string): RunServer {
    const s = this.#servers.servers.find((x) => x.name === name);
    if (!s) throw new ApiError(404, "not_found", `No server called ${name} on this run.`);
    return s;
  }
  #set(name: string, patch: Partial<RunServer>) {
    Object.assign(this.#find(name), patch, { since: new Date().toISOString() });
    this.#changed();
  }
  override async serverAction(_runId: string, name: string, action: "start" | "stop" | "restart"): Promise<RunServer> {
    await wait(120);
    const s = this.#find(name);
    if (!this.#servers.run) throw new ApiError(409, "not_running", "The run is not running.");
    if (action === "stop") {
      this.#set(name, { state: "stopped", stopReason: "stopped", stoppedEpoch: s.epoch });
      return s;
    }
    if (!s.command) throw new ApiError(409, "no_command", `${name} has no command; start it by hand in the container.`);
    // A fresh start forgets how the last one ended.
    delete s.exitCode;
    delete s.error;
    this.#set(name, { state: "starting" });
    // The port answers a moment later — unless it is api's in scenario c, where it never does.
    later(() => {
      const now = this.#servers.servers.find((x) => x.name === name);
      if (now?.state !== "starting") return;
      if (this.#scenario === "c" && name === "api") this.#set(name, { state: "exited", exitCode: 1, error: "listen tcp :8080: bind: address already in use" });
      else this.#set(name, { state: "ready", readySince: new Date().toISOString() });
    }, 1_800);
    if (this.#servers.moved && this.#servers.servers.every((x) => x.name === name || x.state !== "stopped")) this.#servers.moved = null;
    return s;
  }
  override async serversAll(_runId: string, action: "start-all" | "stop-all"): Promise<TaskServers> {
    for (const s of this.#servers.servers) {
      if (action === "start-all" && s.command && canStart(s)) await this.serverAction(_runId, s.name, "start");
      if (action === "stop-all" && canStop(s)) await this.serverAction(_runId, s.name, "stop");
    }
    if (action === "start-all") this.#servers.moved = null;
    return this.#snapshot();
  }
  override async addRunServer(_runId: string, input: AddServer): Promise<RunServer> {
    await wait(150);
    const recipe = "recipe" in input ? this.#recipes.find((r) => r.name === input.recipe) : null;
    if ("recipe" in input && !recipe) throw new ApiError(404, "not_found", `The project has no server called ${input.recipe}.`);
    const name = recipe ? recipe.name : (input as { name: string }).name;
    if (this.#servers.servers.some((s) => s.name === name)) throw new ApiError(409, "name_taken", `${name} is already on this run.`);
    const port = recipe ? recipe.port : (input as { port: number }).port;
    const given = recipe ? recipe.command : (input as { command?: string | string[] | null }).command;
    const command = Array.isArray(given) ? given : given ? ["sh", "-c", given] : null;
    const suffix = this.#servers.run?.luxRunId.replace(/^run_/, "") ?? RUN_SUFFIX;
    const added = server(name, port, {
      state: "stopped",
      command,
      workdir: recipe?.workdir ?? (input as { workdir?: string }).workdir ?? "",
      since: new Date().toISOString(),
    }, suffix);
    this.#servers.servers.push(added);
    this.#logs[name] = [];
    this.#changed();
    if (command) await this.serverAction(_runId, name, "start");
    return added;
  }
  override async removeRunServer(_runId: string, name: string): Promise<void> {
    await wait(100);
    this.#find(name);
    this.#servers.servers = this.#servers.servers.filter((s) => s.name !== name);
    this.#changed();
  }
  override serverLog(_runId: string, name: string): Promise<{ lines: ServerLogLine[] }> {
    return Promise.resolve({ lines: this.#logs[name] ?? [] });
  }
  override async startPreview(): Promise<TaskServers> {
    await wait(200);
    if (this.#servers.run) throw new ApiError(409, "preview_running", "A run is already serving this task.");
    const run = { ...previewRun, id: "run_preview_1", startedAt: new Date().toISOString(), previewStage: "scheduling" as const };
    const suffix = run.luxRunId.replace(/^run_/, "");
    this.#servers = {
      run,
      servers: this.#recipes.map((r) => server(r.name, r.port, { state: "stopped", fromSpec: r.autostartInPreviews, since: new Date().toISOString() }, suffix)),
      moved: null,
      recipes: [...this.#recipes],
      preview: null,
    };
    this.#task = { ...this.#task, status: "running" };
    this.#changed();
    // The stages, one after another, then the autostart servers come up.
    const stages = ["cloning", "setup", "starting", "ready"] as const;
    stages.forEach((stage, i) => later(() => {
      if (this.#servers.run?.id !== run.id) return;
      this.#servers.run = { ...this.#servers.run, previewStage: stage, luxState: stage === "ready" ? "running" : "starting" };
      if (stage === "starting") for (const s of this.#servers.servers) if (s.fromSpec) Object.assign(s, { state: "starting", since: new Date().toISOString() });
      if (stage === "ready") for (const s of this.#servers.servers) if (s.fromSpec) Object.assign(s, { state: "ready", readySince: new Date().toISOString(), since: new Date().toISOString() });
      this.#changed();
    }, 2_500 * (i + 1)));
    return this.#snapshot();
  }
  override async stopPreview(): Promise<TaskServers> {
    await wait(150);
    if (this.#servers.run?.kind !== "preview") throw new ApiError(404, "not_found", "No branch preview is live.");
    this.#servers = { ...this.#servers, run: null, servers: [], moved: null };
    this.#task = { ...this.#task, status: "review" };
    this.#changed();
    return this.#snapshot();
  }

  // -- writes the screens can reach that change nothing here -----------------

  override setWhere(): void {}
  override reassignTask(): Promise<never> {
    return Promise.reject(new ApiError(400, "fixtures", "Not in the fixtures."));
  }
  /**
   * A steer plays as lux now reports one: taken at once while the agent's
   * command runs, then read at its next step — the command finishes, the
   * delivery lands, the agent's next call follows.
   */
  override async steer(runId: string, text: string, options: { interrupt?: boolean; supersedes?: string } = {}) {
    await wait(150);
    const id = `dir_fx_${++streamCursor}`;
    const record = (eventType: string, payload: Record<string, unknown>, actor: PersistedEvent["actor"] = { type: "system", id: "dude" }) => {
      const cursor = ++streamCursor;
      const e = {
        eventId: `evt_fx_${cursor}`, cursor, eventType, occurredAt: new Date().toISOString(), organizationId: ORG.id,
        projectId: PROJECT.id, taskId: TASK_ID, runId, sessionId: `${runId}-s`, workflowRunId: null, actor,
        source: "orchestrator", correlationId: null, causationId: null, payload,
      } as unknown as PersistedEvent;
      this.#events.push(e);
      emit(e);
    };
    record("run.steered", { directiveId: id, text, scope: "run", interrupt: options.interrupt === true, supersedes: options.supersedes ?? null },
      { type: "person", id: YOU });
    later(() => record("run.directive.accepted", { directiveId: id, lands: "next_step", receipt: true }), 400);
    const open = [...this.#events].reverse().find((e) => e.eventType === "agent.tool.called");
    const done = open && !this.#events.some((e) => e.eventType === "agent.tool.completed" && e.payload?.callId === open.payload?.callId);
    later(() => {
      if (done) record("agent.tool.completed", { tool: open!.payload?.tool, callId: open!.payload?.callId, status: "completed", exitCode: 0 },
        { type: "agent", id: "implementer" });
      record("run.directive.delivered", { directiveId: id, read: true });
      record("agent.tool.called", { tool: "read", callId: `c_fx_${streamCursor}`, input: { file_path: "apps/web/src/checkout/PaymentStep.tsx" } },
        { type: "agent", id: "implementer" });
    }, options.interrupt ? 600 : 6000);
    return { id } as unknown as Awaited<ReturnType<ApiClient["steer"]>>;
  }
  override pause(): Promise<never> {
    return Promise.reject(new ApiError(400, "fixtures", "Not in the fixtures."));
  }
  override abort(): Promise<never> {
    return Promise.reject(new ApiError(400, "fixtures", "Not in the fixtures."));
  }
  override runDiff(): Promise<never> {
    return Promise.reject(new ApiError(404, "not_found", "No diff in the fixtures."));
  }
}
