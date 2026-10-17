/*
 * Classification screens that are not the task page: proposal-card-kinds
 * (a brainstorm's proposal, the real ProposalCard, with each item's kind
 * and priority in its title and its tag) and brief-bug (what the
 * implementer's brief and the reviewer's gate say for a bug, a spike and
 * a feature — text, quoted from prompts.go, the added lines marked).
 */

import { useState } from "react";
import { ChatComposer, ChatMessage, ChatTranscript, ProposalCard, ScreenHeader, SessionTitle, SharedMark } from "@dude/design-system/components";
import { KIND, P, type Kind, type Priority } from "./data.ts";
import { KindMark, PriorityMark } from "./Marks.tsx";
import styles from "./pages.module.css";

const T0 = Date.UTC(2026, 9, 16, 10, 0, 0);
const CP = { key: "CP", name: "control-plane" };

const ITEMS: ReadonlyArray<{ title: string; detail: string; kind: Kind; priority: Priority; n: number }> = [
  { n: 1, title: "Deliveries queued during a deploy are dropped", detail: "Steps: deploy while a push arrives; the delivery is acknowledged and never processed. Expected: processed once the new process is up.", kind: "bug", priority: "P0" },
  { n: 2, title: "Spike: can we drain the webhook queue before a deploy stops the process?", detail: "Answer in a write-up: how long a drain takes at today's volume, and what the deploy has to wait for.", kind: "spike", priority: "P1" },
  { n: 3, title: "Replay endpoint for missed deliveries", detail: "Admins re-run a delivery by id from the last 72 hours.", kind: "feature", priority: "P2" },
  { n: 4, title: "Drop the unused webhook_retries table", detail: "Nothing reads it since CP-30.", kind: "chore", priority: "P3" },
];

export function ProposalKindsPage() {
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set([0, 1, 2]));
  return (
    <div className="screen" data-testid="session-screen">
      <ScreenHeader fillTitle title={<SessionTitle title="Webhooks and deploys" />} meta={<><SharedMark owner={P.ana} label="Shared with 1" /><span>Brainstorm</span></>} />
      <ChatTranscript fill revision={1} turns={2}
        footer={<ChatComposer mode="chat" placeholder="Message the brainstorm…" to={<>To <b>Brainstorm</b></>} sentAs="Ana · everyone in the session sees it" onSubmit={() => undefined} />}>
        <ChatMessage role="human" name="Ana" person={P.ana} intent="message" startedAt={T0}
          content="We lost pushes again during yesterday's deploy. File what we need, most urgent first." />
        <ChatMessage role="brainstorm" name="Brainstorm" model="claude-opus-5-5" startedAt={T0 + 20_000} endedAt={T0 + 80_000} costUsd={0.38}
          content={"The lost pushes are a **bug**, P0: the steps are in item 1, so its implementer reproduces it first. Whether a drain is enough is a question, so item 2 is a **spike**: its answer is a write-up. The replay endpoint is a feature at the normal priority; dropping the old table is a chore, when there is room."}
          attachments={
            <ProposalCard data-testid="proposal-card" filingAs="Ana" selected={selected}
              onToggle={(i) => setSelected((s) => { const n = new Set(s); if (n.has(i)) n.delete(i); else n.add(i); return n; })}
              onFile={() => undefined}
              items={ITEMS.map((it) => ({
                kind: "task" as const, n: it.n, project: CP, canFile: true,
                title: <span className={styles["itemLead"]}><KindMark kind={it.kind} quietDefault />{it.title}</span>,
                detail: it.detail,
                tag: <span className={styles["itemLead"]}>{KIND[it.kind].label}<PriorityMark priority={it.priority} showDefault /></span>,
              }))} />
          } />
      </ChatTranscript>
    </div>
  );
}

// ---------------------------------------------------------------------------
// brief-bug
// ---------------------------------------------------------------------------

