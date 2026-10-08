import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Block, Caption, Col, Label, Panes, Row, Section, States, type PaneMode } from "../Frame.tsx";
import styles from "../gallery.module.css";
import { ActivityIndicator } from "../../components/ActivityIndicator.tsx";
import { AgentPlan, type PlanItem } from "../../components/AgentPlan.tsx";
import { ChatComposer, type ComposerSubmission } from "../../components/ChatComposer.tsx";
import { ChatAside, ChatMessage } from "../../components/ChatMessage.tsx";
import { ChatThread } from "../../components/ChatThread.tsx";
import { ChatTranscript } from "../../components/ChatTranscript.tsx";
import { Markdown } from "../../components/Markdown.tsx";
import { QuestionCard } from "../../components/QuestionCard.tsx";
import { ThinkingBlock } from "../../components/ThinkingBlock.tsx";
import { ToolCallCard } from "../../components/ToolCallCard.tsx";
import { ChatEvent } from "../../components/ChatEvent.tsx";
import { ChatProgress } from "../../components/ChatProgress.tsx";
import { ChatNotice } from "../../components/ChatNotice.tsx";
import { TaskHistory } from "../../components/TaskHistory.tsx";
import { ChatRunLine } from "../../components/ChatRunLine.tsx";
import { StartChoice } from "../../components/StartChoice.tsx";
import { DeciderLine } from "../../components/DeciderLine.tsx";
import { ImagesBlock } from "./ChatImages.tsx";
import { Button, IconButton } from "../../primitives/Button.tsx";
import { ACTIVITY_KINDS, ACTIVITY_SPECS } from "../../tokens/activity.ts";
import { at } from "../fixtures.tsx";
import {
  ANSWER_TEXT,
  CLIENT_DIFF,
  CLIENT_SOURCE,
  COLOR_OUTPUT_TRUNCATED,
  CONTEXT_WINDOW,
  CONTROL_CODES_OUTPUT,
  GIT_DIFF_COLOR,
  LS_COLOR,
  LONG_TEST_OUTPUT_FAILED,
  LONG_TEST_OUTPUT_PASSED,
  MSG_1,
  MSG_2,
  MSG_3,
  MSG_4,
  PHASE_PROMPT,
  PYTEST_OUTPUT,
  QUESTION_OPTIONS,
  QUESTION_TEXT,
  THOUGHT_1,
  THOUGHT_2,
  THOUGHT_3,
  THOUGHT_STREAMING,
  TSC_STDERR,
  TSC_STDOUT,
} from "../realisticTranscript.ts";
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
        id="ch-thinking"
        title="ThinkingBlock"
        note="The model's reasoning, which arrives between its messages and its tool calls. Secondary by construction: no avatar, no frame, muted ink, a 24px row — quieter than a message, and distinct from a tool call, which has a surface and a border. Collapsed it is brain · label · a one-line preview · how long it took. While streaming the brain sits inside the thinking rhythm's drifting ring, the preview follows the latest line and the duration ticks. Twelve in a row read as a faint ledger, not a wall."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>collapsed (default) · streaming · expanded · plain text</Label>
            <ThinkingBlock text={THOUGHT_1} durationMs={4_200} />
            <ThinkingBlock text={THOUGHT_STREAMING} streaming startedAt={Date.now() - 6_000} />
            <ThinkingBlock text={THOUGHT_2} durationMs={11_400} defaultExpanded />
            <ThinkingBlock text={"Plain text, no Markdown: the * and ` are shown as typed.\n\n  indentation is kept too."} plain durationMs={800} />
            <ThinkingBlock text="" streaming startedAt={Date.now() - 1_000} />
            <Label>between a message and its tool calls — the transcript rhythm</Label>
            <ChatMessage role="implementer" model="claude-opus-4" content={MSG_1} startedAt={at(60_000)} endedAt={at(64_000)} contextTokens={15_200} contextWindowTokens={CONTEXT_WINDOW} outputTokens={240} costUsd={0.012} />
            <div style={{ padding: "0 12px 0 40px", display: "flex", flexDirection: "column", gap: 4 }}>
              <ThinkingBlock text={THOUGHT_1} durationMs={4_200} />
              <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} durationMs={41} output={CLIENT_SOURCE} />
              <ThinkingBlock text="Single fetch, plain Error. I need a typed check on res.status before the throw." durationMs={1_900} />
              <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/util/wait.ts" }} durationMs={22} output={"export const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));"} />
            </div>
            <Label>many in a row (grayscale check — still a calm ledger)</Label>
            <div style={{ filter: "grayscale(1)", display: "flex", flexDirection: "column", gap: 2 }}>
              {[
                ["Looking at how the route dedupes before deciding whether retries are safe.", 2_100],
                ["The delivery id is stored before the handler runs, so a retried POST is a no-op.", 3_300],
                ["I'll keep the sleep helper that takes a signal.", 900],
                ["Test fixture returns 502 forever; the give-up assertion needs 5 calls.", 4_700],
                ["Diff is 118 lines; under the cap.", 600],
                ["The reviewer prompt does not ask for a changelog. Skipping it.", 1_200],
              ].map(([t, ms], i) => (
                <ThinkingBlock key={i} text={String(t)} durationMs={Number(ms)} />
              ))}
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-tool"
        title="ToolCallCard"
        note="One 28px row per call: glyph · name · what it was called with · duration · outcome. Running calls sweep along the bottom edge with a ticking duration that turns attention-toned once slow. Failed calls open by default and put the error's first line in the row itself; a non-zero exit code is a danger chip in the row whatever the status said. Open, the output is a mono block headed by its exit code. The backend caps each stream at 4 KB and keeps the first and last 2 KB of anything longer — the dropped middle is a labelled dashed line, never a silent join. stderr, when the harness reports it apart, is a second block with its own rail; OpenCode merges the two, which is one block. A diff hands off to DiffView."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>finished, with output</Label>
            <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} durationMs={41} output={CLIENT_SOURCE} />
            <ToolCallCard name="grep" status="completed" args={{ pattern: "webhook", path: "apps/control-plane/src", include: "*.ts" }} durationMs={182} output={"apps/control-plane/src/api/routes/webhooks.ts:12\napps/control-plane/src/api/routes/webhooks.ts:48\napps/control-plane/src/integrations/github/client.ts:7"} exitCode={0} />
            <ToolCallCard name="bash" status="completed" args={{ command: "git status --short" }} durationMs={30} output="" exitCode={0} />
            <Label>running: sweep, ticking clock, output so far; slow after 20s</Label>
            <ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 1_200} />
            <ToolCallCard name="bash" status="running" args={{ command: "bun install && bun run build" }} startedAt={Date.now() - 47_000} output={"$ bun install\nbun install v1.2.4\n + @dude/domain@workspace:*\n 412 packages installed [1.20s]\n$ bun run build\n$ vite build\nvite v6.3.0 building for production..."} defaultExpanded />
            <Label>non-zero exit: the chip is in the row, the card opens itself, the rail goes danger</Label>
            <ToolCallCard name="bash" status="failed" args={{ command: "bun test apps/control-plane" }} durationMs={4_210} exitCode={1} error="1 failing: retries when response is 502" output={LONG_TEST_OUTPUT_FAILED} />
            <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck" }} durationMs={2_900} exitCode={2} output={TSC_STDOUT} stderr={TSC_STDERR} defaultExpanded={false} />
            <Label>head and tail: 4 KB kept, the middle omitted</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "bun test apps/control-plane" }} durationMs={3_880} exitCode={0} output={LONG_TEST_OUTPUT_PASSED} defaultExpanded />
            <Label>stdout and stderr apart (Claude Code) vs merged (OpenCode)</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck" }} durationMs={2_900} exitCode={2} output={TSC_STDOUT} stderr={TSC_STDERR} defaultExpanded />
            <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck" }} durationMs={2_900} exitCode={2} output={`${TSC_STDOUT}\n${TSC_STDERR}`} defaultExpanded />
            <ToolCallCard name="bash" status="completed" args={{ command: "cargo build 2>&1 | tail" }} durationMs={12_000} exitCode={0} stderr={{ head: "   Compiling dude-orchestrator v0.1.0\nwarning: unused variable: `attempt`\n  --> src/lux.rs:41:9\n", tail: "warning: `dude-orchestrator` (bin) generated 3 warnings\n    Finished `dev` profile [unoptimized + debuginfo] target(s) in 11.82s", omittedBytes: 3_072 }} defaultExpanded />
            <Label>the rest</Label>
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
        id="ch-tool-ansi"
        title="ToolCallCard: coloured output"
        note="The container forces colour (FORCE_COLOR, CLICOLOR_FORCE, TERM=xterm-256color, git color.ui=always), so output arrives with the tool's own escape codes. SGR is rendered — bold, dim, italic, underline, inverse, the 16 colours mapped onto --ds-ansi-* so they read on both fields, 256-colour and truecolor with their lightness clamped into the theme's text band — and everything else (cursor moves, erase-line, hidden cursor, OSC hyperlinks) is stripped rather than shown. Inside the block the tool's colours are the content: a red FAILED is pytest's, not a status of ours. The 2 KB cap can cut a sequence in half at the end of the head or the start of the tail; the fragment is dropped, never shown, and the tail starts plain — a style does not carry across the elision."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>pytest: pass/fail colours, bold summary</Label>
            <ToolCallCard name="bash" status="failed" args={{ command: "pytest -q --color=yes tests/" }} durationMs={1_410} exitCode={1} error="1 failed, 5 passed, 1 skipped" output={PYTEST_OUTPUT} />
            <Label>git diff --color: an OSC hyperlink on the hunk header is stripped, its text kept</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "git diff --color HEAD~1 -- apps/control-plane" }} durationMs={38} exitCode={0} output={GIT_DIFF_COLOR} defaultExpanded />
            <Label>ls --color: bold blue directories, green executables, a dim link target</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "ls -la --color=always" }} durationMs={12} exitCode={0} output={LS_COLOR} defaultExpanded />
            <Label>head and tail, each cut inside a sequence: no fragment, no leaked style</Label>
            <ToolCallCard name="bash" status="failed" args={{ command: "bun test apps/control-plane" }} durationMs={4_210} exitCode={1} error="1 failing: retries when response is 502" output={COLOR_OUTPUT_TRUNCATED} />
            <Label>progress bars collapse to their last frame; cursor control stripped; 256-colour, truecolor and the attributes</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "bun install && cargo build" }} durationMs={13_200} exitCode={0} output={CONTROL_CODES_OUTPUT} defaultExpanded />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-event"
        title="ChatEvent / ChatProgress"
        note="Two quiet rows for what an agent records with `dude event`. ChatEvent is a 24px frameless line — zap glyph, the type in mono, one line of the data (a scalar as-is; an object as up to three key=value pairs then …), the recording role's avatar and the time — expanding to the data pretty-printed. ChatProgress is the same line with a 2px bar under it: determinate with 'n of m' when both are known, a sweep otherwise; the step text follows. While running the fill breathes and the sweep moves; `ended` freezes it where it got to, with a check when complete and a stop mark when not. Reduced motion stills both."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>ChatEvent: scalar · object · long object (expand) · array · no data · expanded</Label>
            <ChatEvent type="coverage" data={87.4} at={at(60_000)} role="implementer" />
            <ChatEvent type="tests.finished" data={{ passed: 42, failed: 1, skipped: 3 }} at={at(64_000)} role="implementer" />
            <ChatEvent type="deploy.started" data={{ env: "staging", sha: "9f2c1e4", region: "eu-west-1", by: "implementer", dryRun: false, services: ["api", "web"] }} at={at(70_000)} role="implementer" />
            <ChatEvent type="files.touched" data={["apps/web/src/auth/session.ts", "apps/web/src/auth/pkce.ts"]} at={at(72_000)} role="simplifier" />
            <ChatEvent type="checkpoint" data={null} at={at(75_000)} role="reviewer" />
            <ChatEvent type="tests.finished" data={{ passed: 42, failed: 1, failures: [{ name: "refresh rotates token", file: "session.test.ts", line: 88 }] }} at={at(80_000)} role="reviewer" defaultExpanded />
            <Label>ChatProgress: determinate running · indeterminate running · done only · ended complete · ended short · ended indeterminate</Label>
            <ChatProgress done={3} of={10} step="Running integration tests" at={at(90_000)} startedAt={Date.now() - 42_000} role="implementer" />
            <ChatProgress done={null} of={null} step="Installing dependencies" at={at(91_000)} startedAt={Date.now() - 8_000} role="implementer" />
            <ChatProgress done={128} of={null} step="Files scanned" at={at(92_000)} startedAt={Date.now() - 12_000} role="investigator" />
            <ChatProgress done={10} of={10} step="Running integration tests" at={at(150_000)} startedAt={at(90_000)} role="implementer" ended />
            <ChatProgress done={6} of={10} step="Running integration tests" at={at(130_000)} startedAt={at(90_000)} role="implementer" ended />
            <ChatProgress done={null} of={null} step="Installing dependencies" at={at(100_000)} startedAt={at(91_000)} role="implementer" ended />
            <ChatProgress done={0} of={4} step={null} at={at(100_000)} startedAt={Date.now() - 3_000} role="qa_browser" />
            <Label>in the transcript rhythm — between a tool call and a thought</Label>
            <ToolCallCard name="bash" status="completed" args={{ command: "bun test" }} startedAt={at(0)} endedAt={at(8_100)} output="42 pass, 1 fail" />
            <ChatEvent type="tests.finished" data={{ passed: 42, failed: 1 }} at={at(8_200)} role="implementer" />
            <ChatProgress done={2} of={3} step="Fixing the failing test" at={at(9_000)} startedAt={at(8_500)} role="implementer" />
            <ThinkingBlock text="The refresh test expects a rotated token; the fixture still returns the old one." durationMs={2_100} />
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
        note="A turn is a gutter and a column, not a bubble, so text aligns down the page. Agent turns carry model, elapsed, the context size at that point ('ctx 15.2k / 744k', coloured at 80% and 100% of the window), the output tokens ('out 1.2k'), cost and the live activity in the foot; a cost the harness does not report is '—', never $0.00. Turns addressed to the agent get a hairline frame tinted by intent — an answer is attention-toned (it closes the needs-you state), a steer is accent-toned (the operator reaching in) — so interventions are scannable in a long transcript. The task prompt keeps the neutral frame: when dude authored it, the system avatar and the Task tag say so, and a long phase prompt clamps at eight lines with Show all. A steer the agent has not read yet is queued: dashed bar, a Queued mark with a clock, and a line saying where it lands; once read, the bar is solid and the header says when it was sent and read (Steer delivery, below, has every state)."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>the factory's phase prompt — system-authored, clamped, Show all</Label>
            <ChatMessage role="system" intent="prompt" name="dude" content={PHASE_PROMPT} startedAt={at(0)} />
            <Label>a person's prompt — short, no clamp needed</Label>
            <ChatMessage role="human" name="marcio" intent="prompt" content="Add retry with backoff to the GitHub webhook handler. Cap at 5 attempts, keep the public API unchanged." startedAt={at(0)} />
            <ChatMessage role="system" content="Session started on worker-03 · claude-opus-4" startedAt={at(1_000)} />
            <Label>agent feet: context and output tokens; cost known, unknown, and zero</Label>
            <ChatMessage role="conductor" model="claude-opus-4" content={"I'll map the handler first, then delegate.\n\n**Plan**\n1. Investigate `GithubClient.post`\n2. Add bounded backoff\n3. Tests, then review"} startedAt={at(1_830)} endedAt={at(19_000)} costUsd={0.06} contextTokens={15_200} contextWindowTokens={CONTEXT_WINDOW} outputTokens={1_240} activity="completed" />
            <ChatMessage role="implementer" model="gpt-5-codex" content="Cost not reported by this harness (subscription seat): the foot says so rather than pricing it at zero." startedAt={at(20_000)} endedAt={at(24_000)} costUsd={null} contextTokens={188_400} contextWindowTokens={CONTEXT_WINDOW} outputTokens={620} activity="completed" />
            <ChatMessage role="reviewer" model="claude-sonnet-4" content="Context past 80% of the window: the ctx count takes attention ink, the same threshold a cost takes against its budget." startedAt={at(25_000)} endedAt={at(26_000)} costUsd={0} contextTokens={612_000} contextWindowTokens={CONTEXT_WINDOW} outputTokens={90} activity="completed" />
            <ChatMessage role="simplifier" model="claude-sonnet-4" content="Only a total is known: the plain `tok` count, as before." startedAt={at(27_000)} endedAt={at(28_000)} tokens={4_100} activity="completed" />
            <Label>live</Label>
            <ChatMessage role="implementer" model="claude-opus-4" content="Added `isRetryable` and a bounded loop. Running the tests" streaming startedAt={Date.now() - 38_000} costUsd={0.21} contextTokens={48_000} contextWindowTokens={CONTEXT_WINDOW} activity="streaming" activityProps={{ since: Date.now() - 6_000, detail: "312 tokens" }} />
            <ChatMessage role="implementer" model="claude-opus-4" content="Tests are running." continued attachments={<ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 44_000} />} activity="tool" activityProps={{ tool: "bash", since: Date.now() - 44_000 }} startedAt={Date.now() - 61_000} costUsd={0.23} />
            <ChatMessage role="conductor" model="claude-opus-4" content="Before I open the PR I need a decision from you." startedAt={Date.now() - 125_000} costUsd={0.002} activity="awaiting_input" activityProps={{ since: Date.now() - 125_000, detail: "Should 4xx be retried?" }} />
            <Label>interventions: answer · steer queued (lands at the next step) · steer read</Label>
            <ChatMessage role="human" name="marcio" intent="answer" inReplyTo="Should 4xx responses be retried? The existing code retries everything." content="No — only retry 5xx and network errors." startedAt={at(1_520_000)} />
            <ChatMessage role="human" name="marcio" intent="steer" content="Do not change the public API of GithubClient. Add the retry inside `post` only." startedAt={at(1_530_000)} deliveredAt={null} />
            <ChatMessage role="human" name="marcio" intent="steer" content="Do not change the public API of GithubClient. Add the retry inside `post` only." startedAt={at(1_530_000)} deliveredAt={at(1_571_000)} read />
            <ChatMessage role="reviewer" model="claude-opus-4" content="Backoff jitter uses `Math.random`; consider seeding for tests (minor)." startedAt={at(1_640_000)} endedAt={at(1_650_000)} costUsd={0.03} activity="failed" activityProps={{ detail: "upstream 500 after 5 attempts" }} />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-conductor"
        title="A task's Chat"
        note="Talking with a task's conductor. TaskHistory heads it: how the task went, what ran (a fan-out folded, ×3), what it came to — the whole line before anyone has written. The first message is the person's own turn (intent message: signed, no tint — talking, not intervening); dude's briefing follows, a framed prompt tagged Briefing and signed with the task's dude name. The conductor answers with its own face (round, violet). dude's notices in Chat say who says them. The composer is chat: Send, accent focus, no interrupt, and 'To Conductor · read-only' where 'Sent as' would be."
      >
        <Panes mode={mode} surface>
          <Col>
            <TaskHistory lead="Delivered automatically, merged" steps={["implementer", "reviewers ×3", "fixer", "reviewers ×3", "simplifier", "PR #88"]}
              facts={["5 findings, all settled", "$9.80"]} />
            <ChatMessage role="human" name="Márcio" intent="message" content="why is the max backoff 8s and not something like 30? and does this retry POSTs?" startedAt={at(0)} />
            <ChatMessage role="system" name="El Duderino" intent="briefing" startedAt={at(1_000)}
              content={"Conductor, Márcio wrote in the Chat of SDK-31, \"Retry on 429 with backoff\". You are this task's conductor: answer them.\n\n## The task\n\nSDK-31 · status done\n\n## Runs (8)\n\n- run_… · implement · implementer · completed · Implemented it.\n- run_… · review · correctness · reviewer · completed · One problem."} />
            <ChatAside><ToolCallCard name="bash" status="completed" args={{ command: "rg -n \"maxBackoff|idempot\" src/" }} durationMs={200} /></ChatAside>
            <ChatMessage role="conductor" model="claude-sonnet-4.5" startedAt={at(9_000)} endedAt={at(16_000)} costUsd={0.02}
              content={"**8s**: Tiago asked for it on the PR — *\"browser tab will look hung past ~10s, cap it\"*. The first implementer had 30s; the PR-fix Run changed it to 8s.\n\n**POSTs**: only when the request carries an `Idempotency-Key` (`src/http/retry.ts:41`)."} />
            <ChatNotice kind="parked" by="El Duderino" text="Parked while nobody is writing — nothing is held; your next message resumes it." at={at(320_000)} />
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: "var(--ds-radius-control)", overflow: "hidden" }}>
              <ChatComposer mode="chat" sentAs="Márcio" to={<>To <b>Conductor</b> · read-only</>} onSubmit={() => undefined} />
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-conducted"
        title="A conducted task"
        note="The conductor takes the decisions. StartChoice offers the two ways to start, side by side and equal: no primary, nothing preselected. In Chat, each Run the conductor started is a ChatRunLine — one collapsed line on a rail in its role's colour, with its status and what it came to, opening its session. A decision the delivery waits on is El Duderino's notice (kind decision); an approval or a green check is a notice (kind notice) that wakes nobody. DeciderLine sits above the composer: who decides, what it waits on, and Let Deliver finish it."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>not started: how to start</Label>
            <StartChoice options={[
              { id: "talk", icon: "message", title: "Talk it through",
                description: "The conductor reads the task and the code, asks what it needs, proposes a plan, and starts the agents when you agree.",
                points: ["Nothing is built until you agree", "It asks before the pull request"],
                foot: "Conductor · Thinker · Small", action: <Button>Talk it through</Button> },
              { id: "deliver", icon: "zap", title: "Deliver",
                description: "The automatic pipeline: implementer, reviewers, fixer, simplifier, then the pull request. Nobody needs to be here.",
                points: ["Stops only on a question or an escalation", "You can open Chat later and take over"],
                foot: "The project's pipeline", action: <Button>Deliver</Button> },
            ]} />
            <Label>in Chat</Label>
            <ChatMessage role="conductor" model="claude-opus-5-5" startedAt={at(0)} endedAt={at(6_000)}
              content={"Agreed: retry only 5xx and network errors, cap at 8s. I wrote that into the task's criteria and started the implementer."} />
            <ChatRunLine role="implementer" status="completed" what="implement" facts={["9m", "$1.12"]} onOpen={() => undefined} />
            <ChatNotice kind="decision" by="El Duderino" text="Waiting on the conductor: what to do with the review's findings." at={at(700_000)} />
            <ChatRunLine role="reviewer" status="completed" what="correctness · 2 findings" facts={["4m", "$0.88"]} onOpen={() => undefined} />
            <ChatRunLine role="reviewer" status="running" what="security" facts={["2m"]} onOpen={() => undefined} />
            <Label>a Run the conductor steered: its steers under its line, in each delivery state</Label>
            <ChatRunLine role="implementer" status="running" what="implement" facts={["6m"]} onOpen={() => undefined} steers={<>
              <ChatMessage role="conductor" name="Conductor" intent="steer" content="Use the staging database for this one." startedAt={at(600_000)}
                deliveredAt={at(612_000)} read readAfter="Bash" />
              <ChatMessage role="conductor" name="Conductor" intent="steer" content="Keep the invoice option hidden on monthly plans." startedAt={at(640_000)}
                deliveredAt={null} pendingReason="Lands at the agent's next step." />
              <ChatMessage role="conductor" name="Conductor" intent="steer" content="Also cover the empty cart." startedAt={at(650_000)}
                deliveredAt={null} pendingReason="Lands when the run resumes." />
              <ChatMessage role="conductor" name="Conductor" intent="steer" content="Stop and run the migration first." startedAt={at(660_000)}
                deliveredAt={null} failed="the run finished before the agent read it" />
            </>} />
            <ChatNotice kind="notice" by="El Duderino" text="Pull request #88: approved by Tiago, checks passing." at={at(900_000)} />
            <Label>a Run restarted in its place: the conductor's restart_run, or a person's Restart on the task's banner</Label>
            <ChatNotice kind="restart" by="Conductor" text="The conductor restarted the frontend Run, starting over: Read the worker yourself; do not use the task tool." at={at(950_000)} />
            <ChatNotice kind="restart" by="El Duderino" text="A person restarted the implement Run on another tier, starting over." at={at(960_000)} />
            <ChatNotice kind="stopped" by="El Duderino" text="Stopped Brainstorm's turn: bash was open for 10 min." at={at(970_000)} />
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: "var(--ds-radius-control)", overflow: "hidden" }}>
              <DeciderLine decider="conductor" waiting="whether to open the pull request" action={<Button variant="quiet">Let Deliver finish it</Button>} />
              <ChatComposer mode="chat" sentAs="Márcio" to={<>To <b>Conductor</b></>} onSubmit={() => undefined} />
            </div>
            <DeciderLine decider="policy" />
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-steer"
        title="Steer delivery"
        note="A steer lands at the agent's next step: the harness takes it while a tool runs, and the model reads it before its next call, in the same turn, the tool never cancelled. Until it is read it keeps the queued treatment — dashed bar, Queued mark, and one line saying where it lands (pendingReason): after the named running tool, at the next step, when the turn ends (a harness that reads only between turns), when the run resumes, when the agent starts. Interrupt now is always a named choice on the line, never a fallback. Read, it loses the queued treatment and sits where the agent read it — between the tool it waited for and the next one — with 'sent · read …, after Bash' in its header. An older lux reports only the handoff: 'delivered', no read time. Failed is a danger line with Retry."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>(a) queued while a tool runs — lands after it, in this turn</Label>
            <ChatAside><ToolCallCard name="bash" status="running" args={{ command: "bun test src/integrations/github" }} startedAt={Date.now() - 14_000} /></ChatAside>
            <ChatMessage role="human" name="marcio" intent="steer" content="Also check the migration renames the column, not just the index." startedAt={Date.now() - 6_000} deliveredAt={null}
              pendingReason={<>Lands after <b>Bash</b> finishes.</>} onInterrupt={() => undefined} />
            <Label>queued with nothing running — the agent is thinking or writing</Label>
            <ChatMessage role="human" name="marcio" intent="steer" content="Keep the jitter injectable." startedAt={Date.now() - 3_000} deliveredAt={null}
              pendingReason="Lands at the agent's next step." onInterrupt={() => undefined} />
            <Label>(b) taken — read after Bash, placed where it was read, between that call and the next</Label>
            <ChatAside><ToolCallCard name="bash" status="completed" args={{ command: "bun test src/integrations/github" }} startedAt={at(1_800_000)} endedAt={at(1_836_000)} /></ChatAside>
            <ChatMessage role="human" name="marcio" intent="steer" content="Also check the migration renames the column, not just the index." startedAt={at(1_800_000 + 5_000)} deliveredAt={at(1_836_300)} read readAfter="Bash" />
            <ChatAside><ToolCallCard name="read" status="completed" args={{ path: "migrations/060_rename.sql" }} startedAt={at(1_838_000)} endedAt={at(1_838_100)} /></ChatAside>
            <Label>(c) a harness that reads only between turns</Label>
            <ChatMessage role="human" name="marcio" intent="steer" content="Also check the migration renames the column, not just the index." startedAt={Date.now() - 6_000} deliveredAt={null}
              pendingReason="This agent reads messages only between turns — lands when this turn ends." onInterrupt={() => undefined} />
            <Label>paused · starting</Label>
            <ChatMessage role="human" name="marcio" intent="steer" content="Use the staging database for this one." startedAt={Date.now() - 60_000} deliveredAt={null} pendingReason="Lands when the run resumes." />
            <ChatMessage role="human" name="marcio" intent="steer" content="Start from the failing test." startedAt={Date.now() - 4_000} deliveredAt={null} pendingReason="Lands when the agent starts." />
            <Label>failed — danger line, Retry</Label>
            <ChatMessage role="human" name="marcio" intent="steer" content="Also check the migration renames the column." startedAt={at(1_900_000)} deliveredAt={null}
              failed="the run finished before the agent read it" onRetry={() => undefined} />
            <Label>delivered by an older lux — handed over, no read time</Label>
            <ChatMessage role="human" name="marcio" intent="steer" content="Also check the migration renames the column." startedAt={at(1_900_000)} deliveredAt={at(1_940_000)} />
            <Label>composer hint, following the same capability</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: "var(--ds-radius-control)", overflow: "hidden" }}>
              <ChatComposer running canInterrupt landsHint="Lands after the current tool" onSubmit={() => undefined} />
            </div>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: "var(--ds-radius-control)", overflow: "hidden" }}>
              <ChatComposer running canInterrupt landsHint="Lands at the agent's next step" onSubmit={() => undefined} />
            </div>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: "var(--ds-radius-control)", overflow: "hidden" }}>
              <ChatComposer running canInterrupt landsHint="Lands when the turn ends" onSubmit={() => undefined} />
            </div>
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
        id="ch-question"
        title="QuestionCard"
        note="An agent stops and asks a person (ask_user). While it waits this is the one loud turn in a transcript, and it is loud once: the attention wash and 2px bar. Inside it everything is neutral — the transcript header's Needs-you badge already names the state, the wait clock is muted, and the offered choices are shown once, as one-click chips in the composer (in the card only with onChoose). In grayscale it is still the only barred, tinted turn. Answered, it settles: no wash, a quiet Answered mark with how long it waited, and the choices listed as the record of what was offered. The answer follows as its own turn — the card never repeats it. A question whose run ended is No longer needed and never rings: an answer would reach nobody."
      >
        <Panes mode={mode} surface>
          <Col>
            <Label>waiting — the run is blocked; the clock ticks; choices are in the composer</Label>
            <QuestionCard role="implementer" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={Date.now() - 4 * 60_000 - 12_000} />
            <Label>waiting, no choices, a named session — free-text answer only</Label>
            <QuestionCard role="reviewer" name="reviewer-2" text="The PR body says the route's retry is a product decision. Is there a task for it, or should I file one?" askedAt={Date.now() - 38_000} />
            <Label>waiting with onChoose — the chips become one-click replies</Label>
            <QuestionCard role="conductor" text="Should 4xx responses be retried? The existing code retries everything, but 4xx usually means our request is wrong." options={["Retry 5xx and network only", "Retry everything (current behaviour)"]} askedAt={Date.now() - 125_000} onChoose={() => undefined} />
            <Label>waiting on someone else — the note says how to make it yours; the choices are muted, and hovering them says it again</Label>
            <QuestionCard role="implementer" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={Date.now() - 90_000} waitingOn="Ana" />
            <Label>answered — calm; the answer is the next turn, not quoted here</Label>
            <QuestionCard role="implementer" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={at(45_600)} answeredAt={at(45_600 + 4 * 60_000 + 12_000)} />
            <ChatMessage role="human" name="marcio" intent="answer" inReplyTo="Should I leave the route's retry in place, or fold it into this change?" content={ANSWER_TEXT} startedAt={at(45_600 + 4 * 60_000 + 12_000)} deliveredAt={at(45_600 + 4 * 60_000 + 13_000)} />
            <Label>waiting past the grace period — parked: its container stopped, the answer resumes it</Label>
            <QuestionCard role="implementer" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={Date.now() - 14 * 60 * 60_000} />
            <ChatNotice kind="parked" text="Parked while it waits for you — nothing is held; answering resumes it." at={Date.now() - 14 * 60 * 60_000 + 10 * 60_000} />
            <Label>no longer needed — its run ended first; settled, never rings</Label>
            <QuestionCard role="qa_browser" text="The save button has no stable selector. Should I add a `data-testid`, or is that out of scope?" options={["Add data-testid", "Out of scope — skip the check"]} askedAt={at(100_000)} dismissed />
            <Label>settled elsewhere — the conductor's question about an escalation, decided on the task's banner instead</Label>
            <QuestionCard role="conductor" text="The review is stuck on one finding. I propose one more narrow fix round." options={["Retry as proposed", "Accept as it is", "Stop"]} askedAt={at(110_000)} settledBy="Decided on the banner" />
            <Label>grayscale check — waiting still separates from answered by wash, bar and clock</Label>
            <div style={{ filter: "grayscale(1)", display: "flex", flexDirection: "column" }}>
              <QuestionCard role="implementer" text="Leave the route's retry in place?" options={["Yes", "No"]} askedAt={Date.now() - 90_000} />
              <QuestionCard role="implementer" text="Leave the route's retry in place?" options={["Yes", "No"]} askedAt={at(0)} answeredAt={at(90_000)} />
            </div>
          </Col>
        </Panes>
      </Block>

      <Block
        id="ch-composer"
        title="ChatComposer"
        note="The two ways a human intervenes are distinct on four channels: frame tint, hint text, button label and button icon. Answer is attention-toned with the question quoted above and one-click options; Enter submits because the agent is waiting. Steer is accent-toned and says where it lands; Enter sends it, because it waits for the agent's next step rather than stopping anything. Interrupt now, which stops the turn, is a deliberate tick. The text is cleared only once onSubmit confirms it: one that resolves false or rejects keeps the words."
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
            <Label>a send that fails — the words stay to send again</Label>
            <div style={{ border: "1px solid var(--ds-color-border-subtle)", borderRadius: 6, overflow: "hidden" }}>
              <ChatComposer mode="chat" to={<>To <b>Conductor</b> · read-only</>} defaultValue="and does it retry POSTs?"
                onSubmit={() => new Promise<boolean>((done) => setTimeout(() => done(false), 600))} />
            </div>
          </Col>
        </Panes>
      </Block>

      <ImagesBlock mode={mode} />

      <Block
        id="ch-realistic"
        title="A real session, after the fact"
        note="One implementer session as the real runs look: the factory's phase prompt at the top (system-authored, clamped), the model's reasoning between every move, tool calls whose output the backend capped at 4 KB with the middle dropped, a failing test run with exit 1 that opened itself, a queued steer that was delivered on the next turn, a question the implementer stopped on for four minutes until marcio answered, and a context/output foot on every message. Cost is unknown for this harness and reads as such in the header and the feet, while the tokens still add up. Static: this is about density and hierarchy; the live scenario below is about motion."
      >
        <Panes mode={mode}>
          <RealisticTranscript />
        </Panes>
      </Block>

      <Block
        id="ch-transcript"
        title="ChatTranscript — live scenario"
        note="The whole thing, played back: a task arrives, the orchestrator thinks (a ThinkingBlock streams above its first message), writes a plan, delegates to an investigator (nested), delegates to an implementer whose bash call runs long and then fails with a capped output and exit 1, hits a 429 and backs off twice, recovers, then asks you a question. Answer it in the composer to let it finish; steer it while it runs — the steer shows as queued until the agent's next step reads it, then moves to where it was read. Scroll up mid-stream: the view stops following and offers 'Jump to latest' with a count. The tool 'slow' threshold is 6s here (20s in the product) so the state is reachable."
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

