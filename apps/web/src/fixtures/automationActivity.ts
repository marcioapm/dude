/**
 * Automation runs' activity in the fixtures: the ledger events the
 * orchestrator writes for them (phases.AutomationFeed), keyed by
 * automationRunId, for each state the run page shows. A live run's state
 * is `?step=` in the page's URL: running (the default), done, posted, or
 * scrolled (two turns arrive after it opens).
 */

import type { AutomationRun, PersistedEvent } from "@dude/domain";
import { ORG, PROJECT } from "./data.ts";

const SEC = 1000;
const MIN = 60 * SEC;
const now = Date.now();
const at = (ms: number) => new Date(ms).toISOString();

let cursor = 600_000;
function ev(run: string, type: string, ms: number, payload: Record<string, unknown>): PersistedEvent {
  cursor++;
  return {
    cursor, eventId: `evt_fx_auto_${cursor}`, eventType: type, occurredAt: at(ms), organizationId: ORG.id, projectId: PROJECT.id,
    taskId: null, runId: null, sessionId: null, workflowRunId: null, automationRunId: run,
    actor: { type: "agent", id: run }, source: "runner", correlationId: null, causationId: null, payload,
  };
}
const message = (run: string, ms: number, text: string) => ev(run, "agent.message", ms, { text });
const thought = (run: string, ms: number, text: string) => ev(run, "agent.thought", ms, { text });
const called = (run: string, ms: number, callId: string, tool: string, input: unknown) =>
  ev(run, "agent.tool.called", ms, { tool, callId, input, title: tool });
const completed = (run: string, ms: number, callId: string, tool: string, more: Record<string, unknown> = {}) =>
  ev(run, "agent.tool.completed", ms, { tool, callId, status: "completed", ...more });
const out = (head: string, tail?: string, omittedBytes?: number) => (tail ? { head, tail, omittedBytes } : { head });

const PR_STATS_HEAD = `{
  "org": "acme",
  "since": "2026-10-16T15:00:00+01:00",
  "merged": 41,
  "last_week": { "merged": 36, "median_hours_to_merge": 23.1 },
  "median_hours_to_merge": 19.4,
  "bots": { "opened": 22, "merged": 18 },
  "pull_requests": [
    { "repo": "acme/web", "number": 270, "title": "Webhooks: retry with backoff", "merged_at": "2026-10-20T10:12:00Z" },
    { "repo": "acme/web", "number": 274, "title": "Live run feed", "merged_at": "2026-10-21T16:40:00Z" },`;
const PR_STATS_TAIL = `    { "repo": "acme/billing", "number": 188, "title": "Invoices: PDF export", "merged_at": "2026-10-23T09:03:00Z" }
  ]
}`;
const NUMBERS = `repo           merged  median h  vs last week
acme/web           19      17.2  +4
acme/billing       12      21.8  +2
acme/platform      10      20.5  -1
total              41      19.4  +5`;
const DIGEST_TEXT = "*PR digest · week of 20 Oct*\n📦 *What shipped*\n• Webhook reliability: retries with backoff (<https://github.com/acme/web/pull/270|web#270>)";

/** The digest's activity, as far as `step` has got. */
function digest(run: string, t0: number, step: "running" | "done" | "posted" | "finished" | "scrolled"): PersistedEvent[] {
  const e = [
    message(run, t0 + 16 * SEC, "Getting this week's numbers from `pr-stats` first, then I'll write the digest for #engineering."),
    called(run, t0 + 18 * SEC, "c1", "bash", { command: "pr-stats --org acme --since 7d --format json" }),
  ];
  if (step === "running") return e;
  e.push(completed(run, t0 + 36 * SEC, "c1", "bash", { output: out(PR_STATS_HEAD, PR_STATS_TAIL, 23_860), exitCode: 0 }),
    thought(run, t0 + 42 * SEC, "41 merged against 36 last week; the median time to merge is down from 23.1h to 19.4h. The table comes from the text format, pasted as printed."),
    called(run, t0 + 44 * SEC, "c2", "read", { filePath: "notes/digest-style.md" }),
    completed(run, t0 + 44.2 * SEC, "c2", "read", { output: out("Sections, in order: What shipped · Feature highlight · Numbers · Bots · Parting shot.") }));
  if (step === "scrolled") return e;
  e.push(called(run, t0 + 45 * SEC, "c3", "bash", { command: "pr-stats --org acme --since 7d --format text" }),
    completed(run, t0 + 46.9 * SEC, "c3", "bash", { output: out(NUMBERS), exitCode: 0 }));
  if (step === "done") return e;
  e.push(message(run, t0 + 62 * SEC, "Writing it up: three themes (webhook reliability, the live run feed, billing PDFs), the numbers table as printed, and one line for the bots."),
    called(run, t0 + 64 * SEC, "c4", "post_to_chat", { channel: "#engineering", text: DIGEST_TEXT }),
    completed(run, t0 + 64.6 * SEC, "c4", "post_to_chat", { output: out(`{"channel":"#engineering","channelId":"C0ENGINEER","ts":"1761318192.000100"}`),
      dude: { eventId: `evt_dude_${run}`, posted: { channel: "#engineering", channelId: "C0ENGINEER", ts: "1761318192.000100",
         permalink: "https://acme-hq.slack.com/archives/C0ENGINEER/p1761318192000100" } } }));
  if (step !== "posted") e.push(message(run, t0 + 81 * SEC, "Posted this week's digest to #engineering: 41 merged (+5 on last week), median time to merge 19.4h (down from 23.1h). Bots opened 22 and merged 18."));
  return e;
}

