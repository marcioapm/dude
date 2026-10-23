/**
 * Automations in the fixtures: the organisation's and web-console's, a few
 * runs, past deliveries and what GitHub sends. Writes change what the next
 * read returns, as a backend would.
 */

import type { Automation, AutomationInput, AutomationPromptVersion, AutomationRun, AutomationRunDetail, AutomationRunsResponse, EventSubscriptions, EventVerdict, NewAutomation, PastDelivery, RunNow } from "@dude/domain";
import { automationDefaults, repoFilterAllows } from "@dude/domain";
import { ORG, PEOPLE, PROJECT } from "./data.ts";

const promptOf = (agent: Automation["agent"]): string | null => (!agent ? null : "own" in agent ? agent.own.prompt : agent.brief);

const MIN = 60_000;
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();
const person = (id: string) => {
  const p = PEOPLE.find((x) => x.id === id)!;
  return { id: p.id, name: p.name, photoUrl: p.photoUrl ?? null };
};

function automation(over: Partial<Automation> & Pick<Automation, "id" | "name" | "kind" | "trigger">): Automation {
  return {
    description: "", sessionMode: "ephemeral", expiryDays: null, concurrency: "queue", parallelMax: 2, repos: "all", repoFilter: null,
    agent: null, machineSizeId: null, imageId: null, setupScript: null, command: null, server: null, webhook: null, secrets: [],
    notifyOnFailure: true, projectId: null, projectName: null, enabled: true, owner: person("u_marcio"), ownerLeftAt: null,
    webhookSecretHint: null, nextRunAt: null, lastRun: null, canEdit: true, canTransfer: true, createdAt: iso(30 * 24 * 60 * MIN),
    updatedAt: iso(24 * 60 * MIN), ...over,
  };
}

const NO_FILTERS = { branch: [], label: [], paths: [], author: [] };

