/** Sample data for the gallery. Realistic shapes, deterministic values. */

import type { SessionNodeData } from "../components/SessionTreeNode.tsx";
import type { LogLine } from "../components/LogStream.tsx";
import type { EventActor, EventSeverity } from "../components/EventRow.tsx";
import type { ReactNode } from "react";

// Anchored ~3h in the past so live durations tick forward from a sane value.
const T0 = Date.now() - 3 * 3_600_000;
export const at = (offsetMs: number): string => new Date(T0 + offsetMs).toISOString();

export interface EventFixture {
  readonly id: string;
  readonly occurredAt: string;
  readonly actor: EventActor;
  readonly eventType: string;
  readonly summary: ReactNode;
  readonly severity?: EventSeverity | undefined;
  readonly trailing?: ReactNode;
  readonly detail?: ReactNode;
  readonly meta?: ReadonlyArray<readonly [string, ReactNode]> | undefined;
}

export const events: EventFixture[] = [
  {
    id: "e1",
    occurredAt: at(0),
    actor: { type: "human", name: "marcio" },
    eventType: "task.created",
    summary: "Add retry with backoff to the GitHub webhook handler",
    meta: [
      ["task", <code key="w">WI-2481</code>],
      ["source", "web"],
    ],
  },
  { id: "e2", occurredAt: at(412), actor: { type: "system" }, eventType: "workflow.transition", summary: "received → intake", meta: [["workflow_run", <code key="w">wf_8b21f</code>]] },
  {
    id: "e3",
    occurredAt: at(1_830),
    actor: { type: "agent", role: "conductor" },
    eventType: "session.started",
    summary: "Orchestrator session started on worker-03 (claude-opus-4)",
    meta: [
      ["session", <code key="s">ses_01J9K2</code>],
      ["harness", "claude-code 2.1"],
    ],
  },
  { id: "e4", occurredAt: at(2_101), actor: { type: "agent", role: "conductor" }, eventType: "session.subagent.spawned", summary: "Spawned investigator: map webhook handler and existing retry patterns" },
  {
    id: "e5",
    occurredAt: at(2_155),
    actor: { type: "agent", role: "investigator" },
    eventType: "tool.call.completed",
    summary: (
      <>
        <code>grep</code> "webhook" — 14 matches in 6 files
      </>
    ),
    trailing: "182ms",
    detail: <pre>{`apps/control-plane/src/api/routes/webhooks.ts:12
apps/control-plane/src/api/routes/webhooks.ts:48
apps/control-plane/src/integrations/github/client.ts:7
…`}</pre>,
  },
  { id: "e6", occurredAt: at(6_440), actor: { type: "agent", role: "investigator" }, eventType: "tool.call.completed", summary: <><code>read</code> apps/control-plane/src/integrations/github/client.ts</>, trailing: "41ms" },
  { id: "e7", occurredAt: at(19_002), actor: { type: "agent", role: "investigator" }, eventType: "session.completed", summary: "Investigation complete: 3 findings, 1 risk", severity: "success", trailing: "$0.084" },
  { id: "e8", occurredAt: at(19_310), actor: { type: "agent", role: "conductor" }, eventType: "session.subagent.spawned", summary: "Spawned implementer: add exponential backoff (max 5 attempts) to GithubClient.post" },
  { id: "e9", occurredAt: at(44_870), actor: { type: "agent", role: "implementer" }, eventType: "tool.call.completed", summary: <><code>edit</code> apps/control-plane/src/integrations/github/client.ts (+38 −6)</>, trailing: "12ms" },
  { id: "e10", occurredAt: at(61_240), actor: { type: "agent", role: "implementer" }, eventType: "tool.call.failed", summary: <><code>bash</code> bun test — 1 failing: retries when response is 502</>, severity: "danger", trailing: "4.2s", detail: <pre>{`FAIL  src/integrations/github/client.test.ts
  ✗ retries when response is 502  (12ms)
    expected 5 calls, received 1`}</pre> },
  { id: "e11", occurredAt: at(88_003), actor: { type: "agent", role: "implementer" }, eventType: "tool.call.completed", summary: <><code>bash</code> bun test — 42 passing</>, severity: "success", trailing: "3.9s" },
  { id: "e12", occurredAt: at(90_110), actor: { type: "agent", role: "conductor" }, eventType: "question.asked", summary: "Should 4xx responses be retried? The existing code retries everything.", severity: "attention", meta: [["question", <code key="q">q_44a1</code>], ["blocking", "yes"]] },
  { id: "e13", occurredAt: at(90_200), actor: { type: "system" }, eventType: "workflow.transition", summary: "running → awaiting_input", severity: "attention" },
  { id: "e14", occurredAt: at(1_520_000), actor: { type: "human", name: "marcio" }, eventType: "question.answered", summary: "No — only retry 5xx and network errors." },
  { id: "e15", occurredAt: at(1_521_000), actor: { type: "system" }, eventType: "workflow.transition", summary: "awaiting_input → running" },
  { id: "e16", occurredAt: at(1_640_000), actor: { type: "agent", role: "reviewer" }, eventType: "review.finding", summary: "Backoff jitter uses Math.random; consider seeding for tests (minor)", trailing: "minor" },
  { id: "e17", occurredAt: at(1_700_000), actor: { type: "agent", role: "simplifier" }, eventType: "session.completed", summary: "Removed duplicated sleep helper; −14 lines", severity: "success", trailing: "$0.031" },
  { id: "e18", occurredAt: at(1_790_000), actor: { type: "integration", name: "github" }, eventType: "pr.opened", summary: "PR #412 opened: Add retry with backoff to GitHub client", meta: [["url", "github.com/dude/dude/pull/412"]] },
  { id: "e19", occurredAt: at(1_990_000), actor: { type: "integration", name: "github" }, eventType: "check.completed", summary: "ci / test — passed", severity: "success", trailing: "3m 12s" },
  { id: "e20", occurredAt: at(1_991_000), actor: { type: "system" }, eventType: "workflow.transition", summary: "review → ready_to_merge", severity: "success" },
];

