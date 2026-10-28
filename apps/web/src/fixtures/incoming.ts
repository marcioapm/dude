/**
 * Incoming webhooks in the fixtures: "Investigate the alert" (platform,
 * signature, Send to the running agent, grouped by groupKey) with a day of
 * deliveries from a monitoring tool, "Page triage" (organisation, header
 * token, no project yet), and what PR triage's GitHub deliveries came to.
 */

import type { Automation, AutomationDeliveryDetail, AutomationRun, AutomationRunDetail, AutomationToolCall, IncomingTrigger, IncomingVerdict } from "@dude/domain";
import { conditionHolds, groupKeyOf } from "@dude/domain";

const MIN = 60_000;
const iso = (ago: number) => new Date(Date.now() - ago).toISOString();

export const ALERT_HOOK = "hk_7f3a9c21e4b04d18";

/** A monitoring tool's alert notification, as Grafana sends one. */
export function alertBody(status: "firing" | "resolved", opts: { alertname?: string; service?: string; severity?: string; team?: string } = {}) {
  const alertname = opts.alertname ?? "HighErrorRate";
  const service = opts.service ?? "api";
  return {
    receiver: "dude",
    status,
    groupKey: `{}/{alertname="${alertname}"}:{service="${service}"}`,
    groupLabels: { alertname },
    commonLabels: { alertname, service, severity: opts.severity ?? "critical", team: opts.team ?? "platform" },
    commonAnnotations: { summary: `5xx rate above 5% on ${service} for 5 minutes`,
      description: status === "firing" ? "error_rate=0.087 (threshold 0.05)" : "error_rate=0.004" },
    externalURL: "https://grafana.example.com/d/api-errors?orgId=1",
    alerts: [{ status, labels: { alertname, service, pod: `${service}-7f9c` }, startsAt: "2026-10-26T16:01:00Z" }],
  };
}

const ALERT_TRIGGER: IncomingTrigger = {
  kind: "incoming", hookId: ALERT_HOOK, verify: { mode: "signature", header: "X-Signature", prefix: "sha256=" },
  conditions: [{ path: "status", op: "is", values: ["firing"] }, { path: "commonLabels.severity", op: "one_of", values: ["critical", "high"] }],
  groupBy: "groupKey", ratePerHour: 30,
};

const ALERT_PROMPT = `An alert fired: **{{payload.commonLabels.alertname}}** ({{payload.commonLabels.severity}}) on {{payload.commonLabels.service}}.

Its dashboard: {{payload.externalURL}}

1. Read the recent deploys and error logs of the service in the checked-out repositories.
2. Say what most likely broke, with the commit or config that did it.
3. File one task with the evidence and a proposed fix. Do not open a pull request.

When a later delivery says the alert resolved, add what changed to the task and stop.`;

/** The two incoming automations, built by the fixture world's own builder. */
export function incomingAutomations(build: (over: Partial<Automation> & Pick<Automation, "id" | "name" | "kind" | "trigger">) => Automation,
  project: { id: string; name: string }): Automation[] {
  return [
    build({ id: "aut_alert", name: "Investigate the alert", kind: "agent", projectId: project.id, projectName: project.name,
      sessionMode: "resumable", expiryDays: 1, concurrency: "send", trigger: ALERT_TRIGGER, incomingSecretHint: "a91c",
      publicOrigin: "https://dude.example.com",
      agent: { own: { prompt: ALERT_PROMPT, timeLimitMinutes: 60 } }, secrets: [{ source: "organization", name: "SENTRY_TOKEN" }],
      lastRun: { id: "arn_alert1", status: "running", at: iso(66 * MIN) } }),
    build({ id: "aut_pager", name: "Page triage", kind: "agent", enabled: false, incomingSecretHint: "07de",
      trigger: { kind: "incoming", hookId: "hk_c4e1b0aa93f2", verify: { mode: "header", header: "Authorization" },
        conditions: [{ path: "event_type", op: "is", values: ["incident.triggered"] }], groupBy: "incident.id", ratePerHour: 30 },
      agent: { own: { prompt: "An incident was triggered: **{{payload.incident.title}}**.\n\nFind what changed before {{payload.incident.created_at}} in the checked-out repositories and file one task with the evidence." } },
      secrets: [{ source: "organization", name: "STATUSPAGE_KEY" }] }),
  ];
}