export function BriefPage() {
  return (
    <div className={styles["doc"]} data-testid="brief">
      <div className={styles["docInner"]}>
        <h1>What a kind changes for the agents</h1>
        <p className={styles["lede"]}>
          A kind is not a label. It adds a paragraph to what the implementer is told, a check to what the tests reviewer
          looks for, and, for a spike, a different end. Everything else in the prompt is as today. Highlighted lines are
          the additions; the rest is quoted from <span className={styles["source"]}>orchestrator/internal/delivery/prompts.go</span>.
        </p>

        <h2>The implementer's brief</h2>
        <div className={styles["compare"]}>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="feature" variant="word" /> CP-44, a feature</h3>
            <pre className={styles["prompt"]}>{`Implement this task. Run the project's formatter, type checks and tests before you finish — handing over code that does not build is not finishing.

Billing page: usage this month and projected invoice

Ana's customers ask what they'll pay before the invoice arrives…

Acceptance criteria:
- Usage this month, by day, from usage_hourly…`}</pre>
            <p className={styles["source"]}>builtinInstructions["implementer"] · PromptInput.task()</p>
          </div>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="bug" variant="word" /> CP-62, a bug</h3>
            <pre className={styles["prompt"]}>{`Implement this task. Run the project's formatter, type checks and tests before you finish — handing over code that does not build is not finishing.
`}<span className={styles["added"]}>{`
This task is a bug. Before you change any code that fixes it:
1. Reproduce it with the steps below, and say what you saw.
2. Write a test that fails because of it — named for what goes
   wrong ("exports every row when a name has a comma"), with
   CP-62 in a comment beside it — and commit it on its own:
   "Failing test for CP-62".
3. Then fix it, in a second commit, and run the test again.
If you can't reproduce it, stop and say so: don't guess a fix.
`}</span>{`
CSV export drops rows when an organisation's name has a comma

Customers with a comma in their organisation's name…
`}<span className={styles["added"]}>{`
Steps to reproduce:
1. Rename an organisation to "Acme, Inc.".
2. Export usage as CSV from the billing page.
Expected: Every hour of usage, one row each.
Actual: Rows for Acme, Inc. are missing.
Environment: control-plane 1.18.0, staging
`}</span>{`
Acceptance criteria:
- A name with a comma, a quote or a newline exports every row`}</pre>
            <p className={styles["source"]}>new: kindInstructions["bug"] after the role's lead · PromptInput.task() gains the bug report</p>
          </div>
        </div>

        <h2>The tests reviewer's gate</h2>
        <div className={styles["compare"]}>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="feature" variant="word" /> A feature</h3>
            <pre className={styles["prompt"]}>{`Judge the tests, not the code they test. Each should assert behaviour through the public interface… A missing test for an acceptance criterion or a fixed defect is a finding.`}</pre>
            <p className={styles["source"]}>reviewFocus["tests"]</p>
          </div>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="bug" variant="word" /> A bug</h3>
            <pre className={styles["prompt"]}>{`Judge the tests, not the code they test. … A missing test for an acceptance criterion or a fixed defect is a finding.
`}<span className={styles["added"]}>{`
This task is the bug CP-62. Find the test that reproduces it: a
commit that adds it before the fix, failing there and passing at
the head. Check it out at that commit and run it. No such test, a
test that passes before the fix, or one that tests something else
than the steps describe, is a blocking finding: "No failing test
for CP-62".`}</span></pre>
            <p className={styles["source"]}>new: reviewFocus["tests"] + bugGate(task) when kind = bug · finding category "tests" · when tests isn't among the project's required reviewers, the same paragraph is added to reviewFocus["correctness"], so a bug is never delivered without its failing test</p>
          </div>
        </div>

        <h2>A spike, and a chore</h2>
        <div className={styles["compare"]}>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="spike" variant="word" /> CP-64, a spike</h3>
            <pre className={styles["prompt"]}><span className={styles["added"]}>{`This task is a question, not a change. Answer it in a write-up:
publish it as an artifact (answer.md) — what you tried, what you
found, what you recommend, and what you did not check. Commit
no code to the task branch; throwaway code stays in your checkout.`}</span></pre>
            <p className={styles["source"]}>new: kindInstructions["spike"] · nothing changed, so openPullRequest opens none and the task waits in review, "ready to read" (awaitRead, workflow.go:1081) · its owner's Mark done (markDone, api.go:1153) closes it as Nothing to merge</p>
          </div>
          <div className={styles["compareCol"]}>
            <h3><KindMark kind="chore" variant="word" /> CP-65, a chore</h3>
            <pre className={styles["prompt"]}>{`(No change: delivered and reviewed as a feature.)`}</pre>
            <p className={styles["source"]}>a chore is labelled and filtered; it skips nothing</p>
          </div>
        </div>

        <h2>What a reader sees</h2>
        <ul>
          <li>The bug's pipeline reads <b>Implement · reproduce, failing test, fix</b>; its Findings may carry “No failing test for CP-62” (blocking, tests).</li>
          <li>A spike's pipeline runs as configured; with no diff, review has nothing to read and no pull request opens. Its Files tab has answer.md, and the header offers <b>Mark done</b>, which ends it as Nothing to merge.</li>
          <li>The proposal card names each item's kind; filing keeps it, and the brief follows it.</li>
          <li>Changing the kind after delivery has started is refused ("Delivery has started: its kind is fixed"), as the source of a started task is.</li>
        </ul>
      </div>
    </div>
  );
}
