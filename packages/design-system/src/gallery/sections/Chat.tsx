import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { ActivityIndicator } from "../../components/ActivityIndicator.tsx";
import { AgentPlan, type PlanItem } from "../../components/AgentPlan.tsx";
import { ChatComposer, type ComposerSubmission } from "../../components/ChatComposer.tsx";
import { ChatMessage } from "../../components/ChatMessage.tsx";
import { ChatThread } from "../../components/ChatThread.tsx";
import { ChatTranscript } from "../../components/ChatTranscript.tsx";
import { Markdown } from "../../components/Markdown.tsx";
import { ToolCallCard } from "../../components/ToolCallCard.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { ACTIVITY_KINDS, ACTIVITY_SPECS } from "../../tokens/activity.ts";
import { at } from "../fixtures.tsx";
import {
  buildAnswerSteps,
  buildScenario,
  buildSteerSteps,
  initialDraft,
  runSteps,
  snapshot,
  type ScenarioDraft,
  type ScenarioState,
  type ScenarioStep,
  type ScenarioTurn,
} from "../chatScenario.ts";

const MD_MESSAGE = `I looked at the handler and the failure path. Two things stand out:

- \`GithubClient.post\` throws on **any** non-2xx — there is no retry layer at all
- the webhook route already dedupes by delivery ID, so retries are *safe*

Proposed change (bounded, jittered):

\`\`\`ts
const MAX_ATTEMPTS = 5;
function isRetryable(status: number): boolean {
  return status >= 500 || status === 429;
}
\`\`\`

| Status | Retry? | Why |
|-------:|:------:|-----|
| 5xx | yes | transient upstream |
| 429 | yes | rate limited |
| 4xx | no | our request is wrong |

> Open question: should a human steering message invalidate prior test results?

Next I'll run \`bun test\` and open a PR. See [the plan](https://example.com/plan#retries) for context.`;

const MD_DOCUMENT = `# Investigation: GitHub webhook retries

## Summary

The control plane's GitHub integration has **no retry layer**. Every non-2xx from \`api.github.com\` surfaces immediately as a failed webhook delivery, which the reconciler then treats as a lost event.

## Findings

### 1. Single fetch, no backoff

\`apps/control-plane/src/integrations/github/client.ts:12\` performs one \`fetch\` and throws on \`!res.ok\`. There is no distinction between a 502 (transient) and a 422 (our payload is wrong).

### 2. Duplicated sleep helper

\`apps/control-plane/src/util/wait.ts\` duplicates \`integrations/github/sleep.ts\`. One should go.

### 3. Deliveries are already idempotent

The webhook route dedupes on \`X-GitHub-Delivery\`, so retrying a POST cannot double-apply.

## Risks

- [x] Retrying 4xx would mask real bugs — **must not** retry 4xx
- [ ] Jitter uses \`Math.random\`; tests should seed it
- [ ] Backoff cap must stay under the 30s webhook timeout

## Proposed change

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

## Untrusted input check

Raw HTML like <img src=x onerror=alert(1)> is shown literally, and a [javascript link](javascript:alert(1)) renders as plain text.

---

*Published by the investigator · 3 findings, 1 risk.*`;

const SLOW_DEMO_MS = 6_000;