/** The two turns a scrolled-up reader is told of: "2 new turns · Jump to latest". */
export function digestLater(run: string, t0: number): PersistedEvent[] {
  return [
    called(run, t0 + 45 * SEC, "c3", "bash", { command: "pr-stats --org acme --since 7d --format text" }),
    completed(run, t0 + 46.9 * SEC, "c3", "bash", { output: out(NUMBERS), exitCode: 0 }),
    message(run, t0 + 47 * SEC, "The numbers are ready. Writing the digest for #engineering."),
  ];
}

const KUBECTL_ERR = `E1027 11:56:41.512309     212 memcache.go:265] couldn't get current server API group list: the server has asked for the client to provide credentials
error: You must be logged in to the server (the server has asked for the client to provide credentials)`;

/** The alert's live run: a search, a failed kubectl, the resolved delivery where it arrived, the task it filed. */
function alert(run: string, t0: number): PersistedEvent[] {
  return [
    message(run, t0 + 9 * SEC, "HighErrorRate on api since 16:01. Reading what merged to acme/web-console in the last two hours first."),
    called(run, t0 + 11 * SEC, "g1", "github_search", { query: "repo:example/web-console is:pr is:merged merged:>=2026-10-27T10:00" }),
    completed(run, t0 + 12.2 * SEC, "g1", "github_search", { output: { bytes: 4120, withheld: true, withheldBy: "name" },
      dude: { eventId: "evt_dude_g1", result: { bytes: 4120, withheld: true } } }),
    called(run, t0 + 30 * SEC, "k1", "bash", { command: "kubectl logs deploy/api --since=15m | grep -c 502" }),
    ev(run, "agent.tool.completed", t0 + 31 * SEC, { tool: "bash", callId: "k1", status: "error", stderr: out(KUBECTL_ERR), exitCode: 1 }),
    message(run, t0 + 40 * SEC, "`kubectl` has no credentials in this machine, so no pod logs. Going by what merged: #4182 went out 14 minutes before the alert and retries 502s from the payment gateway, which multiplies the load on a gateway that is already failing."),
    thought(run, t0 + 44 * SEC, "If the retries amplify a gateway problem, the rate should fall when the gateway recovers, with no rollback."),
    called(run, t0 + 48 * SEC, "e1", "bash", { command: "./scripts/error-rate checkout-api --every 60s --for 8m" }),
    completed(run, t0 + 48 * SEC + 8 * MIN + 2 * SEC, "e1", "bash", { output: out("16:20 8.1%\n16:21 7.4%\n16:22 3.0%\n16:23 0.6%\n16:24 0.4%"), exitCode: 0 }),
    ev(run, "automation.delivery.sent", t0 + 11 * MIN, { deliveryId: "dlv_7c5m", source: "incoming", event: "incoming",
      groupKey: `{}/{alertname="HighErrorRate"}:{service="api"}`, summary: "firing · HighErrorRate · api" }),
    ev(run, "automation.delivery.sent", t0 + 35 * MIN, { deliveryId: "dlv_9k2h", source: "incoming", event: "incoming",
      groupKey: `{}/{alertname="HighErrorRate"}:{service="api"}`, summary: "resolved · HighErrorRate · api" }),
    message(run, t0 + 35 * MIN + 96 * SEC, "It resolved nine minutes in, with no rollback: the gateway recovered and the rate fell with it. #4182's retries made the spike worse while it lasted. Filing a task to bound them before the next gateway incident."),
    called(run, t0 + 35 * MIN + 100 * SEC, "t1", "create_task", { title: "Bound checkout-api's payment-gateway retries (#4182)", goal: "HighErrorRate fired 11:55–12:04 on checkout-api …" }),
    completed(run, t0 + 35 * MIN + 100.8 * SEC, "t1", "create_task", { output: out(`{"key":"WC-231"}`), dude: { eventId: "evt_dude_t1", result: { key: "WC-231" } } }),
    message(run, t0 + 35 * MIN + 132 * SEC, "Filed **WC-231** in web-console. Nothing else to do here: the alert is resolved and the cause is a code change, not an outage."),
  ];
}

