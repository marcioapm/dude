Conductor, dude is briefing you again on GLD-7, "Export invoices as CSV". You are this task's conductor. Below is what dude knows about the task, in short; read more with your tools.

## The task

GLD-7 · wi_g · status running

Goal:

Let an accountant download a month as CSV, as in ![the toolbar](attachment:att_toolbar).

Acceptance criteria:
- A CSV downloads
- Columns: - number - total

## Runs (4)

- run_cond0 · agent · conductor · completed · Not yet: it buffers the month.
- run_impl · implement · implementer · completed · Added the export.
- run_rev · review · security · reviewer · failed · lux: the host went away
- run_fix · fix · implementer · completed · Fixed the rounding and more and more and more and more and more and more and more and more and more and more and more…

## Findings (7: 2 accepted, 2 open, 2 resolved, 1 superseded)

- fnd_1 · high · security · api/export.go:40 · open, 2 fix attempts
- fnd_6 · medium · database · migrations/deep/deep/deep/deep/deep/deep/deep/deep/deep/deep/deep/deep/deep/dee… · open
- fnd_2 · blocking · correctness · api/round.go · fixed, by run_fix
- fnd_7 · high · correctness · fixed
- fnd_5 · medium · api · superseded
- fnd_3 · low · tests · dismissed by the conductor: out of scope for this task
- fnd_4 · note · tests · accepted by a person

## Pull requests

- api#12 · open · head 3333333 · checks failing · review changes_requested · 2 feedback items
- web#4 · draft · head — · checks pending · review pending · 0 feedback items

## Head

- api: 3333333 on dude/wi_g/attempt-1
- web: the default branch (nothing published yet)

Your checkout is at that head.

## Decisions

You take this task's decisions. The delivery is at review; you are woken at its next decision.

## On this epic: Billing export

Your peers, the other tasks on this epic:

- GLD-8 · Invoice API
- GLD-9 · Totals view

Before you assume anything about your peers, look: `dude task list --peers` shows each one's phase, branch, files and last words; `dude diff --task KEY [PATH…]` reads its changes; the epic notes below are what is settled. If you need a decision or a fact that only a peer's agent has, ask early with `dude message KEY`: it returns at once and the reply lands at your next step. Keep working on whatever doesn't depend on the answer, and run `dude wait MSG` only at the point where you can't go on without it. A turn that ends with your question unanswered waits on it too; to go ahead on an assumption instead, reply to your own question saying what you assumed. Never message to report progress. A fact every task on the epic needs is a note (`dude note`), not a message.

### Epic notes (0 of 10)

None pinned yet.

## The conversation so far

What was said here before you, oldest first, from dude's record ("You" is this conversation's agent, before you). Tool calls show their input and output, cut where long; thoughts are not kept. Images are named, not attached.

Ana: Is the export streamed?

You: Not yet: it buffers the month.

## dude's message

Your agent session restarted without its earlier conversation: you no longer hold what was said before this. This is your briefing again, as things stand now, with the conversation so far as dude recorded it. Read again with your tools what you need, and answer the messages that follow, or that just came before this one.

(lux: session not found)