export const sessionTree: SessionNodeData = {
  id: "ses_01J9K2",
  role: "conductor",
  status: "running",
  model: "claude-opus-4",
  activity: "Waiting for reviewer…",
  costUsd: 1.284,
  tokens: 412_300,
  startedAt: at(1_830),
  children: [
    { id: "ses_01J9K3", role: "investigator", status: "completed", model: "claude-sonnet-4", costUsd: 0.084, tokens: 61_200, startedAt: at(2_101), endedAt: at(19_002) },
    {
      id: "ses_01J9K4",
      role: "implementer",
      status: "completed",
      model: "claude-opus-4",
      costUsd: 0.912,
      tokens: 288_000,
      startedAt: at(19_310),
      endedAt: at(1_600_000),
      children: [{ id: "ses_01J9K5", role: "qa_browser", status: "failed", model: "claude-sonnet-4", costUsd: 0.044, tokens: 12_800, startedAt: at(100_000), endedAt: at(140_000) }],
    },
    { id: "ses_01J9K6", role: "reviewer", status: "running", model: "claude-opus-4", activity: "Reading client.ts", costUsd: 0.21, tokens: 48_000, startedAt: at(1_610_000) },
    { id: "ses_01J9K7", role: "simplifier", status: "pending", model: "claude-sonnet-4" },
  ],
};

export const sessionTreeWaiting: SessionNodeData = {
  id: "ses_02A",
  role: "conductor",
  status: "awaiting_input",
  model: "claude-opus-4",
  activity: "Asked: retry 4xx?",
  costUsd: 0.63,
  tokens: 190_000,
  startedAt: at(0),
  children: [
    { id: "ses_02B", role: "investigator", status: "completed", model: "claude-sonnet-4", costUsd: 0.07, tokens: 40_000, startedAt: at(1000), endedAt: at(20_000) },
    { id: "ses_02C", role: "implementer", status: "aborted", model: "claude-opus-4", costUsd: 0.31, tokens: 90_000, startedAt: at(21_000), endedAt: at(70_000) },
  ],
};