const TEST_LINE = (name: string, ms: number) => `\x1b[32m✓\x1b[0m ${name} \x1b[2m[${ms.toFixed(2)}ms]\x1b[0m`;
const TEST_NAMES = ["follows the tail", "elides the middle of long output", "names the run's trigger", "renders ANSI colours", "drops a cut escape",
  "stops following on scroll", "retries a 502 once", "keeps the group key", "parses a cron line", "keeps a delivery once", "orders the ledger by seq",
  "refuses a bad signature"];

/** A command's output: `lines` of a lint-and-test run, then its exit when it has one. */
function command(run: string, t0: number, lines: number, exit: number | null): PersistedEvent[] {
  const e: PersistedEvent[] = [];
  const text = ["$ bun run lint && bun test", "\x1b[1mlint\x1b[0m: 0 problems", ""];
  for (let i = 0; text.length < lines; i++) {
    if (i % 14 === 0) text.push("", `\x1b[1m${["web/feed", "web/hooks", "control-plane/events", "orchestrator/runner"][Math.floor(i / 14) % 4]}:\x1b[0m`);
    text.push(i % 9 === 1 ? `\x1b[33m»\x1b[0m ${TEST_NAMES[i % TEST_NAMES.length]} \x1b[2m[skip]\x1b[0m` : TEST_LINE(TEST_NAMES[i % TEST_NAMES.length]!, 4.8 + (i % 14) * 3.6));
  }
  text.slice(0, lines).forEach((line, i) => e.push(ev(run, "automation.log.line", t0 + i * 40, { stream: "stdout", text: line })));
  if (exit !== null) {
    e.push(ev(run, "automation.log.line", t0 + lines * 40, { stream: "stdout", text: "" }),
      ev(run, "automation.log.line", t0 + lines * 40 + 10, { stream: "stdout", text: `\x1b[32m${lines - 20} pass\x1b[0m · 0 fail` }),
      ev(run, "automation.log.exited", t0 + lines * 40 + 20, { exitCode: exit }));
  }
  return e;
}

/** A server's container log, timestamped. */
function server(run: string, t0: number, stopped: boolean): PersistedEvent[] {
  const line = (ms: number, text: string, stream = "stdout") => ev(run, "automation.log.line", t0 + ms, { stream, text, server: true });
  const e = [
    line(400, "\x1b[36mINFO\x1b[0m  changelog 1.4.2 listening on :8080"),
    line(500, "\x1b[36mINFO\x1b[0m  repo example/web-console, branch main"),
    line(4_000, "\x1b[36mINFO\x1b[0m  POST /events pull_request d6f2a1c0-0101 closed merged=true"),
    line(4_100, "\x1b[36mINFO\x1b[0m  wrote CHANGELOG.md entry for #214"),
    line(17_000, "\x1b[36mINFO\x1b[0m  POST /events pull_request d6f2a1c0-0102 closed merged=true"),
    line(17_100, "\x1b[33mWARN\x1b[0m  GitHub rate limited, retrying in 1s", "stderr"),
    line(18_200, "\x1b[36mINFO\x1b[0m  wrote CHANGELOG.md entry for #216"),
  ];
  if (stopped) e.push(line(48_300, "\x1b[36mINFO\x1b[0m  SIGTERM: shutting down"));
  return e;
}