const HEADERS = (bytes: number) => ({ "Content-Type": "application/json", "Content-Length": String(bytes), "User-Agent": "Grafana/12.2.0",
  "X-Signature": "sha256=••••••••", "X-Forwarded-For": "203.0.113.24" });

type Row = AutomationDeliveryDetail;

function delivery(id: string, ago: number, over: Partial<Row>): Row {
  return { id, receivedAt: iso(ago), verdict: null, reason: null, reasonIndex: null, groupKey: null, bytes: 0, runId: null,
    event: "incoming", action: null, repository: null, sender: null, body: null, headers: {}, ...over };
}

function accepted(id: string, ago: number, body: ReturnType<typeof alertBody>, over: Partial<Row>): Row {
  const bytes = JSON.stringify(body).length + 120;
  return delivery(id, ago, { body, bytes, groupKey: body.groupKey, headers: HEADERS(bytes), ...over });
}

/** Investigate the alert's last day: newest first. */
export const ALERT_DELIVERIES: Row[] = [
  accepted("dlv_9k2h", 31 * MIN, alertBody("resolved"), { verdict: "sent_to_run", runId: "arn_alert1" }),
  delivery("dlv_8r1q", 44 * MIN, { verdict: "refused", reason: "rate_limited", bytes: 2150, headers: { "Content-Type": "application/json", "User-Agent": "Grafana/12.2.0", "X-Signature": "sha256=••••••••" } }),
  accepted("dlv_7c5m", 55 * MIN, alertBody("firing"), { verdict: "sent_to_run", runId: "arn_alert1" }),
  accepted("dlv_6p0z", 61 * MIN, alertBody("firing", { alertname: "SlowQueries", service: "db", severity: "warning" }),
    { verdict: "skipped", reason: "commonLabels.severity is warning, not one of critical, high", reasonIndex: 1 }),
  accepted("dlv_5n3x", 66 * MIN, alertBody("firing"), { verdict: "started", runId: "arn_alert1" }),
  accepted("dlv_4b8r", 3 * 60 * MIN, alertBody("resolved", { alertname: "SlowQueries", service: "db" }),
    { verdict: "skipped", reason: "status is resolved, not firing", reasonIndex: 0 }),
  delivery("dlv_3w7d", 5 * 60 * MIN, { verdict: "refused", reason: "bad_signature", bytes: 1950, headers: { "Content-Type": "application/json", "X-Signature": "sha256=••••••••" } }),
  delivery("dlv_2m9a", 26 * 60 * MIN, { verdict: "refused", reason: "not_json", bytes: 38, headers: { "Content-Type": "text/plain" } }),
  delivery("dlv_1t4e", 48 * 60 * MIN, { verdict: "refused", reason: "too_large", bytes: 1_363_149, headers: { "Content-Type": "application/json" } }),
];

/** PR triage's GitHub deliveries and what each came to. */
export const TRIAGE_DELIVERIES: Row[] = [
  delivery("d6f2a1c0-0001", 13 * MIN, { event: "pull_request", action: "opened", repository: "example/web-console", sender: "ana", bytes: 18_430,
    verdict: "started", runId: "arn_t1", body: { action: "opened", repository: { full_name: "example/web-console" }, sender: { login: "ana" }, pull_request: { number: 214, title: "Checkout: keep the cart" } } }),
  delivery("d6f2a1c0-0002", 60 * MIN, { event: "pull_request", action: "opened", repository: "example/web-console", sender: "dependabot[bot]", bytes: 16_080,
    verdict: "skipped", reason: "Author: it is by dependabot[bot], who is excluded",
    body: { action: "opened", repository: { full_name: "example/web-console" }, sender: { login: "dependabot[bot]" }, pull_request: { number: 213, title: "Bump vite" } } }),
  delivery("d6f2a1c0-0003", 120 * MIN, { event: "pull_request", action: "synchronize", repository: "example/svc-auth", sender: "ana", bytes: 17_210,
    verdict: "skipped", reason: "Actions: its action is synchronize, not opened or reopened",
    body: { action: "synchronize", repository: { full_name: "example/svc-auth" }, sender: { login: "ana" }, pull_request: { number: 88, title: "Rotate keys" } } }),
];