export const AUTOMATIONS: Automation[] = [
  automation({ id: "aut_triage", name: "PR triage", kind: "agent",
    trigger: { kind: "github", event: "pull_request", actions: ["opened", "reopened"], filters: { ...NO_FILTERS, author: ["!dependabot[bot]"] } },
    repoFilter: { mode: "except", repos: ["example/sandbox-*", "example/notebooks"] },
    agent: { own: { prompt: "Read {{pr.title}} in {{repository}} and label it." } },
    lastRun: { id: "arn_t1", status: "succeeded", at: iso(12 * MIN) } }),
  automation({ id: "aut_audit", name: "Nightly dependency audit", kind: "agent", sessionMode: "resumable", expiryDays: 7,
    trigger: { kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" }, repoFilter: { mode: "only", repos: ["example/svc-*"] },
    agent: { preset: { role: "investigator", overrides: {} }, brief: "Audit dependencies and file a task for each upgrade that matters." },
    nextRunAt: iso(-8 * 60 * MIN), lastRun: { id: "arn_a1", status: "failed", at: iso(21 * 60 * MIN) } }),
  automation({ id: "aut_cost", name: "Weekly cost report", kind: "command", enabled: true, owner: null, ownerLeftAt: iso(3 * 24 * 60 * MIN),
    trigger: { kind: "schedule", cron: "0 8 * * 1", tz: "Europe/Lisbon" }, command: "bun run scripts/cost-report.ts > \"$DUDE_OUTPUT_DIR/report.md\"",
    nextRunAt: iso(-3 * 24 * 60 * MIN) }),
  automation({ id: "aut_release", name: "Release notes to Slack", kind: "webhook", trigger: { kind: "github", event: "release", actions: ["published"], filters: NO_FILTERS },
    webhook: { url: "https://hooks.example.com/release" }, webhookSecretHint: "9f3c", repoFilter: { mode: "only", repos: ["example/web-console"] } }),
  automation({ id: "aut_qa", name: "Nightly QA pass", kind: "agent", projectId: PROJECT.id, projectName: PROJECT.name, owner: person("u_ana"),
    trigger: { kind: "schedule", cron: "0 3 * * *", tz: "Europe/Lisbon" },
    agent: { preset: { role: "qa_browser", overrides: { timeLimitMinutes: 120 } }, brief: "Walk the sign-in and checkout flows." },
    nextRunAt: iso(-8 * 60 * MIN), lastRun: { id: "arn_q1", status: "succeeded", at: iso(21 * 60 * MIN) } }),
  automation({ id: "aut_changelog", name: "Changelog builder", kind: "server", projectId: PROJECT.id, projectName: PROJECT.name, enabled: false,
    trigger: { kind: "github", event: "pull_request", actions: ["closed"], filters: NO_FILTERS },
    server: { command: "bun run changelog:serve", port: 8080, path: "/events", health: "/healthz" } }),
];

const RUNS: Record<string, AutomationRun[]> = {
  aut_triage: [
    { id: "arn_t1", automationId: "aut_triage", status: "succeeded", reason: null, source: "event", session: "ephemeral", runId: null, startedBy: null,
      costUsd: 0.31, createdAt: iso(13 * MIN), startedAt: iso(13 * MIN), endedAt: iso(12 * MIN), event: "pull_request", action: "opened", payloadCount: 1 },
    { id: "arn_t0", automationId: "aut_triage", status: "skipped", reason: "a run was still going (Concurrency: skip)", source: "event", session: null,
      runId: null, startedBy: null, costUsd: null, createdAt: iso(14 * MIN), startedAt: null, endedAt: iso(14 * MIN), event: "pull_request", action: "opened", payloadCount: 1 },
  ],
  aut_audit: [
    { id: "arn_a1", automationId: "aut_audit", status: "failed", reason: "the agent's turn failed: the registry did not answer", source: "schedule",
      session: "resumed", runId: null, startedBy: null, costUsd: 0.84, createdAt: iso(21 * 60 * MIN), startedAt: iso(21 * 60 * MIN),
      endedAt: iso(21 * 60 * MIN - 6 * MIN), event: null, action: null, payloadCount: 0 },
  ],
};

const DELIVERIES: PastDelivery[] = [
  { id: "d6f2a1c0-0001", event: "pull_request", action: "opened", repository: "example/web-console", sender: "ana", receivedAt: iso(13 * MIN),
    body: { action: "opened", repository: { full_name: "example/web-console" }, sender: { login: "ana" }, pull_request: { number: 214, title: "Checkout: keep the cart" } } },
  { id: "d6f2a1c0-0002", event: "pull_request", action: "opened", repository: "example/web-console", sender: "dependabot[bot]", receivedAt: iso(60 * MIN),
    body: { action: "opened", repository: { full_name: "example/web-console" }, sender: { login: "dependabot[bot]" }, pull_request: { number: 213, title: "Bump vite" } } },
];

/** The fixture world's automations, changed by writes. */
export class FixtureAutomations {
  list = AUTOMATIONS.map((a) => ({ ...a }));
  runs: Record<string, AutomationRun[]> = structuredClone(RUNS);

  all(query: Record<string, string>): { automations: Automation[] } {
    return { automations: this.list.filter((a) =>
      (!query["scope"] || (query["scope"] === "organization") === (a.projectId === null)) &&
      (!query["trigger"] || a.trigger.kind === query["trigger"]) &&
      (!query["kind"] || a.kind === query["kind"].split(":")[0]) &&
      (!query["enabled"] || String(a.enabled) === query["enabled"]) &&
      (!query["owner"] || a.owner?.id === "u_marcio") &&
      (!query["q"] || a.name.toLowerCase().includes(query["q"].toLowerCase()))) };
  }

  project(projectId: string): { automations: Automation[]; alsoRunningHere: Automation[] } {
    return {
      automations: this.list.filter((a) => a.projectId === projectId),
      alsoRunningHere: this.list.filter((a) => a.projectId === null && repoFilterAllows(a.repoFilter, "example/web-console"))
        .map((a) => ({ ...a, canEdit: false })),
    };
  }

  one(id: string): Automation {
    const a = this.list.find((x) => x.id === id);
    if (!a) throw new Error(`no automation ${id}`);
    return a;
  }

  create(input: NewAutomation & { projectId: string | null; tz: string }): Automation {
    const settings = automationDefaults(input, input.tz);
    const a = automation({ ...settings, id: `aut_fx_${this.list.length + 1}`, enabled: false, projectId: input.projectId,
      projectName: input.projectId ? PROJECT.name : null });
    this.list.push(a);
    return a;
  }

  update(id: string, input: AutomationInput): Automation {
    const a = this.one(id);
    const before = promptOf(a.agent);
    Object.assign(a, input, { updatedAt: new Date().toISOString() });
    this.#keep(id, before, promptOf(a.agent), null);
    return a;
  }

  /** Versions of each automation's prompt or brief, newest first, kept as the backend keeps them. */
  prompts: Record<string, AutomationPromptVersion[]> = {
    aut_qa: [
      { id: "apv_qa2", number: 2, body: "Walk the sign-in and checkout flows.", createdBy: { id: "u_ana", name: "Ana" },
        createdAt: iso(2 * 24 * 60 * MIN), restoredFrom: null, current: true },
      { id: "apv_qa1", number: 1, body: "Walk the sign-in flow.", createdBy: { id: "u_ana", name: "Ana" },
        createdAt: iso(9 * 24 * 60 * MIN), restoredFrom: null, current: false },
    ],
  };

  #keep(id: string, before: string | null, after: string | null, restoredFrom: number | null): void {
    if (after === null) return;
    const list = (this.prompts[id] ??= []);
    if (list.length === 0 && before !== null && before !== after) {
      list.unshift({ id: `apv_${id}_1`, number: 1, body: before, createdBy: null, createdAt: new Date().toISOString(), restoredFrom: null, current: false });
    }
    if ((list[0]?.body ?? before) === after && restoredFrom === null) return;
    for (const v of list) v.current = false;
    const number = (list[0]?.number ?? 0) + 1;
    list.unshift({ id: `apv_${id}_${number}`, number, body: after, createdBy: { id: "u_marcio", name: "Márcio" },
      createdAt: new Date().toISOString(), restoredFrom, current: true });
  }

  promptHistory(id: string): { versions: AutomationPromptVersion[] } {
    this.one(id);
    return { versions: (this.prompts[id] ?? []).map((v) => ({ ...v })) };
  }

  restorePrompt(id: string, versionId: string): Automation {
    const a = this.one(id);
    const v = (this.prompts[id] ?? []).find((x) => x.id === versionId);
    if (!v || !a.agent) throw new Error(`no version ${versionId}`);
    const before = promptOf(a.agent);
    a.agent = "own" in a.agent ? { own: { ...a.agent.own, prompt: v.body } } : { ...a.agent, brief: v.body };
    this.#keep(id, before, v.body, v.number);
    return a;
  }

  remove(id: string): void {
    this.list = this.list.filter((a) => a.id !== id);
  }

  enable(id: string, on: boolean): Automation {
    const a = this.one(id);
    a.enabled = on;
    return a;
  }

  duplicate(id: string): Automation {
    const a = this.one(id);
    const copy = { ...structuredClone(a), id: `aut_fx_${this.list.length + 1}`, name: `${a.name} (copy)`, enabled: false,
      owner: person("u_marcio"), ownerLeftAt: null, lastRun: null, nextRunAt: null, createdAt: new Date().toISOString() };
    this.list.push(copy);
    return copy;
  }

  transfer(id: string, personId: string): Automation {
    const a = this.one(id);
    a.owner = person(personId);
    a.ownerLeftAt = null;
    return a;
  }

  repositories() {
    return { groups: [{ projectId: PROJECT.id, project: PROJECT.name, repos: ["example/web-console", "example/web-console-e2e"] },
      { projectId: "p_platform", project: "platform", repos: ["example/svc-auth", "example/svc-billing", "example/sandbox-1", "example/notebooks"] }] };
  }

  subscriptions(): EventSubscriptions {
    return { events: ["check_run", "check_suite", "issue_comment", "issues", "pull_request", "pull_request_review", "push", "release", "workflow_run"],
      source: "app", settingsUrl: `https://github.com/settings/apps/dude-${ORG.id}/permissions` };
  }

  runsOf(id: string): AutomationRunsResponse {
    const a = this.one(id);
    return { runs: this.runs[id] ?? [], total: (this.runs[id] ?? []).length, session: a.sessionMode === "resumable"
      ? { since: iso(6 * 24 * 60 * MIN), lastRunAt: iso(21 * 60 * MIN), resumes: 5, expiresAt: iso(-6 * 24 * 60 * MIN) } : null };
  }

  run(id: string, runId: string): AutomationRunDetail {
    const r = (this.runs[id] ?? []).find((x) => x.id === runId);
    if (!r) throw new Error(`no run ${runId}`);
    return { ...r, payloads: [{ deliveryId: DELIVERIES[0]!.id, event: "pull_request", action: "opened", body: DELIVERIES[0]!.body }],
      payloadsPurged: false, logTail: "Labelled #214: area/checkout.", exitCode: null, containerStartedAt: null, containerStoppedAt: null,
      stopReason: null, requests: [] };
  }

  deliveries(): { deliveries: PastDelivery[] } {
    return { deliveries: DELIVERIES };
  }

  runNow(id: string, body: RunNow): { runId: string } {
    const runId = `arn_fx_${Date.now()}`;
    const test = "testEvent" in body || "deliveryId" in body;
    (this.runs[id] ??= []).unshift({ id: runId, automationId: id, status: "queued", reason: null, source: test ? "test" : "run_now",
      session: null, runId: null, startedBy: person("u_marcio"), costUsd: null, createdAt: new Date().toISOString(), startedAt: null,
      endedAt: null, event: "testEvent" in body ? body.testEvent.event : null, action: "testEvent" in body ? body.testEvent.action : null, payloadCount: test ? 1 : 0 });
    return { runId };
  }

  evaluate(id: string, body: Record<string, unknown>): EventVerdict {
    const a = this.one(id);
    if (a.trigger.kind !== "github") return { matches: true };
    const action = typeof body["action"] === "string" ? body["action"] : "";
    if (a.trigger.actions.length && !a.trigger.actions.includes(action)) {
      return { matches: false, filter: "actions", because: `its action is ${action || "empty"}, not ${a.trigger.actions.join(" or ")}` };
    }
    const sender = (body["sender"] as { login?: string } | undefined)?.login ?? "";
    if (a.trigger.filters.author.some((x) => x.startsWith("!") && x.slice(1).toLowerCase() === sender.toLowerCase())) {
      return { matches: false, filter: "author", because: `it is by ${sender}, who is excluded` };
    }
    return { matches: true };
  }
}