/** The audit's run that the 30-minute limit stopped in the middle of its last call. */
function audit(run: string, t0: number): PersistedEvent[] {
  return [
    message(run, t0 + 14 * SEC, "Checking every Go module in `example/svc-auth` for updates with known vulnerabilities."),
    called(run, t0 + 16 * SEC, "a1", "bash", { command: "go list -m -u -json all > /tmp/mods.json" }),
    completed(run, t0 + 74 * SEC, "a1", "bash", { output: out(""), exitCode: 0 }),
    called(run, t0 + 76 * SEC, "a2", "bash", { command: "govulncheck -mode=binary ./bin/svc-auth" }),
    completed(run, t0 + 86 * SEC, "a2", "bash", { exitCode: 3, output: out("Scanning your binary for known vulnerabilities...\n\nVulnerability #1: GO-2026-4013\n    Excessive memory use in golang.org/x/net/html\n  Found in: golang.org/x/net@v0.30.0\n  Fixed in: golang.org/x/net@v0.33.0\n\nYour code is affected by 1 vulnerability from 1 module.") }),
    message(run, t0 + 120 * SEC, "One affected module. Running the whole test suite against `golang.org/x/net@v0.33.0` before suggesting the bump."),
    called(run, t0 + 166 * SEC, "a3", "bash", { command: "go get golang.org/x/net@v0.33.0 && go test ./... -count=1" }),
    ev(run, "agent.tool.completed", t0 + 30 * MIN, { tool: "bash", callId: "a3", status: "aborted", note: "Stopped by the time limit, when the run reached 30 minutes.",
      output: out("ok   example/svc-auth/internal/token  4.211s\nok   example/svc-auth/internal/session  12.904s\n=== RUN   TestRotateUnderLoad") }),
  ];
}

const DIGEST_LIVE = now - 34 * SEC;
/** The digest runs automations.ts lists from before (arn_d1, arn_d0): three days and three hours ago, and a week before. */
const DIGEST_D1_AGO = (3 * 24 + 3) * 60 * MIN;

/** A digest whose post was refused: Slack was not connected. */
function refusedDigest(run: string, t0: number): PersistedEvent[] {
  return [
    message(run, t0 + 16 * SEC, "Posting this week's digest to #engineering."),
    called(run, t0 + 178 * SEC, "r1", "post_to_chat", { channel: "#engineering", text: DIGEST_TEXT }),
    ev(run, "agent.tool.completed", t0 + 178.04 * SEC, { tool: "post_to_chat", callId: "r1", status: "error",
      dude: { eventId: "evt_d0", error: `Slack is not connected for ${ORG.name}: an organisation admin adds dude's Slack app in Organisation settings → Chat. Nothing was posted` } }),
  ];
}
const ALERT_LIVE = now - 66 * MIN;
const COMMAND_LIVE = now - 6 * SEC;
const SERVER_LIVE = now - 24 * SEC;
const AUDIT_AT = now - 21 * 60 * MIN;

/** The live digest's state, from the page's `?step=`. */
const fixtureStep = typeof window === "undefined" ? null : localStorage.getItem("dude.fixtures.automationStep");
function step(): "running" | "done" | "posted" | "scrolled" {
  return fixtureStep === "done" || fixtureStep === "posted" || fixtureStep === "scrolled" ? fixtureStep : "running";
}

/** Every automation run's activity, by run. */
export function automationActivity(runId: string): PersistedEvent[] {
  switch (runId) {
    case "arn_d3": {
      if (step() !== "scrolled") return digest(runId, DIGEST_LIVE, step());
      const history = Array.from({ length: 16 }, (_, i) => message(runId, DIGEST_LIVE - (16 - i) * 5 * SEC,
        `Repository ${i + 1}: read the merged pull requests and checked their release notes.\n\nThe digest groups them by theme and keeps the numbers from pr-stats unchanged.`));
      return [...history, ...digest(runId, DIGEST_LIVE, "scrolled")];
    }
    case "arn_d2": return digest(runId, now - 7 * 24 * 60 * MIN, "finished");
    case "arn_d1": return digest(runId, now - DIGEST_D1_AGO, "finished");
    case "arn_d0": return refusedDigest(runId, now - DIGEST_D1_AGO - 7 * 24 * 60 * MIN);
    case "arn_alert1": return alert(runId, ALERT_LIVE);
    case "arn_c2": return command(runId, COMMAND_LIVE, 118, null);
    case "arn_c1": return command(runId, now - 3 * 24 * 60 * MIN, 200, 0);
    case "arn_s2": return server(runId, SERVER_LIVE, false);
    case "arn_s1": return server(runId, now - 2 * 24 * 60 * MIN, true);
    case "arn_a2": return audit(runId, AUDIT_AT);
  }
  return [];
}