export function deliveriesOf(id: string): Row[] {
  return id === "aut_alert" ? ALERT_DELIVERIES : id === "aut_triage" ? TRIAGE_DELIVERIES : [];
}

export const ALERT_RUNS: AutomationRun[] = [
  { id: "arn_alert1", automationId: "aut_alert", status: "running", reason: null, source: "event", session: "new", runId: null, startedBy: null,
    costUsd: 0.62, createdAt: iso(66 * MIN), startedAt: iso(66 * MIN), endedAt: null, event: "incoming", action: null, payloadCount: 3 },
  { id: "arn_alert0", automationId: "aut_alert", status: "succeeded", reason: null, source: "event", session: "new", runId: null, startedBy: null,
    costUsd: 0.41, createdAt: iso(26 * 60 * MIN), startedAt: iso(26 * 60 * MIN), endedAt: iso(25 * 60 * MIN), event: "incoming", action: null, payloadCount: 1 },
];

const LOG = [
  "13:07 read deploys of acme/api since 12:30: 1 (a41c9e2, 12:58 'Pool: lower idle timeout to 5s')",
  "13:12 error logs: 'connection reset by peer' from pgbouncer, 8.7% of requests",
  "13:15 filed PLAT-212 'HighErrorRate on api: idle timeout below pgbouncer's'",
  "13:41 delivery dlv_9k2h: status resolved — added the recovery time to PLAT-212",
].join("\n");

// The live run's create_task call: the PLAT-212 its log names.
const ALERT_CALLS: AutomationToolCall[] = [
  { id: "evt_alert_task", tool: "create_task", at: iso(66 * MIN - 8 * MIN), status: "completed", durationMs: 310, error: null, posted: null,
    args: { project: "platform", title: "HighErrorRate on api: idle timeout below pgbouncer's" } },
];

export function alertRunDetail(run: AutomationRun): AutomationRunDetail {
  const mine = ALERT_DELIVERIES.filter((d) => d.runId === run.id).reverse();
  const first = mine[0];
  return { ...run, payloads: first ? [{ deliveryId: first.id, event: "incoming", action: null, body: first.body }] : [], payloadsPurged: false,
    logTail: run.id === "arn_alert1" ? LOG : "Filed PLAT-201.", exitCode: null, containerStartedAt: null, containerStoppedAt: null,
    stopReason: null, requests: [], deliveries: mine.map(({ body: _b, headers: _h, ...d }) => d),
    toolCalls: run.id === "arn_alert1" ? ALERT_CALLS : [], failedCallId: null };
}

/** What each later delivery sent to a run gave its agent, by delivery id. */
export function alertSentBodies(run: AutomationRun): Record<string, unknown> {
  return Object.fromEntries(ALERT_DELIVERIES.filter((d) => d.runId === run.id && d.verdict === "sent_to_run").map((d) => [d.id, d.body]));
}

/** A body against an incoming trigger, as the orchestrator answers the test dialog. */
export function evaluateIncomingFixture(trigger: IncomingTrigger, body: Record<string, unknown>): IncomingVerdict {
  const results = trigger.conditions.map((c) => conditionHolds(c, body));
  const groupKey = groupKeyOf(trigger.groupBy, body);
  const live = ALERT_DELIVERIES.find((d) => d.runId === "arn_alert1" && d.groupKey === groupKey && groupKey);
  return { matches: results.every((r) => r.ok), results, groupKey, liveRunId: live ? "arn_alert1" : null };
}

export const FIXTURE_SECRET = "whsec_4f8Qk2NnZ0tB7cVx1RjL9mPa91c";
