# The conductor: talking to a task

Status: design, 2026-10-02. Mockup: `mockups/conductor/index.html`. Built
so far: `run_diff` / `dude diff` (#33).

## What

Today a task is delivered by a fixed pipeline: implement → review ⟲ fix →
simplify → [test] → pull request → PR feedback ⟲ fix. It runs well, but a
person can only watch it, steer one agent at a time, or answer an
escalation. They cannot talk the work through, have a review round run
again because of something they noticed, say "not yet" before the pull
request, or ask questions about a task after it was delivered.

The **conductor** is an agent that takes the pipeline's decisions, in a
chat with the people on the task. It plans with them, starts the same phase
Runs Deliver starts, reads what they did, steers them, makes small edits
itself, and asks before the pull request. Each task has one conversation
with it, its **Chat**, whether the task was started with it or delivered
automatically, and whether the work is running, waiting or merged.

Decided (2026-09-30 to 2026-10-02, Márcio):

- **One chat per task, and asking about a task is the same chat.** No
  separate "ask an agent about this PR": opening Chat on a finished task
  resumes its conductor, read-only until someone asks for a change.
- **Its name is the conductor** (the `orchestrator` agent role, renamed).
- **El Duderino is dude's voice, not the conductor.** dude's own messages —
  the first prompt, wake notes, GitHub notices, parked and resumed — are
  signed with the task's dude name (`dudeName(taskId)`: The Dude, El
  Duderino, His Dudeness, Duder) in the system style the design system
  already gives prompts. The conductor is an agent with its own face and
  colour. dude tells it what happened; it decides what to do.
- **The pipeline stays; the conductor takes its decisions.** The delivery
  workflow keeps doing the mechanics — create phase Runs, wait for all of
  them, advance the branch, open pull requests, classify GitHub. The points
  where it now applies a fixed rule become the conductor's call.
- **The pull request is the last step, and a question.** The conductor
  asks before opening it, with what ran and what was not verified.
- **Wake notes carry no diff.** A diff can be any size. A note is a fixed
  size; the conductor reads what it needs with `dude diff`.
- **The conductor may edit code, for small things.** It has its own
  checkout, kept current by lux, and publishes like a phase. "Small" is
  60 changed lines and 3 files, enforced when it publishes; past it, it
  delegates.
- **Starting is a choice, every time.** Two equal buttons, no project
  default: **Talk it through**, or **Deliver**. Plain words, kept.
- **The conductor has its own settings and a small machine.** Its own
  model, effort and machine size, like any role. Its machine is small: it
  reads, edits and talks, and never runs the code. Anything that builds or
  tests is a phase Run.

## Starting a task

Where a task's "Not started" offers Deliver today, there are two, side by
side and equal. The person picks each time; no setting picks for them.

- **Talk it through** starts the conductor in Chat. It reads the
  task and the code, then asks what it needs to (a question card, as
  `ask_person` makes today) or proposes a plan. The task waits on the
  person; the conductor is parked while it waits.
- **Deliver** is today's pipeline, unchanged: it runs to the pull request
  on its own.

Internally the decider is `policy` (Deliver) or `conductor`.

## Taking over a delivered task

The first message anyone sends in a task's Chat starts its conductor, if it
has none. It is briefed from what dude already has: the task, the workflow
state, every Run's summary and outcome, the findings, the pull requests and
their feedback. It reads more with tools. From then on the task's decisions
are its own: the step running finishes, and the next decision goes to the
conductor instead of the policy (`State.Decider`: `policy` | `conductor`).

"Let Deliver finish it" hands decisions back to the policy, from the next
decision on. A merged or closed task's conductor is read-only and cannot
start Runs; a change it is asked for becomes a follow-up task, linked to
this one, delivered or conducted like any other.

## Decisions it takes

Each of these is a decision point in `delivery/workflow.go` today. With
`Decider = conductor`, the workflow does the step's mechanics, then parks
on `SignalConductorDecision` and wakes the conductor with a note, instead
of choosing the next step itself.