/** Turns that arrive on a live run's stream after it opens (the digest scrolled up). */
export function automationLater(runId: string): PersistedEvent[] {
  return runId === "arn_d3" && step() === "scrolled" ? digestLater(runId, DIGEST_LIVE) : [];
}

const run = (over: Partial<AutomationRun> & Pick<AutomationRun, "id" | "automationId" | "status" | "source">): AutomationRun => ({
  reason: null, session: "ephemeral", runId: null, startedBy: null, costUsd: null, createdAt: at(now), startedAt: at(now), endedAt: null,
  event: null, action: null, payloadCount: 0, ...over,
});

/** The runs these activities belong to, by automation, newest first. */
export const ACTIVITY_RUNS: Record<string, AutomationRun[]> = {
  aut_digest: [
    run({ id: "arn_d3", automationId: "aut_digest", status: "running", source: "schedule", costUsd: 0.04, createdAt: at(DIGEST_LIVE - 4 * SEC),
      startedAt: at(DIGEST_LIVE), now: step() === "running" ? { tool: "bash", args: { command: "pr-stats --org acme --since 7d --format json" } } : null }),
    run({ id: "arn_d2", automationId: "aut_digest", status: "succeeded", source: "schedule", costUsd: 0.18, createdAt: at(now - 7 * 24 * 60 * MIN - 4 * SEC),
      startedAt: at(now - 7 * 24 * 60 * MIN), endedAt: at(now - 7 * 24 * 60 * MIN + 81 * SEC) }),
  ],
  aut_cost: [
    run({ id: "arn_c2", automationId: "aut_cost", status: "running", source: "schedule", createdAt: at(COMMAND_LIVE), startedAt: at(COMMAND_LIVE) }),
    run({ id: "arn_c1", automationId: "aut_cost", status: "succeeded", source: "schedule", createdAt: at(now - 3 * 24 * 60 * MIN),
      startedAt: at(now - 3 * 24 * 60 * MIN), endedAt: at(now - 3 * 24 * 60 * MIN + 8_400) }),
  ],
  aut_changelog: [
    run({ id: "arn_s2", automationId: "aut_changelog", status: "running", source: "event", event: "pull_request", action: "closed", payloadCount: 2,
      createdAt: at(SERVER_LIVE), startedAt: at(SERVER_LIVE) }),
    run({ id: "arn_s1", automationId: "aut_changelog", status: "succeeded", source: "event", event: "pull_request", action: "closed", payloadCount: 2,
      createdAt: at(now - 2 * 24 * 60 * MIN), startedAt: at(now - 2 * 24 * 60 * MIN), endedAt: at(now - 2 * 24 * 60 * MIN + 48_500) }),
  ],
  aut_audit: [
    run({ id: "arn_a2", automationId: "aut_audit", status: "failed", source: "schedule", session: "resumed", costUsd: 0.41,
      reason: "Stopped at its 30-minute time limit during a bash call. Nothing was posted", createdAt: at(AUDIT_AT - 4 * SEC),
      startedAt: at(AUDIT_AT), endedAt: at(AUDIT_AT + 30 * MIN) }),
  ],
};

/** A server run's container times and requests, for its timeline. */
export function serverRun(runId: string) {
  const t0 = runId === "arn_s2" ? SERVER_LIVE : now - 2 * 24 * 60 * MIN;
  const stopped = runId === "arn_s1";
  return {
    containerStartedAt: at(t0), containerStoppedAt: stopped ? at(t0 + 48_500) : null, stopReason: stopped ? "idle" as const : null,
    requests: [
      { id: `${runId}_q1`, deliveryId: "d6f2a1c0-0101", try: 1, httpStatus: 202, ms: 38, error: null, responseExcerpt: null, at: at(t0 + 4_000) },
      { id: `${runId}_q2`, deliveryId: "d6f2a1c0-0102", try: 1, httpStatus: 202, ms: 41, error: null, responseExcerpt: null, at: at(t0 + 17_000) },
    ],
  };
}

/** When a scheduled run was due: four seconds before it started. */
export function scheduledFor(r: AutomationRun): string | null {
  return r.source === "schedule" && r.startedAt ? at(Date.parse(r.startedAt) - 4 * SEC) : null;
}
