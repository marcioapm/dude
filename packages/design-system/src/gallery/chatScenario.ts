/**
 * A scripted live scenario for the gallery: one Run watched from the
 * orchestrator's seat, played back step by step so every activity state,
 * the nested subagent, the retry, the blocking question and the plan
 * update are seen *moving*, not as a screenshot.
 */

import type { PlanItem } from "../components/AgentPlan.tsx";
import type { ActivityKind } from "../tokens/activity.ts";
import type { AgentRole, SessionStatus } from "@dude/domain";
import type { ToolCallStatus } from "../tokens/activity.ts";

export interface ScenarioTool {
  readonly id: string;
  readonly name: string;
  readonly args: unknown;
  readonly status: ToolCallStatus;
  readonly startedAt: number;
  readonly endedAt?: number | undefined;
  readonly result?: string | undefined;
  readonly diff?: string | undefined;
  readonly error?: string | undefined;
  readonly exitCode?: number | undefined;
}

export interface ScenarioTurn {
  readonly id: string;
  readonly kind: "agent" | "human" | "system" | "thread";
  readonly role: AgentRole | "human" | "system";
  readonly name?: string | undefined;
  readonly model?: string | undefined;
  readonly text: string;
  /** How many characters of `text` are visible (streaming). */
  readonly shown: number;
  readonly activity?: ActivityKind | undefined;
  readonly activitySince?: number | undefined;
  readonly tool?: string | undefined;
  readonly attempt?: number | undefined;
  readonly retryAt?: number | undefined;
  readonly intent?: "prompt" | "answer" | "steer" | undefined;
  readonly inReplyTo?: string | undefined;
  readonly startedAt: number;
  readonly endedAt?: number | undefined;
  readonly costUsd?: number | undefined;
  readonly tokens?: number | undefined;
  readonly tools: ReadonlyArray<ScenarioTool>;
  // thread-only
  readonly sessionId?: string | undefined;
  readonly threadStatus?: SessionStatus | undefined;
  readonly task?: string | undefined;
  readonly turns?: ReadonlyArray<ScenarioTurn> | undefined;
}

export interface ScenarioState {
  readonly status: SessionStatus;
  readonly costUsd: number;
  readonly tokens: number;
  readonly plan: ReadonlyArray<PlanItem>;
  readonly turns: ReadonlyArray<ScenarioTurn>;
  readonly question: { id: string; text: string; options: string[] } | null;
  readonly stepLabel: string;
  readonly done: boolean;
}

const ORCH_TEXT_1 = `I'll start by mapping the webhook handler and any existing retry patterns before changing anything.

**Plan**
1. Investigate the current \`GithubClient.post\` path and how failures surface
2. Add exponential backoff with jitter, capped at 5 attempts
3. Make the test suite green, then hand off to review`;

const INV_TEXT = `Found the call site. \`GithubClient.post\` throws on any non-2xx and there is **no retry anywhere** in the integration layer.

Three findings:

- \`apps/control-plane/src/integrations/github/client.ts:12\` — single \`fetch\`, no retry
- \`apps/control-plane/src/util/wait.ts\` — a duplicated \`sleep\` helper that should be removed
- The webhook route already dedupes by delivery ID, so retries are safe

One risk: the existing code would retry **4xx** too if we wrap it naively.`;

const ORCH_TEXT_2 = `The investigator confirmed there is no retry layer today. Delegating the implementation.`;

const IMPL_TEXT = `Added \`isRetryable\` (5xx and 429 only) and a bounded backoff loop around \`post\`. Running the tests.`;

const ORCH_TEXT_3 = `Tests pass. Before I open the PR I need a decision from you.`;

const ORCH_TEXT_4 = `Understood — retrying only 5xx and network errors. Opening the PR now.

\`\`\`diff
--- a/apps/control-plane/src/integrations/github/client.ts
+++ b/apps/control-plane/src/integrations/github/client.ts
@@ -1,4 +1,9 @@
 import type { GithubConfig } from "./config.ts";
+
+function isRetryable(status: number): boolean {
+  return status >= 500 || status === 429;
+}
\`\`\`

PR **#412** opened. Handing off to the reviewer.`;

const QUESTION = "Should 4xx responses be retried? The existing code retries everything, but 4xx usually means our request is wrong.";

interface Step {
  readonly label: string;
  readonly at: number;
  readonly apply: (s: Draft) => void;
}

interface Draft {
  status: SessionStatus;
  costUsd: number;
  tokens: number;
  plan: PlanItem[];
  turns: ScenarioTurn[];
  question: ScenarioState["question"];
}