| Decision point | Today | With the conductor |
|---|---|---|
| Start (`implement`) | implement at once | plan with the person, then start the implementer (or edit it itself, if small) |
| After implement (`awaitImplement`) | review | read the diff; review, steer, fix, or ask |
| After a review round (`awaitReview` → `Exit`) | fix every open finding, or escalate at the bound | triage the findings: fix some, dismiss some with a reason, ask the person, or go on |
| After a fix (`awaitFix`) | review again | review again, or not, or only some categories |
| Before the PR (`test` → `openPullRequest`) | open it | ask the person: Open / Draft / Show me the diff / Another round |
| PR feedback (`awaitPullRequest` → `prFix`) | a fixer for actionable feedback | answer, fix, or ask |
| Escalation (`decide`) | a person picks an action | the conductor proposes one; the person still decides what only a person may |

Bounds stay. The policy's review iterations, attempts per finding and PR
fix rounds bound the conductor as they bound the policy; past one, it must
ask the person. A conductor cannot loop a task's budget away any more than
the pipeline can.

## Its tools

dude's agent tools (`agenttools`), conductor-only unless said:

- `start_phase(phase, {categories, findings, note})`: a phase Run from the
  task's head — implement, review (some or all categories), fix (some or
  all findings), simplify, test. The same `CreatePhaseRun` the workflow
  uses, so these are ordinary Runs: their own checkout, record, cost and
  live Changes.
- `steer(run, text, {interrupt})`: a directive, as a person's steer is
  today (`directives`), marked as the conductor's.
- `run_diff` / `dude diff` (all roles, built): what a Run changed, files
  first, then the lines of the files named.
- `findings`, `dismiss_finding(id, reason)`, `pull_requests`: what the
  workflow knows, read and acted on.
- `decide(action, note)`: the decision the workflow is parked on — the next
  step, "ask the person", or "open the pull request" (only after the person
  said so).
- `ask_person` (built): a question card. The task waits on the person.

## Wakes

The conductor runs a turn when there is a decision to take or a person
wrote. Nothing else wakes it.

- **What wakes it:** a decision the workflow parked on (a phase finished, a
  review round complete, PR feedback the classifier judged actionable, an
  escalation); a person's message in Chat; a Run it started that failed or
  asks a question.
- **What does not:** approvals, green checks, readiness, a bot's comment,
  one reviewer of four finishing. They appear in Chat as El Duderino's
  notices and wake nobody.
- **Fan-out is waited for by dude.** Four reviewers are one wake, "review
  round done", as `awaitReview` already waits for every reviewer.
- **Coalesced.** Wake reasons that arrive within a short window (15 s, or
  while a turn is running) are one turn: one note listing them, not one
  turn each.
- **Bounded notes.** A note is fixed-size facts and ids: which Run,
  finished or failed, its commit, how many files and lines, whether tests
  passed, its findings by severity. For a batch, one line per Run, capped.
  Never a diff, a file list or a finding's text: the conductor reads those
  with tools.
- **One safety net.** A conductor asleep for long with a Run of its own in
  flight is woken once, to say what it is waiting for. No other polling.

## Its settings and machine

- **Its own role settings.** `conductor` is a role in
  `default_agent_models` and `agent_models` like the others — model,
  effort, machine size — with its own settings page. It does not follow
  the implementer.
- **A small machine.** It reads code, runs `dude diff`, `rg` and `git`,
  edits and talks; it never builds, installs or runs tests. Every
  organisation gets a small size for it, **Small: 0.5 CPU · 1 GiB ·
  10 GiB**, the conductor's default (a migration for existing
  organisations, the seeding trigger for new ones). Disk is the checkout
  and nothing else.
- **What it cannot run, a phase Run does.** "Are the tests green?" is
  answered by the last implementer, fixer, simplifier or test Run on that
  head, which it reads. It does not claim what it did not see a Run do.

Between turns it is **warm, then parked**:

- After a turn, it stays running for a grace period, so a person answering
  at their desk gets a live reply (`conductorWarmMinutes`, default 10 —
  about the model's prompt cache).
- Past it, dude parks it (`dude_pause = 'conductor'`): lux stops the
  container and keeps its state and conversation. A wake resumes it.
- A wake after a phase Run is minutes later anyway, so a resume's latency is
  invisible there. **To measure first:** a lux resume's latency, end to end.
  If it is a few seconds, the grace period can be shorter.

## Its checkout

- **Phase Runs are unchanged:** each starts from the task's heads in its
  own clone, passes its work on by its branch, and dude fast-forwards the
  task branch. Reviewers read the same commit in parallel.
- **The conductor has a writable checkout, kept current.** On every resume
  and every wake, lux brings in what other Runs pushed: its runner, which
  holds the credential, fetches the task's heads into the host's mirror and
  leaves a git bundle in the container; inside, git fetches the bundle and
  fast-forwards. No credential enters the container and lux never runs git
  in the checkout, as today. This is a lux change: "incoming bundle" on
  resume and on request (`POST /v1/runs/:id/refresh`).
- **Until then**, it reads with `dude diff` and does not edit.
- **Its edits publish like a phase:** commit, `lux push` to its own branch
  with the usual lease, and dude fast-forwards the task branch — refused if
  its checkout was not current, which says so instead of losing work.
- **One writer at a time.** It cannot publish while an implementer or fixer
  is running on the task; it waits, or steers that agent instead.
- **Small, enforced.** A publish past the project's limit (60 changed
  lines, 3 files) is refused with "delegate this".
- **Never untested at the PR.** It cannot run tests, so its commits go
  through the next review round like any other, and the PR gate refuses a
  head whose last commit is the conductor's until a review or test Run
  has run on it.

## What it replaces from threadctl and arny

The rules we keep by hand when an agent conducts agents outside dude,
mostly already true of dude:

- **Watchdogs and frozen-run detection** → dude reads every Run's stream:
  turn ends, failures and quiet agents are known, not inferred.
- **Tracking a pull request** → webhooks and the classifier.
- **Monitors and stale wakes** → unnecessary: every wake is an event. The
  one safety net above is the exception.
- **Never stacking two watches** → structural: the workflow's state is the
  only watch.
- **Pause/resume being a person's** → stays a person's, in the UI.
- **Status cards** → the task's status and its Chat.

## Where it lives in the task's screen

- A **Chat** tab, first, on every task. Nothing else moves.
- **Sessions** becomes a tree: the conductor, and the Runs it started under
  it. A delivered task's Runs sit under "Delivered automatically".
- Overview, Findings, Files and Servers are unchanged: the conductor's
  Runs are ordinary phase Runs.
- In Chat, each Run it starts is a collapsed thread in its role's colour;
  a steer shows where it landed; El Duderino's notes are system lines.

## GitHub

- Changes requested, a person's comment, CI turning red: wake the
  conductor (today: a fixer).
- Approved, green, ready to merge: a notice in Chat, no wake.
- Merged or closed: one wake, to close out.
- **@dude on a pull request** is a message to its task's conductor, which
  answers in the pull request's thread.
- **Webhooks are a prerequisite.** Today only one repository has dude's
  webhook; the rest are read every 15 minutes. A project's repositories
  get the webhook when they are added (`EnsureWebhook`, today a button in
  Settings → GitHub).

## Order of work

1. **Measure** a lux resume, end to end.
2. **Read-only Chat** on any task: the conductor role, its settings and
   the Small size, its Run, briefing
   from the task, `dude diff`, `findings`, `pull_requests`, `ask_person`;
   warm-then-parked; El Duderino's notes. No decisions yet.
3. **Decisions:** `State.Decider`, the decision points parking on
   `SignalConductorDecision`, `start_phase`, `decide`,
   `dismiss_finding`, coalesced wakes; Talk it through; take-over; the PR
   gate.
4. **Steering:** `steer`, and lux delivering a steer mid-turn.
5. **GitHub:** routing to the conductor, @dude, webhooks on project add.
6. **Edits:** lux's incoming bundle, the conductor's publish, its limit.

## Open

Nothing at the moment. Decided 2026-10-02: the start buttons are Deliver
and Talk it through; the edit limit is 60 lines and 3 files; Small is
0.5 CPU · 1 GiB · 10 GiB.