export const unifiedDiff = `diff --git a/apps/control-plane/src/integrations/github/client.ts b/apps/control-plane/src/integrations/github/client.ts
index 3f1a2b4..9c8d7e6 100644
--- a/apps/control-plane/src/integrations/github/client.ts
+++ b/apps/control-plane/src/integrations/github/client.ts
@@ -1,6 +1,15 @@
 import type { GithubConfig } from "./config.ts";

+const MAX_ATTEMPTS = 5;
+const BASE_DELAY_MS = 250;
+
+function isRetryable(status: number): boolean {
+  // Only server errors and rate limits; 4xx means our request is wrong.
+  return status >= 500 || status === 429;
+}
+
 export class GithubClient {
   constructor(private readonly config: GithubConfig) {}

@@ -12,10 +21,29 @@ export class GithubClient {
   async post<T>(path: string, body: unknown): Promise<T> {
-    const res = await fetch(this.url(path), {
-      method: "POST",
-      headers: this.headers(),
-      body: JSON.stringify(body),
-    });
-    if (!res.ok) throw new Error(\`GitHub \${res.status}\`);
-    return (await res.json()) as T;
+    let attempt = 0;
+    let lastError: unknown;
+    while (attempt < MAX_ATTEMPTS) {
+      attempt++;
+      try {
+        const res = await fetch(this.url(path), {
+          method: "POST",
+          headers: this.headers(),
+          body: JSON.stringify(body),
+        });
+        if (res.ok) return (await res.json()) as T;
+        if (!isRetryable(res.status)) throw new Error(\`GitHub \${res.status}\`);
+        lastError = new Error(\`GitHub \${res.status}\`);
+      } catch (err) {
+        lastError = err;
+      }
+      await sleep(BASE_DELAY_MS * 2 ** (attempt - 1));
+    }
+    throw lastError;
   }
 }
diff --git a/apps/control-plane/src/integrations/github/sleep.ts b/apps/control-plane/src/integrations/github/sleep.ts
new file mode 100644
index 0000000..a1b2c3d
--- /dev/null
+++ b/apps/control-plane/src/integrations/github/sleep.ts
@@ -0,0 +1,3 @@
+export function sleep(ms: number): Promise<void> {
+  return new Promise((resolve) => setTimeout(resolve, ms));
+}
diff --git a/apps/control-plane/src/util/wait.ts b/apps/control-plane/src/util/wait.ts
deleted file mode 100644
index d4e5f6a..0000000
--- a/apps/control-plane/src/util/wait.ts
+++ /dev/null
@@ -1,4 +0,0 @@
-// Duplicated helper; use integrations/github/sleep.ts
-export const wait = (ms: number) =>
-  new Promise((r) => setTimeout(r, ms));
-
diff --git a/docs/diagram.png b/docs/diagram.png
index 1111111..2222222 100644
Binary files a/docs/diagram.png and b/docs/diagram.png differ
`;

const LOG_SRC = [
  ["info", "stdout", "$ bun test src/integrations/github"],
  ["debug", "stdout", "bun test v1.3.10 (30e609e0)"],
  ["info", "stdout", ""],
  ["info", "stdout", "src/integrations/github/client.test.ts:"],
  ["info", "stdout", "\u001b[32m✓\u001b[0m posts JSON with auth header \u001b[2m[2.10ms]\u001b[0m"],
  ["info", "stdout", "✓ returns parsed body on 200 [0.41ms]"],
  ["error", "stderr", "\u001b[31m✗\u001b[0m retries when response is 502 \u001b[2m[12.02ms]\u001b[0m"],
  ["error", "stderr", "  error: expect(received).toBe(expected)"],
  ["error", "stderr", "  Expected: 5"],
  ["error", "stderr", "  Received: 1"],
  ["error", "stderr", "      at /workspace/src/integrations/github/client.test.ts:41:22"],
  ["info", "stdout", "✓ does not retry 404 [0.33ms]"],
  ["warn", "stderr", "warn: fetch mock reset between tests is deprecated; use mock.restore()"],
  ["info", "stdout", ""],
  ["info", "stdout", " 3 pass"],
  ["info", "stdout", " 1 fail"],
  ["info", "stdout", " 8 expect() calls"],
  ["info", "stdout", "Ran 4 tests across 1 file. [48.00ms]"],
  ["system", "stdout", "[harness] tool bash exited 1 after 4.21s"],
  ["system", "stdout", "[harness] model turn 14 — 2,481 in / 312 out tokens"],
] as const;

export const logLines: LogLine[] = LOG_SRC.map(([level, channel, text], i) => ({
  seq: i,
  text,
  level,
  channel,
  ts: at(61_240 + i * 137),
}));

/** Generate N lines of plausible noise for volume tests. */
export function bulkLog(n: number, startSeq = 0): LogLine[] {
  const templates = [
    "GET /api/v1/tasks?status=running 200 4.1ms",
    "worker-03 heartbeat ok (cpu 42%, mem 1.9G)",
    "session ses_01J9K6 model turn 27 — 1,902 in / 148 out",
    "ledger cursor advanced to 128331",
    "[harness] tool read completed in 3ms",
    "reconcile: 3 runs checked, 0 drift",
  ];
  return Array.from({ length: n }, (_, i) => ({
    seq: startSeq + i,
    text: templates[i % templates.length] ?? "",
    level: i % 47 === 0 ? "warn" : i % 113 === 0 ? "error" : "info",
    ts: at(200_000 + i * 53),
  }));
}