function upd(turns: ScenarioTurn[], id: string, patch: Partial<ScenarioTurn>): void {
  const i = turns.findIndex((t) => t.id === id);
  const cur = turns[i];
  if (i === -1 || !cur) return;
  turns[i] = { ...cur, ...patch };
}
function updThreadTurn(turns: ScenarioTurn[], threadId: string, id: string, patch: Partial<ScenarioTurn>): void {
  const i = turns.findIndex((t) => t.id === threadId);
  const th = turns[i];
  if (i === -1 || !th || !th.turns) return;
  const inner = [...th.turns];
  upd(inner, id, patch);
  turns[i] = { ...th, turns: inner };
}
function pushThreadTurn(turns: ScenarioTurn[], threadId: string, t: ScenarioTurn): void {
  const i = turns.findIndex((x) => x.id === threadId);
  const th = turns[i];
  if (i === -1 || !th) return;
  turns[i] = { ...th, turns: [...(th.turns ?? []), t] };
}

/** Build the step list relative to a start time `t0` (ms). */
export function buildScenario(t0: number): ReadonlyArray<Step> {
  const T = (s: number) => t0 + s * 1000;
  const tools = (arr: ScenarioTool[]) => arr;
  return [
    {
      label: "Task received",
      at: 0,
      apply: (s) => {
        s.turns.push({ id: "h0", kind: "human", role: "human", name: "marcio", intent: "prompt", text: "Add retry with backoff to the GitHub webhook handler. Cap at 5 attempts, keep the public API unchanged.", shown: 999, startedAt: T(0), tools: [] });
        s.turns.push({ id: "s0", kind: "system", role: "system", text: "Session started on worker-03 · claude-opus-4", shown: 999, startedAt: T(1), tools: [] });
      },
    },
    {
      label: "Orchestrator thinking",
      at: 1.2,
      apply: (s) => {
        s.status = "running";
        s.turns.push({ id: "o1", kind: "agent", role: "orchestrator", model: "claude-opus-4", text: ORCH_TEXT_1, shown: 0, activity: "thinking", activitySince: T(1.2), startedAt: T(1.2), tools: [], costUsd: 0.004, tokens: 1_900 });
      },
    },
    ...streamSteps("o1", ORCH_TEXT_1, 3.0, 5.6, (s, shown, last) => {
      upd(s.turns, "o1", { shown, activity: last ? "tool" : "streaming", tool: last ? "todowrite" : undefined, activitySince: T(last ? 5.6 : 3.0) });
      s.costUsd = 0.004 + shown * 0.00002;
      s.tokens = 1_900 + shown;
    }),
    {
      label: "Plan written",
      at: 6.2,
      apply: (s) => {
        s.plan = [
          { content: "Map GithubClient.post and existing retry patterns", status: "in_progress", priority: "high" },
          { content: "Add exponential backoff (max 5 attempts) to GithubClient.post", status: "pending", priority: "high" },
          { content: "Run bun test and fix failures", status: "pending", priority: "medium" },
          { content: "Open PR and hand off to reviewer", status: "pending", priority: "medium" },
        ];
        upd(s.turns, "o1", { activity: "tool", tool: "task", activitySince: T(6.2), tools: tools([{ id: "t0", name: "todowrite", args: { todos: s.plan }, status: "completed", startedAt: T(5.6), endedAt: T(5.7) }]) });
      },
    },
    {
      label: "Subagent spawned: investigator",
      at: 7,
      apply: (s) => {
        s.turns.push({
          id: "th1",
          kind: "thread",
          role: "investigator",
          sessionId: "ses_01J9K3",
          threadStatus: "running",
          task: "Map webhook handler and existing retry patterns",
          model: "claude-sonnet-4",
          text: "",
          shown: 0,
          startedAt: T(7),
          costUsd: 0.002,
          tools: [],
          turns: [{ id: "i1", kind: "agent", role: "investigator", model: "claude-sonnet-4", text: INV_TEXT, shown: 0, activity: "thinking", activitySince: T(7), startedAt: T(7), tools: [], costUsd: 0.002, tokens: 800 }],
        });
      },
    },
    {
      label: "Investigator: grep",
      at: 8.5,
      apply: (s) => {
        updThreadTurn(s.turns, "th1", "i1", {
          activity: "tool",
          tool: "grep",
          activitySince: T(8.5),
          tools: tools([{ id: "it1", name: "grep", args: { pattern: "webhook", path: "apps/control-plane/src" }, status: "running", startedAt: T(8.5) }]),
        });
      },
    },
    {
      label: "Investigator: grep done, read",
      at: 9.4,
      apply: (s) => {
        updThreadTurn(s.turns, "th1", "i1", {
          tool: "read",
          activitySince: T(9.4),
          tools: tools([
            { id: "it1", name: "grep", args: { pattern: "webhook", path: "apps/control-plane/src" }, status: "completed", startedAt: T(8.5), endedAt: T(8.7), result: "apps/control-plane/src/api/routes/webhooks.ts:12\napps/control-plane/src/api/routes/webhooks.ts:48\napps/control-plane/src/integrations/github/client.ts:7\n… 11 more" },
            { id: "it2", name: "read", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "running", startedAt: T(9.4) },
          ]),
        });
      },
    },
    {
      label: "Investigator writing",
      at: 10.2,
      apply: (s) => {
        const th = s.turns.find((t) => t.id === "th1");
        const i1 = th?.turns?.find((t) => t.id === "i1");
        const prev = i1?.tools ?? [];
        const first = prev[0];
        updThreadTurn(s.turns, "th1", "i1", {
          activity: "streaming",
          activitySince: T(10.2),
          tools: tools([...(first ? [first] : []), { id: "it2", name: "read", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "completed", startedAt: T(9.4), endedAt: T(9.45), result: "import type { GithubConfig } from \"./config.ts\";\n\nexport class GithubClient {\n  constructor(private readonly config: GithubConfig) {}\n  async post<T>(path: string, body: unknown): Promise<T> {\n    const res = await fetch(this.url(path), { method: \"POST\", headers: this.headers(), body: JSON.stringify(body) });\n    if (!res.ok) throw new Error(`GitHub ${res.status}`);\n    return (await res.json()) as T;\n  }\n}" }]),
        });
      },
    },
    ...streamSteps("i1", INV_TEXT, 10.4, 14.4, (s, shown, last) => {
      updThreadTurn(s.turns, "th1", "i1", { shown, activity: last ? "completed" : "streaming", endedAt: last ? T(14.4) : undefined, costUsd: 0.002 + shown * 0.0001, tokens: 800 + shown * 3 });
      if (last) {
        upd(s.turns, "th1", { threadStatus: "completed", endedAt: T(14.4), costUsd: 0.084 });
        s.plan = s.plan.map((p, i) => (i === 0 ? { ...p, status: "completed" } : i === 1 ? { ...p, status: "in_progress" } : p));
      }
      s.costUsd = 0.02 + shown * 0.0001;
      s.tokens += 3;
    }),
    {
      label: "Orchestrator resumes",
      at: 15,
      apply: (s) => {
        upd(s.turns, "o1", { activity: "completed", endedAt: T(15), tool: undefined, costUsd: 0.06, tokens: 6_100 });
        s.turns.push({ id: "o2", kind: "agent", role: "orchestrator", model: "claude-opus-4", text: ORCH_TEXT_2, shown: 0, activity: "thinking", activitySince: T(15), startedAt: T(15), tools: [], costUsd: 0.001, tokens: 400 });
      },
    },
    ...streamSteps("o2", ORCH_TEXT_2, 16, 17.2, (s, shown, last) => {
      upd(s.turns, "o2", { shown, activity: last ? "tool" : "streaming", tool: last ? "task" : undefined, activitySince: T(last ? 17.2 : 16) });
    }),
    {
      label: "Subagent spawned: implementer",
      at: 17.6,
      apply: (s) => {
        s.turns.push({
          id: "th2",
          kind: "thread",
          role: "implementer",
          sessionId: "ses_01J9K4",
          threadStatus: "running",
          task: "Add exponential backoff (max 5 attempts) to GithubClient.post",
          model: "claude-opus-4",
          text: "",
          shown: 0,
          startedAt: T(17.6),
          costUsd: 0.003,
          tools: [],
          turns: [{ id: "m1", kind: "agent", role: "implementer", model: "claude-opus-4", text: IMPL_TEXT, shown: 0, activity: "thinking", activitySince: T(17.6), startedAt: T(17.6), tools: [], costUsd: 0.003, tokens: 1_200 }],
        });
      },
    },
    {
      label: "Implementer: edit",
      at: 19,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", {
          activity: "tool",
          tool: "edit",
          activitySince: T(19),
          tools: tools([{ id: "mt1", name: "edit", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "running", startedAt: T(19) }]),
        });
      },
    },
    {
      label: "Implementer: bash (this one is slow)",
      at: 20,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", {
          tool: "bash",
          activitySince: T(20),
          tools: tools([
            { id: "mt1", name: "edit", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "completed", startedAt: T(19), endedAt: T(19.02), diff: IMPL_DIFF },
            { id: "mt2", name: "bash", args: { command: "bun test src/integrations/github" }, status: "running", startedAt: T(20) },
          ]),
        });
      },
    },
    ...streamSteps("m1", IMPL_TEXT, 20.4, 21.6, (s, shown) => {
      updThreadTurn(s.turns, "th2", "m1", { shown });
    }),
    {
      label: "bash still running… (slow after 20s is compressed here to 6s)",
      at: 26,
      apply: (s) => {
        // Keep as is; the demo's slowAfterMs is set short so this reads as slow.
        s.costUsd += 0.01;
      },
    },
    {
      label: "bash failed",
      at: 28,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", {
          activity: "thinking",
          tool: undefined,
          activitySince: T(28),
          tools: tools([
            { id: "mt1", name: "edit", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "completed", startedAt: T(19), endedAt: T(19.02), diff: IMPL_DIFF },
            { id: "mt2", name: "bash", args: { command: "bun test src/integrations/github" }, status: "failed", startedAt: T(20), endedAt: T(28), exitCode: 1, error: "1 failing: retries when response is 502\n  expected 5 calls, received 1\n    at src/integrations/github/client.test.ts:41:22" },
          ]),
        });
      },
    },
    {
      label: "Upstream 429 — retrying with backoff",
      at: 29.5,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", { activity: "retrying", attempt: 2, activitySince: T(29.5), retryAt: T(35.5) });
      },
    },
    {
      label: "Retry attempt 3",
      at: 35.6,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", { activity: "retrying", attempt: 3, activitySince: T(35.6), retryAt: T(41.6) });
      },
    },
    {
      label: "Recovered; bash again",
      at: 41.8,
      apply: (s) => {
        updThreadTurn(s.turns, "th2", "m1", {
          activity: "tool",
          tool: "bash",
          activitySince: T(41.8),
          attempt: undefined,
          retryAt: undefined,
          tools: tools([
            { id: "mt1", name: "edit", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "completed", startedAt: T(19), endedAt: T(19.02), diff: IMPL_DIFF },
            { id: "mt2", name: "bash", args: { command: "bun test src/integrations/github" }, status: "failed", startedAt: T(20), endedAt: T(28), exitCode: 1, error: "1 failing: retries when response is 502\n  expected 5 calls, received 1\n    at src/integrations/github/client.test.ts:41:22" },
            { id: "mt3", name: "edit", args: { file_path: "apps/control-plane/src/integrations/github/client.ts" }, status: "completed", startedAt: T(41.5), endedAt: T(41.52) },
            { id: "mt4", name: "bash", args: { command: "bun test src/integrations/github" }, status: "running", startedAt: T(41.8) },
          ]),
        });
      },
    },
    {
      label: "Tests green; implementer done",
      at: 45,
      apply: (s) => {
        const th = s.turns.find((t) => t.id === "th2");
        const m1 = th?.turns?.find((t) => t.id === "m1");
        const prev = (m1?.tools ?? []).slice(0, 3);
        updThreadTurn(s.turns, "th2", "m1", {
          activity: "completed",
          tool: undefined,
          endedAt: T(45),
          costUsd: 0.91,
          tokens: 288_000,
          tools: tools([...prev, { id: "mt4", name: "bash", args: { command: "bun test src/integrations/github" }, status: "completed", startedAt: T(41.8), endedAt: T(45), exitCode: 0, result: " 4 pass\n 0 fail\n 9 expect() calls\nRan 4 tests across 1 file. [51.00ms]" }]),
        });
        upd(s.turns, "th2", { threadStatus: "completed", endedAt: T(45), costUsd: 0.912 });
        upd(s.turns, "o2", { activity: "completed", endedAt: T(45), tool: undefined, costUsd: 0.03, tokens: 2_800 });
        s.plan = s.plan.map((p, i) => (i <= 2 ? { ...p, status: "completed" } : { ...p, status: "in_progress" }));
        s.costUsd = 1.1;
        s.tokens = 380_000;
      },
    },
    {
      label: "Orchestrator asks a question",
      at: 46,
      apply: (s) => {
        s.turns.push({ id: "o3", kind: "agent", role: "orchestrator", model: "claude-opus-4", text: ORCH_TEXT_3, shown: 0, activity: "thinking", activitySince: T(46), startedAt: T(46), tools: [], costUsd: 0.002, tokens: 300 });
      },
    },
    ...streamSteps("o3", ORCH_TEXT_3, 47, 48, (s, shown, last) => {
      upd(s.turns, "o3", { shown, activity: last ? "awaiting_input" : "streaming", activitySince: T(last ? 48 : 47) });
      if (last) {
        s.status = "awaiting_input";
        s.question = { id: "q_44a1", text: QUESTION, options: ["Retry 5xx and network only", "Retry everything (current behaviour)"] };
      }
    }),
  ];
}

