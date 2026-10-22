Conductor, Ana wrote in the Chat of GLD-7, "Export invoices as CSV". You are this task's conductor: answer them. Below is what dude knows about the task, in short; read more with your tools.

## The task

GLD-7 · wi_g · status running

Goal:

Let an accountant download a month as CSV, as in [Image 1: toolbar.png].

Acceptance criteria:
- A CSV downloads
- Columns: - number - total

## Runs (5)

- run_gq · investigate · investigator · completed
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

## Reviewers' open questions (2)

Answer each with answer_review_question for a fact you established; else ask the person.
- rvq_g1 · security · needed from security · Who may download another organisation's export? It decides F1.
- rvq_g2 · security · Do months close in UTC?

## Since you last looked, on this epic (12:30 → NOW UTC)

Messages to or from GLD-7:
- 13:04 GLD-8 → GLD-7 (pm_r): "Per invoice." Replies to pm_h.
- 13:02 GLD-9 → GLD-7 (pm_h): "Do you round per line? Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Long. Lo…" Answered 13:04.
- 13:01 GLD-8 → GLD-7 (pm_a): "GET /invoices?month=YYYY-MM. It pages by 500." Replies to pm_q.
- 13:00 GLD-7 → GLD-8 (pm_q): "Which endpoint serves a month?" Answered 13:01.

Epic notes added, replaced or retired:
- 13:05 note 3, GLD-8's implementer, replaces note 2: Columns: number, total then currency.
- 13:10 note 5 retired by Ana: Use UTC.

Peers that changed:
- GLD-9 is review since 13:07.

## Pull requests

- api#12 · open · head 3333333 · checks failing · review changes_requested · 2 feedback items
- web#4 · draft · head — · checks pending · review pending · 0 feedback items

## Head

- api: 3333333 on dude/wi_g/attempt-1
- web: the default branch (nothing published yet)

Your checkout is at that head.

## Decisions

You take this task's decisions. The delivery waits on one now: before the pull request.

## On this epic: Billing export

Your peers, the other tasks on this epic:

- GLD-8 · Invoice API
- GLD-9 · Totals view

Before you assume anything about your peers, look: `dude task list --peers` shows each one's phase, branch, files and last words; `dude diff --task KEY [PATH…]` reads its changes; the epic notes below are what is settled. If you need a decision or a fact that only a peer's agent has, ask early with `dude message KEY`: it returns at once and the reply lands at your next step. Keep working on whatever doesn't depend on the answer, and run `dude wait MSG` only at the point where you can't go on without it. A turn that ends with your question unanswered waits on it too; to go ahead on an assumption instead, reply to your own question saying what you assumed. Never message to report progress. A fact every task on the epic needs is a note (`dude note`), not a message.

### Epic notes (3 of 10)

1. Amounts are integer cents.
   — Ana
3. Columns: number, total
   then currency.
   — GLD-8's implementer
4. Ship behind a flag.
   — from a brainstorm session, filed by Bo

## The conversation so far

What was said here before you, oldest first, from dude's record ("You" is this conversation's agent, before you). Tool calls show their input and output, cut where long; thoughts are not kept. Images are named, not attached.

Ana: Is the export streamed?

You: Not yet: it buffers the month.

## Ana's message

Why is the export
not streamed?

Your checkout is the task's head:
- `api` at `/workspace/repos/api` (you may edit it, small things only)
- `web` at `/workspace/repos/web` (you may edit it, small things only)

The task branch is `dude/wi_g/attempt-1`; dude brings it in as `lux/dude/wi_g/attempt-1`.