export function ChatSection({ mode }: { readonly mode: PaneMode }) {
  return (
    <Section
      id="chat"
      title="Chat: the operator's screen"
      intro="The conversational view of one Session. This is what an operator has open all day: the agent's narrative, its tool calls, the subagents it delegates to, its plan, and the two ways a human intervenes. Event streams and logs are for debugging; this is for watching and steering."
    >
      <Block
        id="ch-activity"
        title="ActivityIndicator"
        note={
          <>
            What a turn is doing <em>right now</em>. Each state has its own tone, glyph and <em>rhythm</em>: thinking drifts a dashed ring, writing blinks a caret, a tool sweeps a track under a ticking clock and turns attention-toned once slow, retrying depletes a countdown ring beside the attempt number. Needs-you reuses the StatusBadge ring unchanged, so it stays the loudest thing. Under reduced motion every one freezes in a legible pose; the clocks keep ticking as text.
          </>
        }
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>Line variant (foot of a message)</Label>
            <States
              items={[
                ["thinking", <ActivityIndicator kind="thinking" since={Date.now() - 4_000} />],
                ["streaming", <ActivityIndicator kind="streaming" since={Date.now() - 9_000} detail="312 tokens" />],
                ["tool (fast)", <ActivityIndicator kind="tool" tool="read" since={Date.now() - 800} detail={<code>client.ts</code>} />],
                ["tool (slow)", <ActivityIndicator kind="tool" tool="bash" since={Date.now() - 41_000} detail={<code>bun test src/integrations</code>} />],
                ["retrying", <RetryDemo />],
                ["awaiting_input", <ActivityIndicator kind="awaiting_input" since={Date.now() - 125_000} detail="Should 4xx be retried?" />],
                ["completed", <ActivityIndicator kind="completed" since={at(0)} detail="2 tool calls" />],
                ["failed", <ActivityIndicator kind="failed" detail="upstream 500 after 5 attempts" />],
                ["aborted", <ActivityIndicator kind="aborted" detail="stopped by marcio" />],
              ]}
            />
            <Label>Badge variant (headers, collapsed threads)</Label>
            <Row>
              {ACTIVITY_KINDS.map((k) => (
                <ActivityIndicator key={k} kind={k} variant="badge" since={k === "tool" ? Date.now() - 41_000 : undefined} tool={k === "tool" ? "bash" : undefined} attempt={k === "retrying" ? 2 : undefined} maxAttempts={k === "retrying" ? 5 : undefined} />
              ))}
            </Row>
            <Label>Grayscale check — rhythm and glyph still separate them</Label>
            <Row style={{ filter: "grayscale(1)" }}>
              {ACTIVITY_KINDS.map((k) => (
                <ActivityIndicator key={k} kind={k} variant="badge" size="sm" />
              ))}
            </Row>
            <div className={styles["statusMatrix"]} style={{ gridTemplateColumns: "max-content max-content max-content minmax(0,1fr)" }}>
              {ACTIVITY_KINDS.map((k) => {
                const s = ACTIVITY_SPECS[k];
                return (
                  <FragmentRow key={k} cells={[<Caption>{k}</Caption>, <Caption>{s.tone}</Caption>, <Caption>{s.motion}</Caption>, <span style={{ fontSize: 12, color: "var(--ds-color-text-secondary)" }}>{s.description}</span>]} />
                );
              })}
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-tool"
        title="ToolCallCard"
        note="One 28px row per call: glyph · name · what it was called with · duration · outcome. Running calls sweep along the bottom edge with a ticking duration that turns attention-toned once slow. Failed calls open by default and put the error's first line in the row itself. A diff hands off to DiffView."
      >
        <Panes mode={mode} surface>
          <Col>
            <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} durationMs={41} result={"import type { GithubConfig } from \"./config.ts\";\n\nexport class GithubClient {\n  constructor(private readonly config: GithubConfig) {}\n}"} />
            <ToolCallCard name="grep" status="completed" args={{ pattern: "webhook", path: "apps/control-plane/src", include: "*.ts" }} durationMs={182} result={"apps/control-plane/src/api/routes/webhooks.ts:12\napps/control-plane/src/api/routes/webhooks.ts:48\napps/control-plane/src/integrations/github/client.ts:7"} />
            <ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 1_200} />
            <ToolCallCard name="bash" status="running" args={{ command: "bun install && bun run build" }} startedAt={Date.now() - 47_000} />
            <ToolCallCard name="bash" status="failed" args={{ command: "bun test src/integrations/github" }} durationMs={4_210} exitCode={1} error={"1 failing: retries when response is 502\n  expected 5 calls, received 1\n    at src/integrations/github/client.test.ts:41:22"} result={" 3 pass\n 1 fail\n 8 expect() calls"} />
            <ToolCallCard
              name="edit"
              status="completed"
              args={{ file_path: "apps/control-plane/src/integrations/github/client.ts", old_string: "if (!res.ok) throw", new_string: "if (!isRetryable(res.status)) throw" }}
              durationMs={12}
              diff={`diff --git a/apps/control-plane/src/integrations/github/client.ts b/apps/control-plane/src/integrations/github/client.ts
--- a/apps/control-plane/src/integrations/github/client.ts
+++ b/apps/control-plane/src/integrations/github/client.ts
@@ -12,3 +12,3 @@
   async post<T>(path: string, body: unknown): Promise<T> {
-    if (!res.ok) throw new Error(\`GitHub \${res.status}\`);
+    if (!isRetryable(res.status)) throw new Error(\`GitHub \${res.status}\`);
`}
            />
            <ToolCallCard name="webfetch" status="aborted" args={{ url: "https://docs.github.com/en/rest/webhooks" }} durationMs={8_000} summary="docs.github.com/en/rest/webhooks — aborted by operator" />
            <ToolCallCard name="publish_artifact" status="completed" args={{ path: "/workspace/artifacts/investigation.md", title: "Investigation: webhook retries" }} durationMs={320} result="ART-8291" />
            <ToolCallCard name="todowrite" status="completed" args={{ todos: PLAN_BASE }} durationMs={9} icon="list-check" />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-plan"
        title="AgentPlan"
        note="The agent's running plan from todowrite. Rendered in place — the current list, not one card per rewrite — with a segmented bar and 'N of M' so progress reads at a glance. A status change flashes its row once and pops the glyph. Collapsed, it shows the current item. Press Advance to watch it move."
      >
        <Panes mode={mode} surface>
          <PlanDemo />
        </Panes>
      </Block>

      <Block
        id="ch-markdown"
        title="Markdown"
        note={
          <>
            Rendered from a typed AST — never an HTML string — so raw HTML shows literally and <code>javascript:</code> URLs are dropped. Code blocks share their type with LogStream; a <code>```diff</code> fence hands off to DiffView. <code>message</code> is the chat rhythm (13px, 72ch); <code>document</code> is for published artifacts (84ch, more air, an outline). Streaming renders unterminated constructs as open so nothing flickers when the closer lands.
          </>
        }
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>message variant</Label>
            <Markdown source={MD_MESSAGE} />
            <StreamingMarkdownDemo />
          </Col>
        </Panes>
        <div style={{ height: 8 }} />
        <Panes mode={mode}>
          <Col>
            <Label>document variant with outline</Label>
            <Markdown source={MD_DOCUMENT} variant="document" outline />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-message"
        title="ChatMessage"
        note="A turn is a gutter and a column, not a bubble, so text aligns down the page. Agent turns carry model, elapsed, tokens, cost and the live activity in the foot. Human turns get a hairline frame tinted by intent — an answer is attention-toned (it closes the needs-you state), a steer is accent-toned (the operator reaching in) — so interventions are scannable in a long transcript."
      >
        <Panes mode={mode} surface>
          <Col>
            <ChatMessage role="human" name="marcio" intent="prompt" content="Add retry with backoff to the GitHub webhook handler. Cap at 5 attempts, keep the public API unchanged." startedAt={at(0)} />
            <ChatMessage role="system" content="Session started on worker-03 · claude-opus-4" startedAt={at(1_000)} />
            <ChatMessage role="orchestrator" model="claude-opus-4" content={"I'll map the handler first, then delegate.\n\n**Plan**\n1. Investigate `GithubClient.post`\n2. Add bounded backoff\n3. Tests, then review"} startedAt={at(1_830)} endedAt={at(19_000)} costUsd={0.06} tokens={6_100} activity="completed" />
            <ChatMessage role="implementer" model="claude-opus-4" content="Added `isRetryable` and a bounded loop. Running the tests" streaming startedAt={Date.now() - 38_000} costUsd={0.21} tokens={48_000} activity="streaming" activityProps={{ since: Date.now() - 6_000, detail: "312 tokens" }} />
            <ChatMessage role="implementer" model="claude-opus-4" content="Tests are running." continued attachments={<ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 44_000} />} activity="tool" activityProps={{ tool: "bash", since: Date.now() - 44_000 }} startedAt={Date.now() - 61_000} costUsd={0.23} />
            <ChatMessage role="orchestrator" model="claude-opus-4" content="Before I open the PR I need a decision from you." startedAt={Date.now() - 125_000} costUsd={0.002} activity="awaiting_input" activityProps={{ since: Date.now() - 125_000, detail: "Should 4xx be retried?" }} />
            <ChatMessage role="human" name="marcio" intent="answer" inReplyTo="Should 4xx responses be retried? The existing code retries everything." content="No — only retry 5xx and network errors." startedAt={at(1_520_000)} />
            <ChatMessage role="human" name="marcio" intent="steer" content="Do not change the public API of GithubClient. Add the retry inside `post` only." startedAt={at(1_530_000)} />
            <ChatMessage role="reviewer" model="claude-opus-4" content="Backoff jitter uses `Math.random`; consider seeding for tests (minor)." startedAt={at(1_640_000)} endedAt={at(1_650_000)} costUsd={0.03} activity="failed" activityProps={{ detail: "upstream 500 after 5 attempts" }} />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-thread"
        title="ChatThread (nested subagents)"
        note="A subagent's conversation nested in its parent's. The 2px rail is the child's role colour; depth is the count of rails to the left, indented 12px then 8px so three levels still leave a readable measure. Depth 1 opens; depth 2 starts collapsed; depth 3+ shows only the header with an Open action. A collapsed live thread keeps its activity in the header, so you can still see it is waiting on a 40-second bash. A child's own plan lives inside its thread, flat and collapsed — only the watched session pins a plan."
      >
        <Panes mode={mode} surface>
          <Col>
            <ChatThread sessionId="ses_01J9K3" role="investigator" status="completed" model="claude-sonnet-4" task="Map webhook handler and existing retry patterns" startedAt={at(2_101)} endedAt={at(19_002)} costUsd={0.084} turnCount={3} summary="3 findings, 1 risk · no retry layer exists today">
              <ChatMessage role="investigator" model="claude-sonnet-4" content="Found the call site. No retry anywhere in the integration layer." startedAt={at(2_101)} endedAt={at(19_002)} costUsd={0.084} activity="completed" />
            </ChatThread>
            <ChatThread sessionId="ses_01J9K4" role="implementer" status="running" model="claude-opus-4" task="Add exponential backoff (max 5 attempts) to GithubClient.post" startedAt={Date.now() - 90_000} costUsd={0.41} activity="tool" activityProps={{ tool: "bash", since: Date.now() - 44_000 }} onOpen={() => undefined}>
              <AgentPlan flat defaultCollapsed items={[{ content: "Add isRetryable and bounded loop", status: "completed" }, { content: "Run bun test", status: "in_progress" }, { content: "Remove duplicated sleep helper", status: "pending" }]} meta="this subagent's own plan" />
              <ChatMessage role="implementer" model="claude-opus-4" content="Added `isRetryable` and a bounded loop. Running the tests." startedAt={Date.now() - 90_000} costUsd={0.41} activity="tool" activityProps={{ tool: "bash", since: Date.now() - 44_000 }} attachments={<ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 44_000} />} />
              <ChatThread sessionId="ses_01J9K5" role="qa_browser" status="running" model="claude-sonnet-4" task="Verify the webhook settings page still renders" depth={2} startedAt={Date.now() - 30_000} costUsd={0.02} activity="thinking" activityProps={{ since: Date.now() - 3_000 }} onOpen={() => undefined}>
                <ChatMessage role="qa_browser" model="claude-sonnet-4" content="Navigating to /settings/webhooks." startedAt={Date.now() - 30_000} activity="thinking" activityProps={{ since: Date.now() - 3_000 }} />
                <ChatThread sessionId="ses_01J9K8" role="investigator" status="running" model="claude-sonnet-4" task="Find the selector for the save button" depth={3} startedAt={Date.now() - 10_000} costUsd={0.004} onOpen={() => undefined} />
              </ChatThread>
            </ChatThread>
            <ChatThread sessionId="ses_02C" role="implementer" status="aborted" model="claude-opus-4" task="Naive retry wrapper (superseded by steer)" startedAt={at(21_000)} endedAt={at(70_000)} costUsd={0.31} turnCount={2} summary="Stopped by marcio after steer" />
            <ChatThread sessionId="ses_01J9K5b" role="qa_browser" status="failed" model="claude-sonnet-4" task="Verify the webhook settings page still renders" startedAt={at(100_000)} endedAt={at(140_000)} costUsd={0.044} turnCount={4} summary="Timed out waiting for #save-webhook" />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-composer"
        title="ChatComposer"
        note="The two ways a human intervenes are distinct on four channels: frame tint, hint text, button label and button icon. Answer is attention-toned with the question quoted above and one-click options; Enter submits because the agent is waiting. Steer is accent-toned, says plainly that it interrupts the current turn, and needs ⌘/Ctrl+Enter — an accidental interrupt costs a turn."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>answer — session blocked on a question</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
              <ChatComposer question={{ id: "q_44a1", askedBy: "Orchestrator", text: "Should 4xx responses be retried? The existing code retries everything, but 4xx usually means our request is wrong.", options: ["Retry 5xx and network only", "Retry everything (current behaviour)"] }} onSubmit={() => undefined} />
            </div>
            <Label>steer — session running</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
              <ChatComposer running onSubmit={() => undefined} defaultValue="Do not change the public API of GithubClient." />
            </div>
            <Label>prompt — nothing running yet</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
              <ChatComposer onSubmit={() => undefined} />
            </div>
            <Label>disabled — terminal session</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
              <ChatComposer disabled disabledReason="This session completed. Start a new run to continue." onSubmit={() => undefined} />
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-transcript"
        title="ChatTranscript — live scenario"
        note="The whole thing, played back: a task arrives, the orchestrator thinks, writes a plan, delegates to an investigator (nested), delegates to an implementer whose bash call runs long and then fails, hits a 429 and backs off twice, recovers, then asks you a question. Answer it in the composer to let it finish; steer it while it runs. Scroll up mid-stream: the view stops following and offers 'Jump to latest' with a count. The tool 'slow' threshold is 6s here (20s in the product) so the state is reachable."
      >
        <Panes mode={mode}>
          <LiveTranscriptDemo />
        </Panes>
      </Block>
    </Section>
  );
}

function FragmentRow({ cells }: { readonly cells: ReadonlyArray<ReactNode> }) {
  return (
    <>
      {cells.map((c, i) => (
        <span key={i}>{c}</span>
      ))}
    </>
  );
}

// ---------------------------------------------------------------------------

function RetryDemo() {
  const [cycle, setCycle] = useState(() => ({ since: Date.now(), retryAt: Date.now() + 8_000, attempt: 2 }));
  useEffect(() => {
    const id = setInterval(() => {
      setCycle((c) => (Date.now() >= c.retryAt ? { since: Date.now(), retryAt: Date.now() + 8_000, attempt: c.attempt >= 5 ? 2 : c.attempt + 1 } : c));
    }, 500);
    return () => clearInterval(id);
  }, []);
  return <ActivityIndicator kind="retrying" since={cycle.since} retryAt={cycle.retryAt} attempt={cycle.attempt} maxAttempts={5} detail={<>429 from <code>api.anthropic.com</code></>} />;
}

const PLAN_BASE: PlanItem[] = [
  { content: "Map GithubClient.post and existing retry patterns", status: "completed", priority: "high" },
  { content: "Add exponential backoff (max 5 attempts) to GithubClient.post", status: "in_progress", priority: "high" },
  { content: "Run bun test and fix failures", status: "pending", priority: "medium" },
  { content: "Remove duplicated sleep helper", status: "pending", priority: "low" },
  { content: "Open PR and hand off to reviewer", status: "pending", priority: "medium" },
];

function PlanDemo() {
  const [items, setItems] = useState<PlanItem[]>(PLAN_BASE);
  const advance = () => {
    setItems((cur) => {
      const i = cur.findIndex((p) => p.status === "in_progress");
      if (i === -1) return PLAN_BASE;
      return cur.map((p, j) => (j === i ? { ...p, status: "completed" } : j === i + 1 ? { ...p, status: "in_progress" } : p));
    });
  };
  return (
    <Col>
      <Row>
        <Button size="sm" onClick={advance}>
          Advance
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setItems(PLAN_BASE)}>
          Reset
        </Button>
      </Row>
      <AgentPlan items={items} meta="orchestrator" />
      <Label>Collapsed: progress and the current item</Label>
      <AgentPlan items={items} defaultCollapsed />
      <Label>All done</Label>
      <AgentPlan items={PLAN_BASE.map((p) => ({ ...p, status: "completed" }))} collapsible={false} />
      <Label>With a cancelled item and mixed priority</Label>
      <AgentPlan items={[...PLAN_BASE.slice(0, 2), { content: "Rewrite the whole client", status: "cancelled", priority: "low" }, ...PLAN_BASE.slice(2)]} />
    </Col>
  );
}

function StreamingMarkdownDemo() {
  const text = MD_MESSAGE;
  const [shown, setShown] = useState(0);
  const [running, setRunning] = useState(false);
  useEffect(() => {
    if (!running) return;
    const id = setInterval(() => {
      setShown((n) => {
        if (n >= text.length) {
          setRunning(false);
          return n;
        }
        return Math.min(text.length, n + 3 + Math.floor(Math.random() * 4));
      });
    }, 40);
    return () => clearInterval(id);
  }, [running, text.length]);
  return (
    <Col>
      <Row>
        <Label>streaming (open fence, open bold, half-written table all render as open)</Label>
        <Button
          size="sm"
          onClick={() => {
            setShown(0);
            setRunning(true);
          }}
        >
          Stream
        </Button>
      </Row>
      <Markdown source={text.slice(0, shown)} streaming={shown < text.length} />
    </Col>
  );
}

// ---------------------------------------------------------------------------

function LiveTranscriptDemo() {
  const [state, setState] = useState<ScenarioState | null>(null);
  const [playing, setPlaying] = useState(false);
  const [rev, setRev] = useState(0);
  const draftRef = useRef<ScenarioDraft>(initialDraft());
  const stepsRef = useRef<ReadonlyArray<ScenarioStep>>([]);
  const idxRef = useRef(0);
  const t0Ref = useRef(0);
  const rafRef = useRef<number | null>(null);
  const labelRef = useRef("");

  const stop = useCallback(() => {
    if (rafRef.current !== null) cancelAnimationFrame(rafRef.current);
    rafRef.current = null;
    setPlaying(false);
  }, []);

  const tick = useCallback(() => {
    const sec = (Date.now() - t0Ref.current) / 1000;
    const before = idxRef.current;
    idxRef.current = runSteps(draftRef.current, stepsRef.current, sec, idxRef.current);
    if (idxRef.current !== before) {
      labelRef.current = stepsRef.current[idxRef.current - 1]?.label ?? labelRef.current;
      setState(snapshot(draftRef.current, labelRef.current, false));
      setRev((r) => r + 1);
    }
    if (idxRef.current >= stepsRef.current.length) {
      stop();
      setState(snapshot(draftRef.current, labelRef.current, draftRef.current.question === null && draftRef.current.status !== "running"));
      return;
    }
    rafRef.current = requestAnimationFrame(tick);
  }, [stop]);

  const play = useCallback(
    (steps: ReadonlyArray<ScenarioStep>) => {
      stop();
      stepsRef.current = steps;
      idxRef.current = 0;
      t0Ref.current = Date.now();
      setPlaying(true);
      rafRef.current = requestAnimationFrame(tick);
    },
    [stop, tick],
  );

  const start = () => {
    draftRef.current = initialDraft();
    labelRef.current = "";
    setState(snapshot(draftRef.current, "", false));
    play(buildScenario(Date.now()));
  };
  const reset = () => {
    stop();
    draftRef.current = initialDraft();
    setState(null);
    setRev(0);
  };
  useEffect(() => stop, [stop]);

  const onSubmit = (sub: ComposerSubmission) => {
    if (sub.mode === "answer") play(buildAnswerSteps(Date.now(), sub.text));
    else if (sub.mode === "steer") {
      // Inject the steer into the running scenario without restarting it.
      const steer = buildSteerSteps(Date.now(), sub.text);
      for (const st of steer) st.apply(draftRef.current);
      setState(snapshot(draftRef.current, "Steered", false));
      setRev((r) => r + 1);
    }
  };

  const s = state;
  const running = s?.status === "running";
  const startedAt = useMemo(() => (s ? s.turns[0]?.startedAt : undefined), [s]);

  return (
    <Col>
      <Row>
        <Button size="sm" variant="primary" onClick={start} disabled={playing}>
          {s ? "Replay" : "Play scenario"}
        </Button>
        <Button size="sm" variant="ghost" onClick={reset} disabled={!s}>
          Reset
        </Button>
        {s ? <Caption>{playing ? `▶ ${s.stepLabel}` : s.question ? "⏸ waiting for your answer" : s.done ? "■ finished" : "⏸ paused"}</Caption> : null}
      </Row>
      <ChatTranscript
        session={
          s
            ? {
                id: "ses_01J9K2",
                role: "orchestrator",
                status: s.status,
                model: "claude-opus-4",
                workItemId: "WI-2481",
                title: "Add retry with backoff to the GitHub webhook handler",
                repo: "dude/dude",
                branch: "wi-2481-webhook-retry",
                startedAt,
                endedAt: s.status === "completed" ? Date.now() : undefined,
                costUsd: s.costUsd,
                budgetUsd: 2.5,
                tokens: s.tokens,
              }
            : { id: "ses_01J9K2", role: "orchestrator", status: "pending", model: "claude-opus-4", workItemId: "WI-2481", title: "Add retry with backoff to the GitHub webhook handler", repo: "dude/dude" }
        }
        headerActions={
          <>
            <IconButton icon="pause" label="Pause" size="sm" disabled={!running} />
            <IconButton icon="stop" label="Abort" size="sm" disabled={!running} />
            <IconButton icon="external" label="Open events" size="sm" />
          </>
        }
        pinned={s && s.plan.length > 0 ? <AgentPlan items={s.plan} meta="orchestrator" defaultCollapsed /> : undefined}
        footer={<ChatComposer question={s?.question ?? undefined} running={running} disabled={!s || s.status === "completed"} disabledReason={s?.status === "completed" ? "This session completed." : "Press Play to start the scenario."} onSubmit={onSubmit} />}
        revision={rev}
        live={playing}
        maxHeight={640}
        emptyMessage="Press Play to watch a run unfold."
      >
        {s?.turns.map((t) => (
          <TurnNode key={t.id} turn={t} depth={0} />
        ))}
      </ChatTranscript>
    </Col>
  );
}

function TurnNode({ turn, depth }: { readonly turn: ScenarioTurn; readonly depth: number }) {
  if (turn.kind === "thread" && turn.sessionId && turn.threadStatus && turn.task && turn.role !== "human" && turn.role !== "system") {
    const lastTurn = turn.turns?.[turn.turns.length - 1];
    return (
      <ChatThread
        sessionId={turn.sessionId}
        role={turn.role}
        status={turn.threadStatus}
        model={turn.model}
        task={turn.task}
        depth={depth + 1}
        startedAt={turn.startedAt}
        endedAt={turn.endedAt}
        costUsd={turn.costUsd}
        turnCount={turn.turns?.length}
        activity={lastTurn?.activity}
        activityProps={lastTurn ? activityPropsFor(lastTurn) : undefined}
        onOpen={() => undefined}
      >
        {turn.turns?.map((t) => (
          <TurnNode key={t.id} turn={t} depth={depth + 1} />
        ))}
      </ChatThread>
    );
  }
  if (turn.kind === "system") return <ChatMessage role="system" content={turn.text} startedAt={turn.startedAt} isNew />;
  if (turn.kind === "human") return <ChatMessage role="human" name={turn.name} intent={turn.intent} inReplyTo={turn.inReplyTo} content={turn.text} startedAt={turn.startedAt} isNew />;
  if (turn.role === "human" || turn.role === "system") return null;
  const streaming = turn.activity === "streaming";
  return (
    <ChatMessage
      role={turn.role}
      model={turn.model}
      content={turn.text.slice(0, turn.shown)}
      streaming={streaming}
      activity={turn.activity}
      activityProps={activityPropsFor(turn)}
      startedAt={turn.startedAt}
      endedAt={turn.endedAt}
      costUsd={turn.costUsd}
      tokens={turn.tokens}
      attachments={
        turn.tools.length > 0
          ? turn.tools.map((tc) => (
              <ToolCallCard key={tc.id} name={tc.name} status={tc.status} args={tc.args} startedAt={tc.startedAt} endedAt={tc.endedAt} result={tc.result} diff={tc.diff} error={tc.error} exitCode={tc.exitCode} slowAfterMs={SLOW_DEMO_MS} />
            ))
          : undefined
      }
    />
  );
}

function activityPropsFor(t: ScenarioTurn) {
  const terminal = t.activity === "completed" || t.activity === "failed" || t.activity === "aborted";
  return {
    since: terminal ? undefined : t.activitySince,
    tool: t.tool,
    attempt: t.attempt,
    maxAttempts: t.attempt !== undefined ? 5 : undefined,
    retryAt: t.retryAt,
    slowAfterMs: SLOW_DEMO_MS,
    detail: t.activity === "retrying" ? "429 from upstream" : undefined,
  };
}
