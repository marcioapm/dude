/**
 * The API without a backend: the mockups' world, answered from memory.
 * For seeing the screens and the gallery's scenarios in the real app —
 * `?fixtures=a` … `f` (or `dude.fixtures` in localStorage), outside
 * production builds. Reads come from `data.ts`; writes to servers change
 * what the next read returns, so Start, Stop and Preview branch move
 * things as a backend would, if a little faster.
 */

import type { ServerScenario } from "@dude/design-system/fixtures/servers";
import { PREVIEW_PAGE, previewRun, serverRecipes } from "@dude/design-system/fixtures/servers";
import type { NavProject } from "@dude/design-system";
import type { PersistedEvent, PreviewSettings, RunServerInput, Server, ServerLogLine, ServerRecipe, ServerRecipeInput, SettingsResponse, TaskServers } from "@dude/domain";
import { ApiClient, ApiError, type Member, type ProjectDetail, type RunDetail, type TaskDetail, type TaskMetrics } from "../api/client.ts";
import { EPIC, FINDINGS, METRICS, ORG, PEOPLE, PROJECT, PULL_REQUEST, RUN_ID, SETTINGS, TASK_ID, YOU, eventsFor, logsFor, navigationFor, runDetailFor, serversFor, taskFor } from "./data.ts";

export const SCENARIOS: readonly ServerScenario[] = ["a", "b", "c", "d", "e", "f"];
const KEY = "dude.fixtures";

/** The scenario asked for: `?fixtures=b` (remembered), or what was remembered. Null for the real API. */
export function fixtureScenario(): ServerScenario | null {
  if (import.meta.env.MODE === "production") return null;
  const asked = new URLSearchParams(window.location.search).get("fixtures");
  if (asked !== null) {
    if (asked === "" || asked === "off") localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, asked);
    // The parameter has done its work; the hash is the app's own.
    window.history.replaceState(null, "", `${window.location.pathname}${window.location.hash}`);
  }
  const stored = localStorage.getItem(KEY);
  return stored && (SCENARIOS as readonly string[]).includes(stored) ? (stored as ServerScenario) : null;
}

/**
 * An EventSource over the fixtures: on open it replays the scope's ledger
 * (the backfill the real stream sends), then stays quiet until the fixture
 * client changes something, when it sends the `servers.changed` a backend
 * would — so the screens re-read through the same path they do for real.
 */
const streams = new Set<QuietEventSource>();
let streamCursor = 1_000_000;
let ledger: ((params: { runId?: string; taskId?: string; after?: number }) => PersistedEvent[]) | null = null;
class QuietEventSource extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSED = 2;
  readyState = 0;
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  constructor(readonly url: string) {
    super();
    streams.add(this);
    const q = new URL(url, window.location.origin).searchParams;
    setTimeout(() => {
      this.readyState = 1;
      this.onopen?.(new Event("open"));
      if (q.get("live") || !ledger) return;
      const params = { ...(q.get("runId") ? { runId: q.get("runId")! } : {}), ...(q.get("taskId") ? { taskId: q.get("taskId")! } : {}), after: Number(q.get("after") ?? 0) };
      for (const e of ledger(params)) this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(e) }));
    }, 20);
  }
  close() {
    this.readyState = 2;
    streams.delete(this);
  }
}