/** Steps after the operator answers. */
export function buildAnswerSteps(t0: number, answer: string): ReadonlyArray<Step> {
  const T = (s: number) => t0 + s * 1000;
  return [
    {
      label: "Answered",
      at: 0,
      apply: (s) => {
        s.question = null;
        s.status = "running";
        upd(s.turns, "o3", { activity: "completed", endedAt: T(0) });
        s.turns.push({ id: "h1", kind: "human", role: "human", name: "marcio", intent: "answer", inReplyTo: QUESTION, text: answer, shown: 999, startedAt: T(0), tools: [] });
        s.turns.push({ id: "o4", kind: "agent", role: "orchestrator", model: "claude-opus-4", text: ORCH_TEXT_4, shown: 0, activity: "thinking", activitySince: T(0.2), startedAt: T(0.2), tools: [], costUsd: 0.003, tokens: 900 });
      },
    },
    ...streamSteps("o4", ORCH_TEXT_4, 1.2, 5.5, (s, shown, last) => {
      upd(s.turns, "o4", { shown, activity: last ? "completed" : "streaming", activitySince: T(1.2), endedAt: last ? T(5.5) : undefined, costUsd: 0.003 + shown * 0.00005 });
      if (last) {
        s.plan = s.plan.map((p) => ({ ...p, status: "completed" }));
        s.status = "completed";
        s.turns.push({ id: "s9", kind: "system", role: "system", text: "PR #412 opened · session completed", shown: 999, startedAt: T(5.5), tools: [] });
      }
    }),
  ];
}