/** Aligns a thought or a tool card with the message column when it is a turn of its own. */
function Aside({ children }: { readonly children: ReactNode }) {
  return <div style={{ padding: "1px 12px 1px 40px", display: "flex", flexDirection: "column", gap: 4 }}>{children}</div>;
}

const MIN = 60_000;

function RealisticTranscript() {
  return (
    <ChatTranscript
      session={{
        id: "ses_01J9K4",
        role: "implementer",
        status: "completed",
        model: "claude-opus-4",
        taskKey: "WI-2481",
        title: "Add retry with backoff to the GitHub webhook handler",
        repo: "dude/dude",
        branch: "wi-2481-webhook-retry",
        startedAt: at(0),
        endedAt: at(9 * MIN),
        costUsd: null,
        tokens: 1_240_000,
      }}
      maxHeight={720}
    >
      <ChatMessage role="system" intent="prompt" name="dude" content={PHASE_PROMPT} startedAt={at(0)} />
      <ChatMessage role="system" content="Session started on worker-03 · claude-opus-4" startedAt={at(1_000)} />
      <Aside>
        <ThinkingBlock text={THOUGHT_1} startedAt={at(1_200)} endedAt={at(5_400)} />
      </Aside>
      <ChatMessage role="implementer" model="claude-opus-4" content={MSG_1} startedAt={at(5_400)} endedAt={at(7_000)} costUsd={null} contextTokens={18_300} contextWindowTokens={CONTEXT_WINDOW} outputTokens={60} />
      <Aside>
        <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={at(7_100)} endedAt={at(7_141)} output={CLIENT_SOURCE} />
        <ToolCallCard name="read" status="completed" args={{ file_path: "apps/control-plane/src/util/wait.ts" }} startedAt={at(7_300)} endedAt={at(7_322)} output={"export const wait = (ms: number) => new Promise((r) => setTimeout(r, ms));"} />
        <ThinkingBlock text="Single fetch, plain Error. I need a typed check on res.status before the throw, and the sleep in integrations/github already takes a signal — that is the one to keep." startedAt={at(7_400)} endedAt={at(10_100)} />
        <ToolCallCard name="edit" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={at(10_200)} endedAt={at(10_212)} diff={CLIENT_DIFF} />
        <ToolCallCard name="bash" status="completed" args={{ command: "git rm -q apps/control-plane/src/util/wait.ts" }} startedAt={at(10_300)} endedAt={at(10_330)} exitCode={0} output="" />
      </Aside>
      <ChatMessage role="implementer" model="claude-opus-4" content={MSG_2} startedAt={at(10_400)} endedAt={at(14_000)} costUsd={null} contextTokens={31_200} contextWindowTokens={CONTEXT_WINDOW} outputTokens={410} />
      <Aside>
        <ToolCallCard name="bash" status="failed" args={{ command: "bun test apps/control-plane" }} startedAt={at(14_100)} endedAt={at(18_310)} exitCode={1} error="1 failing: retries when response is 502" output={LONG_TEST_OUTPUT_FAILED} />
        <ThinkingBlock text={THOUGHT_2} startedAt={at(18_400)} endedAt={at(29_800)} />
      </Aside>
      <ChatMessage role="human" name="marcio" intent="steer" content="Keep the jitter, but make it injectable so the tests can seed it." startedAt={at(20_000)} deliveredAt={at(29_800)} read />
      <ChatMessage role="implementer" model="claude-opus-4" content={MSG_3} startedAt={at(29_800)} endedAt={at(33_000)} costUsd={null} contextTokens={52_800} contextWindowTokens={CONTEXT_WINDOW} outputTokens={380} />
      <Aside>
        <ToolCallCard name="edit" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={at(33_100)} endedAt={at(33_109)} />
        <ToolCallCard name="edit" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.test.ts" }} startedAt={at(33_200)} endedAt={at(33_211)} />
        <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck" }} startedAt={at(33_300)} endedAt={at(36_200)} exitCode={2} output={TSC_STDOUT} stderr={TSC_STDERR} />
        <ThinkingBlock text="Two type errors from the catch scope: `res` is unknown outside the try. Narrow it before the check." startedAt={at(36_300)} endedAt={at(38_000)} />
        <ToolCallCard name="edit" status="completed" args={{ file_path: "apps/control-plane/src/integrations/github/client.ts" }} startedAt={at(38_100)} endedAt={at(38_108)} />
        <ToolCallCard name="bash" status="completed" args={{ command: "bun run typecheck && bun test apps/control-plane" }} startedAt={at(38_200)} endedAt={at(42_080)} exitCode={0} output={LONG_TEST_OUTPUT_PASSED} />
        <ThinkingBlock text={THOUGHT_3} startedAt={at(42_100)} endedAt={at(45_600)} />
      </Aside>
      <ChatMessage role="implementer" model="claude-opus-4" content="All green: 65 pass, 0 fail. The diff is 118 lines. One thing I need a decision on before the PR." startedAt={at(45_600)} endedAt={at(47_000)} costUsd={null} contextTokens={61_400} contextWindowTokens={CONTEXT_WINDOW} outputTokens={40} />
      <QuestionCard role="implementer" text={QUESTION_TEXT} options={QUESTION_OPTIONS} askedAt={at(47_000)} answeredAt={at(47_000 + 4 * MIN + 12_000)} />
      <ChatMessage role="human" name="marcio" intent="answer" inReplyTo="Should I leave the route's retry in place, or fold it into this change?" content={ANSWER_TEXT} startedAt={at(47_000 + 4 * MIN + 12_000)} deliveredAt={at(47_000 + 4 * MIN + 13_000)} />
      <ChatMessage role="implementer" model="claude-opus-4" content={MSG_4} startedAt={at(47_000 + 4 * MIN + 13_000)} endedAt={at(47_000 + 4 * MIN + 19_000)} costUsd={null} contextTokens={62_000} contextWindowTokens={CONTEXT_WINDOW} outputTokens={480} />
      <Aside>
        <ToolCallCard name="bash" status="completed" args={{ command: 'gh pr create --title "WI-2481: retry GitHub webhook deliveries with backoff" --body-file /tmp/pr.md' }} startedAt={at(47_000 + 4 * MIN + 19_100)} endedAt={at(47_000 + 4 * MIN + 21_900)} exitCode={0} output="https://github.com/dude/dude/pull/412" />
      </Aside>
      <ChatMessage role="implementer" model="claude-opus-4" content="PR **#412** opened. Handing off to the reviewer." startedAt={at(47_000 + 4 * MIN + 22_000)} endedAt={at(47_000 + 4 * MIN + 23_000)} costUsd={null} contextTokens={62_700} contextWindowTokens={CONTEXT_WINDOW} outputTokens={18} activity="completed" />
      <ChatMessage role="system" content="PR #412 opened · session completed" startedAt={at(47_000 + 4 * MIN + 23_000)} />
    </ChatTranscript>
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
        <Button size="sm" variant="quiet" onClick={() => setItems(PLAN_BASE)}>
          Reset
        </Button>
      </Row>
      <AgentPlan items={items} meta="conductor" />
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
  const steerTimers = useRef<Set<ReturnType<typeof setTimeout>>>(new Set());

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
    for (const id of steerTimers.current) clearTimeout(id);
    steerTimers.current.clear();
    draftRef.current = initialDraft();
    setState(null);
    setRev(0);
  };
  useEffect(() => {
    const timers = steerTimers.current;
    return () => {
      stop();
      for (const id of timers) clearTimeout(id);
    };
  }, [stop]);

  const onSubmit = (sub: ComposerSubmission) => {
    if (sub.mode === "answer") play(buildAnswerSteps(Date.now(), sub.text));
    else if (sub.mode === "steer") {
      // Inject the steer into the running scenario without restarting it.
      // Its later steps (delivery once the turn ends) run on their own
      // clock so the queued state is actually seen.
      const steer = buildSteerSteps(Date.now(), sub.text);
      const commit = (label: string) => {
        setState(snapshot(draftRef.current, label, false));
        setRev((r) => r + 1);
      };
      for (const st of steer) {
        if (st.at === 0) {
          st.apply(draftRef.current);
          commit(st.label);
        } else {
          const id = setTimeout(() => {
            steerTimers.current.delete(id);
            st.apply(draftRef.current);
            commit(st.label);
          }, st.at * 1000);
          steerTimers.current.add(id);
        }
      }
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
        <Button size="sm" variant="quiet" onClick={reset} disabled={!s}>
          Reset
        </Button>
        {s ? <Caption>{playing ? `▶ ${s.stepLabel}` : s.question ? "⏸ waiting for your answer" : s.done ? "■ finished" : "⏸ paused"}</Caption> : null}
      </Row>
      <ChatTranscript
        session={
          s
            ? {
                id: "ses_01J9K2",
                role: "conductor",
                status: s.status,
                model: "claude-opus-4",
                taskKey: "WI-2481",
                title: "Add retry with backoff to the GitHub webhook handler",
                repo: "dude/dude",
                branch: "wi-2481-webhook-retry",
                startedAt,
                endedAt: s.status === "completed" ? Date.now() : undefined,
                costUsd: s.costUsd,
                budgetUsd: 2.5,
                tokens: s.tokens,
              }
            : { id: "ses_01J9K2", role: "conductor", status: "pending", model: "claude-opus-4", taskKey: "WI-2481", title: "Add retry with backoff to the GitHub webhook handler", repo: "dude/dude" }
        }
        headerActions={
          <>
            <IconButton icon="pause" label="Pause" size="sm" disabled={!running} />
            <IconButton icon="stop" label="Abort" size="sm" disabled={!running} />
            <IconButton icon="external" label="Open events" size="sm" />
          </>
        }
        pinned={s && s.plan.length > 0 ? <AgentPlan items={s.plan} meta="conductor" defaultCollapsed /> : undefined}
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
  if (turn.kind === "question" && turn.role !== "human" && turn.role !== "system") {
    return <QuestionCard role={turn.role} text={turn.text} options={turn.options} askedAt={turn.startedAt} answeredAt={turn.answeredAt} isNew />;
  }
  if (turn.kind === "human") return <ChatMessage role="human" name={turn.name} intent={turn.intent} inReplyTo={turn.inReplyTo} content={turn.text} startedAt={turn.startedAt} deliveredAt={turn.deliveredAt} read={turn.read} isNew />;
  if (turn.role === "human" || turn.role === "system") return null;
  const streaming = turn.activity === "streaming";
  const thought = turn.thought;
  const thinking = turn.activity === "thinking" && thought !== undefined && turn.thoughtShown !== undefined && turn.thoughtShown < thought.length;
  const showThought = thought !== undefined && (thinking || (turn.thoughtShown ?? 0) > 0);
  return (
    <>
      {showThought ? (
        <Aside>
          <ThinkingBlock text={thought.slice(0, turn.thoughtShown ?? thought.length)} streaming={thinking} startedAt={turn.thoughtStartedAt} endedAt={thinking ? undefined : turn.thoughtEndedAt} />
        </Aside>
      ) : null}
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
        contextTokens={turn.contextTokens}
        contextWindowTokens={CONTEXT_WINDOW}
        outputTokens={turn.outputTokens}
        attachments={
          turn.tools.length > 0
            ? turn.tools.map((tc) => (
                <ToolCallCard key={tc.id} name={tc.name} status={tc.status} args={tc.args} startedAt={tc.startedAt} endedAt={tc.endedAt} output={tc.output} result={tc.result} diff={tc.diff} error={tc.error} exitCode={tc.exitCode} slowAfterMs={SLOW_DEMO_MS} />
              ))
            : undefined
        }
      />
    </>
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