function emit(event: Omit<PersistedEvent, "cursor" | "eventId">): void {
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
  #recipes: ServerRecipe[];
  #previews: PreviewSettings;
  #task: TaskDetail;
  #events: PersistedEvent[];
  #nav: NavProject[];

  constructor(scenario: ServerScenario) {
    super({ apiKey: "fixtures" });
    this.#scenario = scenario;
    this.#servers = serversFor(scenario);
    this.#logs = logsFor(scenario);
    this.#recipes = [...serverRecipes];
    this.#previews = { image: null, egress: ["registry.npmjs.org", "proxy.golang.org", "sum.golang.org", "api.absmartly.com", "sandbox.absmartly.io"], idleTimeoutMinutes: 30, domain: "lux.absmartly.dev" };
    this.#task = taskFor(scenario);
    this.#events = eventsFor(scenario);
    this.#nav = navigationFor(scenario);
    ledger = (params) => this.#events.filter((e) => e.cursor > (params.after ?? 0) && (!params.runId || e.runId === params.runId) && (!params.taskId || e.taskId === params.taskId));
  }

  /** The frame has no server to reach: the mockup's page stands in. */
  override previewDocument(): string {
    return PREVIEW_PAGE;
  }

  get scenario(): ServerScenario {
    return this.#scenario;
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
    return Promise.resolve({ pullRequests: this.#scenario === "d" ? [PULL_REQUEST] : [] });
  }
  override listPullRequests(taskId: string) {
    return Promise.resolve({ pullRequests: this.#scenario === "d" && taskId === TASK_ID ? [PULL_REQUEST] : [] });
  }
  override getTask(id: string): Promise<TaskDetail> {
    return id === TASK_ID ? Promise.resolve(this.#task) : Promise.reject(new ApiError(404, "not_found", "No such task."));
  }
  override getRun(id: string): Promise<RunDetail> {
    const detail = runDetailFor(this.#scenario);
    return id === RUN_ID || this.#task.runs.some((r) => r.id === id) ? Promise.resolve({ ...detail, ...(this.#task.runs.find((r) => r.id === id) ?? {}) } as RunDetail) : Promise.reject(new ApiError(404, "not_found", "No such run."));
  }
  override getProject(id: string): Promise<ProjectDetail> {
    return id === PROJECT.id ? Promise.resolve(PROJECT) : Promise.reject(new ApiError(404, "not_found", "No such project."));
  }
  override events(params: { runId?: string; taskId?: string; after?: number; limit?: number }) {
    const after = params.after ?? 0;
    const events = this.#events.filter((e) => e.cursor > after && (!params.runId || e.runId === params.runId) && (!params.taskId || e.taskId === params.taskId));
    return Promise.resolve({ events, nextCursor: events.at(-1)?.cursor ?? after });
  }
  override listFindings(taskId: string) {
    return Promise.resolve({ findings: this.#scenario === "d" && taskId === TASK_ID ? FINDINGS : [] });
  }
  override listArtifacts() {
    return Promise.resolve({ artifacts: [] });
  }
  override taskMetrics(): Promise<TaskMetrics> {
    return Promise.resolve(METRICS);
  }
  override githubSettings() {
    return Promise.resolve({ whoCanWake: "members" as const, openAs: "ready" as const, requestReviewFrom: "codeowners" as const, reviewLogins: [], mergeMethod: "squash" as const, whenBehind: "update" as const, fixRoundsPerPr: 5, ciStuckMinutes: 30 });
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

  // -- servers --------------------------------------------------------------

  override projectServers(): Promise<{ servers: ServerRecipe[]; previews: PreviewSettings }> {
    return Promise.resolve({ servers: this.#recipes, previews: this.#previews });
  }
  override async putProjectServer(_projectId: string, name: string, recipe: ServerRecipeInput): Promise<ServerRecipe> {
    await wait(150);
    if (recipe.name !== name && this.#recipes.some((r) => r.name === recipe.name)) throw new ApiError(409, "name_taken", `There is already a server called ${recipe.name}.`);
    const saved: ServerRecipe = { ...recipe, updatedAt: new Date().toISOString(), updatedBy: { id: YOU, name: PEOPLE[0]!.name, photoUrl: null, online: true } };
    const at = this.#recipes.findIndex((r) => r.name === name);
    if (at >= 0) this.#recipes[at] = saved;
    else this.#recipes.push(saved);
    this.#servers.recipes = [...this.#recipes];
    return saved;
  }
  override async removeProjectServer(_projectId: string, name: string): Promise<void> {
    await wait(100);
    this.#recipes = this.#recipes.filter((r) => r.name !== name);
    this.#servers.recipes = [...this.#recipes];
  }
  override async updatePreviewSettings(_projectId: string, settings: PreviewSettings): Promise<PreviewSettings> {
    await wait(100);
    this.#previews = { ...this.#previews, ...settings };
    return this.#previews;
  }
  override taskServers(): Promise<TaskServers> {
    return Promise.resolve(this.#snapshot());
  }
  override runServers(): Promise<TaskServers> {
    return Promise.resolve(this.#snapshot());
  }
  #snapshot(): TaskServers {
    return { ...this.#servers, servers: this.#servers.servers.map((s) => ({ ...s })) };
  }
  #find(name: string): Server {
    const s = this.#servers.servers.find((x) => x.name === name);
    if (!s) throw new ApiError(404, "not_found", `No server called ${name} on this run.`);
    return s;
  }
  #set(name: string, patch: Partial<Server>) {
    Object.assign(this.#find(name), patch, { since: new Date().toISOString() });
    this.#changed();
  }
  override async serverAction(_runId: string, name: string, action: "start" | "stop" | "restart"): Promise<unknown> {
    await wait(120);
    const s = this.#find(name);
    if (!this.#servers.run) throw new ApiError(409, "not_running", "The run is not running.");
    if (action === "stop") {
      this.#set(name, { state: "stopped", stopReason: "stopped", stoppedEpoch: s.epoch });
      return s;
    }
    if (!s.command) throw new ApiError(409, "no_command", `${name} has no command; start it by hand in the container.`);
    this.#set(name, { state: "starting", exitCode: null, error: null });
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
  override async serversAll(_runId: string, action: "start-all" | "stop-all"): Promise<unknown> {
    for (const s of this.#servers.servers) {
      if (action === "start-all" && s.command && (s.state === "stopped" || s.state === "exited" || s.state === "unreachable")) await this.serverAction(_runId, s.name, "start");
      if (action === "stop-all" && (s.state === "ready" || s.state === "starting" || s.state === "unreachable")) await this.serverAction(_runId, s.name, "stop");
    }
    if (action === "start-all") this.#servers.moved = null;
    return {};
  }
  override async addRunServer(_runId: string, input: RunServerInput): Promise<unknown> {
    await wait(150);
    const recipe = "recipe" in input ? this.#recipes.find((r) => r.name === input.recipe) : null;
    if ("recipe" in input && !recipe) throw new ApiError(404, "not_found", `The project has no server called ${input.recipe}.`);
    const name = recipe ? recipe.name : (input as { name: string }).name;
    if (this.#servers.servers.some((s) => s.name === name)) throw new ApiError(409, "name_taken", `${name} is already on this run.`);
    const port = recipe ? recipe.port : (input as { port: number }).port;
    const command = recipe ? `exec ${recipe.command}` : (input as { command?: string }).command;
    const suffix = this.#servers.run?.luxRunId.replace(/^run_/, "") ?? "k3jq7x2mfa9vbn4z";
    const server: Server = {
      name, port, command: command ? ["sh", "-c", command] : null, workdir: recipe?.workdir ?? (input as { workdir?: string }).workdir ?? "", env: {},
      fromSpec: false, state: "stopped", since: new Date().toISOString(), readySince: null, stopReason: null, stoppedEpoch: null, epoch: 1,
      url: `https://${name}-${suffix}.lux.absmartly.dev`,
    };
    this.#servers.servers.push(server);
    this.#logs[name] = [];
    this.#changed();
    if (command) await this.serverAction(_runId, name, "start");
    return server;
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
    if (this.#servers.run) throw new ApiError(409, "conflict", "A run is already serving this task.");
    const run = { ...previewRun, id: "run_preview_1", startedAt: new Date().toISOString(), previewStage: "scheduling" as const };
    const suffix = run.luxRunId.replace(/^run_/, "");
    this.#servers = {
      run,
      servers: this.#recipes.map((r) => ({
        name: r.name, port: r.port, command: ["sh", "-c", `exec ${r.command}`], workdir: r.workdir, env: {}, fromSpec: r.autostartInPreviews, state: "stopped" as const,
        since: new Date().toISOString(), readySince: null, stopReason: null, stoppedEpoch: null, epoch: 1, url: `https://${r.name}-${suffix}.lux.absmartly.dev`,
      })),
      moved: null,
      recipes: [...this.#recipes],
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
  override async stopPreview(): Promise<void> {
    await wait(150);
    this.#servers = { ...this.#servers, run: null, servers: [], moved: null };
    this.#task = { ...this.#task, status: "review" };
    this.#changed();
  }

  // -- writes the screens can reach that change nothing here -----------------

  override setWhere(): void {}
  override reassignTask(): Promise<never> {
    return Promise.reject(new ApiError(400, "fixtures", "Not in the fixtures."));
  }
  override steer(): Promise<never> {
    return Promise.reject(new ApiError(400, "fixtures", "Not in the fixtures: the agent is a recording."));
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