/** Steps after a steer while running. */
export function buildSteerSteps(t0: number, instruction: string): ReadonlyArray<Step> {
  const T = (s: number) => t0 + s * 1000;
  return [
    {
      label: "Steered",
      at: 0,
      apply: (s) => {
        s.turns.push({ id: `st${t0}`, kind: "human", role: "human", name: "marcio", intent: "steer", text: instruction, shown: 999, startedAt: T(0), tools: [] });
        s.turns.push({ id: `sys${t0}`, kind: "system", role: "system", text: "Current turn interrupted · directive recorded", shown: 999, startedAt: T(0.1), tools: [] });
      },
    },
  ];
}

function streamSteps(id: string, text: string, from: number, to: number, apply: (s: Draft, shown: number, last: boolean) => void): Step[] {
  const n = 14;
  const out: Step[] = [];
  for (let k = 1; k <= n; k++) {
    const shown = k === n ? text.length : Math.floor((text.length * k) / n);
    out.push({ label: `${id} streaming`, at: from + ((to - from) * (k - 1)) / (n - 1), apply: (s) => apply(s, shown, k === n) });
  }
  return out;
}

const IMPL_DIFF = `diff --git a/apps/control-plane/src/integrations/github/client.ts b/apps/control-plane/src/integrations/github/client.ts
--- a/apps/control-plane/src/integrations/github/client.ts
+++ b/apps/control-plane/src/integrations/github/client.ts
@@ -1,4 +1,11 @@
 import type { GithubConfig } from "./config.ts";
+
+const MAX_ATTEMPTS = 5;
+const BASE_DELAY_MS = 250;
+
+function isRetryable(status: number): boolean {
+  return status >= 500 || status === 429;
+}

 export class GithubClient {
   constructor(private readonly config: GithubConfig) {}
`;

export function initialDraft(): Draft {
  return { status: "pending", costUsd: 0, tokens: 0, plan: [], turns: [], question: null };
}

export function runSteps(draft: Draft, steps: ReadonlyArray<Step>, uptoSec: number, fromIndex: number): number {
  let i = fromIndex;
  while (i < steps.length) {
    const st = steps[i];
    if (!st || st.at > uptoSec) break;
    st.apply(draft);
    i++;
  }
  return i;
}

export function snapshot(d: Draft, label: string, done: boolean): ScenarioState {
  return { status: d.status, costUsd: d.costUsd, tokens: d.tokens, plan: d.plan, turns: d.turns, question: d.question, stepLabel: label, done };
}

export type { Draft as ScenarioDraft, Step as ScenarioStep };
