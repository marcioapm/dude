# Tools for agents: dude's MCP server

Status: design, 2026-09-24. Task #44 (and #45, repository access, built on
it).

## What

Agents get tools that act on dude itself — the things today only a person
can do in the UI — through an MCP server the orchestrator serves (MCP
streamable HTTP, the official Go SDK `github.com/modelcontextprotocol/go-sdk`).
lux passes it to every agent session (`workload.mcpServers`, agreed with lux;
ACP `mcpServers` for OpenCode, `--mcp-config` for Claude Code).

## Tools (first set)

| tool | what it does | who may |
|---|---|---|
| `create_task` | a new task in the same project (title, goal, criteria, epic, repositories) — not delivered; a person decides. The goal is required, at least 16 characters trimmed, as a person's is (`GoalMin`, `TASK_GOAL_MIN`) | implementer, investigator, conductor |
| `create_epic` | a new epic in the project | investigator |
| `list_tasks` | the project's epics and tasks, with status and keys. With `peers: true` (`dude task list --peers`), the caller's peers instead (`task_peers`, migration 111: the other tasks on its epic; none off an epic), its own task marked `self`: each one's key, title, status, newest phase Run's phase and role, `run` live / parked / none, the peer it waits on, branch and head per repository, files by churn from the stored `run_diffs` snapshot (top 10 of `fileCount`), its agent's last message (one paragraph, ≤ 600 characters), and its unread messages to the caller (`messagesForYou`, `waitsOnYourReply`) | all |
| `search_memory` | search the project's history: tasks, findings, artifacts' text, PR titles (Postgres full-text first; embeddings later) | all |
| `ask_person` | decisions only a person can make; the turn ends and the answers are the next input. The only way an agent asks: the question block is gone. Either today's `{question, choices?: string[], actions?}` or `{questions: [{header, question, choices?: [{label, description?, recommended?}], multiple?}]}` — 1 to 4 questions asked together (header ≤ 24 characters and required when several, question ≤ 4000, 0–6 choices, label ≤ 120, description ≤ 300, at most one recommended, labels distinct); exactly one of the two, refused readably otherwise. From the shell: `dude ask "Q" --choice C`, or `dude ask --questions-json '[…]'` with the same array, sent to the same tool. dude adds "Something else…" (the person's own words) to every question: the agent never lists an "other". Kept whole in `questions.items` (migration 104); `prompt` is the question for one, "4 questions: Retry scope, …" for several, and `options` one question's labels. The person answers every question at once in the question's turn (`{answers: [{choices, text}], note?, attachmentIds?}` on the answer route; `{text}` still answers one); the answers are kept in `questions.answers`, and the agent is told them as one message ("Answers to your 4 questions: …", each question with its answer, then "Also from <name>:" and the note). One question's answer and directive are exactly as before ("Answer to your question %q"), which the pull request gate and an escalation's question match on. A conductor's is answered in its task's Chat. A conductor's asked while its delivery waits at an escalation is that escalation's question — one question, never several: its `choices` each need an `actions` entry naming one of the escalation's actions; the owner picking a choice decides the escalation as the banner does, and the banner closes the question (`question.closed`). A brainstorm session's takes `to` too: the member named answers the whole ask | implementer (and so the fixer), investigator, conductor |
| `dude publish` | a file for people, from the shell: `dude publish FILE [--name NAME] [--description TEXT]`, the description one short line of what the file is for. It runs lux's `/.lux/bin/lux-shim publish` and answers with its JSON, `{"id","name","size","sha256"}`; a refusal is lux-shim's own words on stderr and its exit status. The file is listed with the task or session while the Run goes on (lux's `artifact.published` on the Run's stream, recorded by the translator), and the same name again is a new version. Agents are told only of `dude publish`, never a directory or `lux-shim`. On an older lux (`$LUX_ARTIFACTS` set) it copies the file there instead and drops the description, collected when the Run stops | all |
| `request_repository` | ask for another repository of the organization, read or write, with a reason; a person approves or denies (#45) | all |
| `run_diff` | what a Run of the caller's task changed (`dude diff`): its checkout, uncommitted work included, against the commit it started from, as stored in `run_diffs` — one snapshot, no history. Without paths, the files by churn with counts and no lines (paged, 200 by default, at most 1,000); `nameStatus`, path and status only; with paths, those files as unified diff text, at most 2,000 lines a call. Another task's Run is refused. With `task` (a peer's key; `dude diff --task ABS-2 [PATH…]`), that peer's newest implement / fix / simplify Run, a live one first, read from the same stored snapshot with the same paging and bounds; a task that is not a peer is refused as another task's Run is | all |
| `message` | text to one peer task (`dude message ABS-2 "…" [--reply-to MSG]`), never a Run, never a broadcast. Never blocks: returns `{id, to, lands: next_step \| next_run, notice?}`. It is a directive (no `interrupt`) on the Run that speaks for the task — its newest live implementer, fixer or investigator; a reply goes to the Run that asked, while live — read at its next step; with none, it is held for the task's next such Run's prompt and `notice` says "ABS-3 has no Run going; it will see this when its next Run starts." A task that is done, failed or aborted is refused ("ABS-3 is done: no Run will read this. …"). At most `SteerTextMax`; at most 5 per Run in any rolling hour, the sixth refused saying when the next is allowed. A reply to one's own message closes one's own question. Refusals are `agent.message.refused` | implementer (and so the fixer), investigator, conductor |
| `wait_for_reply` | ends the turn until the reply to one of the caller's own messages (`dude wait MSG`); returns the reply at once if it has come, or the peer-ended line, opening nothing, if the peer's task has ended. One open wait per Run | implementer, investigator, conductor |
| `note` | pins a note on the caller's epic (`dude note "…" [--replaces N]`), numbered, never edited; with `replaces`, a new note in N's place. Returns `{id, n, replaces, told, current}`. At most 2,000 characters and 10 current notes ("This epic has 10 notes. Replace one (`dude note --replaces N`) instead."); a replace never counts. Agents cannot retire | implementer, investigator, conductor |
| `findings` | the caller's task's review findings (`dude findings [ID...]`), open and most severe first, at most 200: each one's id, severity, category, repository, `file:line`, status, who raised it and how it was settled — fixed by which Run, accepted by a person, superseded, or open after so many fix attempts. No text in the list: with `ids` (at most 20) those findings in full — title, description, suggested fix, resolution note — and `unknown` for ids that are not the task's | conductor |
| `pull_requests` | the caller's task's pull requests (`dude prs`): repository and number, URL, state, head branch and commit, base, checks (the roll-up and each check's status and conclusion), review (the roll-up and each reviewer's word), unresolved threads, and the feedback people left — the latest 50, oldest first, each with its author, kind (comment, line comment, review, changes requested), path, an excerpt of at most 280 characters on one line, whether a fixer Run was sent it (`actedOnBy`, matched on its repository, path, author and words in the Run's `pr_feedback`) and why it woke nobody (`ignored`); `feedbackTotal` counts all of it | conductor |
| `start_phase` | the decision the task's delivery waits on (`dude phase start PHASE`): a phase Run from the task's head — implement, review (`categories`, else those the change warrants), fix (`findings`, else every open one; at pull request feedback, the feedback), simplify or test — created by the workflow's own steps, marked with the conductor (`runs.conductor_run_id`) and its `note`. Refused past the policy's review rounds, fix attempts per finding or pull request fix rounds | conductor, deciding |
| `decide` | the decision waited on (`dude decide ACTION`): `next` (what Deliver would do), `ask_person` (a question for the person in `note`; before the pull request the fixed question Open / Draft / Show me the diff / Another round, recorded with the heads it was asked at), `wait` (at pull request feedback) or `open_pull_request` — only when the latest gate question, asked at the task's current heads, was answered Open (or Draft: a draft) | conductor, deciding |
| `dismiss_finding` | an open finding left as it is (`dude finding dismiss ID --reason R`): status accepted, the reason its resolution note | conductor, deciding |
| `decide_escalation` | the escalation the delivery is stopped at (`dude escalation decide ACTION [--note N]`), decided by the conductor with one of its actions — only while the conducted delivery waits at `decide`, undecided, and the latest answered question about **this** escalation (`questions.escalation`) was answered by the task's owner (`answered_by_person`). Goes through `DecideEscalationTx`, as the banner does; `task.decided` records `by: conductor`, the question and who answered, and the note carries the proposal, the answer and the conductor's word. Refused otherwise, pointing to the banner | conductor, at an escalation |
| `update_task` | what Chat settled, written into the task (`dude task update`): its goal, its acceptance criteria (the whole list), or both; a `task.updated` event with what they were. Only while no implementer has started on the attempt | conductor, deciding |
| `steer` | a steer for a live phase Run of the caller's task's current attempt (`dude steer RUN TEXT [--interrupt]`), queued as a person's is (`delivery.Steer`): read at the agent's next step, `interrupt` stops its turn. Text only, at most a person's bound (`SteerTextMax`). The directive (`conductor_run_id`) and its `run.steered` event (actor the conductor's Run, `by: conductor`) are the conductor's. Returns the directive, the Run and `lands` once lux has said it. Refused for the conductor itself, another task's Run, an earlier attempt's, a branch preview, a Run ended or ending, a done, aborted or failed task, and a conductor superseded (the Chat lock, as decisions take it). Its read wakes the conductor (`steer_read`); its failure too (`steer_failed`, with why) | conductor, whoever decides |

A task's **conductor** (the agent people talk to in its Chat) reads with
these. Its decisions (`start_phase`, `decide`, `dismiss_finding`,
`update_task`) are its only while it takes the task's decisions (the
delivery's `decider` is `conductor`) and the delivery is parked on one;
under Deliver, or on a merged or closed task, each is refused saying so
and it is read-only. Its `steer` takes no decision: it is allowed under
Deliver too, as a person's steer is. It has no tool that edits, and its checkout is
never pushed. Its briefing names findings and Runs by id, and these tools
are how it reads what the briefing leaves out.

Everything an agent creates is marked as created by that Run (`created_by_run_id`)
and shown so in the UI; nothing an agent creates starts work on its own.

## Waiting on a person

Anything that blocks on a person goes through a tool, so dude knows the
model's intent: `ask_person`, and `request_repository` with `wait: true`
(`dude repo request --wait`), which the agent uses when it cannot go on
without the repository. A turn that ends with one of these open is not
done: the Run is **waiting**. A request without `wait` doesn't hold the
turn. The agent carries on, and a later phase gets the repository if it is
approved.

- **Grace period, then parked.** A waiting Run stays live for the project's
  `parkAfterMinutes` (default 10), so someone at their desk answers a live
  agent. Past it, dude pauses the Run (`dude_pause = 'person'`): lux stops
  the container and keeps its state and the agent's conversation, so the
  Run holds nothing. The chat says "Parked while it waits for you".
- **The answer resumes it.** An answered question, or a decided request,
  resumes the same lux Run. The answer is its next message, and the
  conversation continues, whether that is the next minute or the next week.
  A person's own pause of a parked Run makes it theirs: the answer then
  waits for their Resume.
- **One ask, one answer.** An agent has at most one open ask
  (`HasOpenQuestion`), of up to four questions; the person answers them
  together, through the form in the question's turn, and nothing reaches the
  agent before Send (their picks are kept in their browser until then). A
  message written meanwhile ("Write to the agent instead", `aside`) goes to
  the agent beside the open question and never answers it; a message in a
  task's Chat answers a single question, never several.
- **A hard limit of running time.** Every phase Run is sent lux's
  `timeout` (`DUDE_AGENT_TIMEOUT`, default 4h); lux counts only time spent
  running, so parked time never counts, and past it stops the Run, which
  fails and escalates like any failure. The conductor gets none. lux keeps
  a stopped Run's state until it is cancelled.
- **Idle nudge instead of a timeout** (optional, `idleNudgeMinutes`, 0 = off).
  An agent mid-turn that has said nothing, runs no tool and waits on
  nobody for that long gets one interrupting nudge: "carry on, or ask".
  Still quiet as long again, it is parked (`dude_pause = 'idle'`). The work
  item goes to awaiting input, and back to its earlier status when a person
  resumes the Run. Only a person's Resume takes it up.
- **Decisions are rechecked.** A park or a nudge re-checks, as it writes,
  that the Run still waits or is still quiet, so an answer or a word from
  the agent in between wins.
- **Asks die with their Run.** When a Run completes, fails, is aborted or
  is lost, its open questions and pending requests are cancelled
  (triggers). The chat shows them as "No longer needed". Answering one
  gets a 409 that says the run ended.
- **A conductor is parked between messages, not for a person.** After its
  turn it stays running for `conductorWarmMinutes` (default 5), then is
  parked (`dude_pause = 'conductor'`) with nothing open for anyone: it
  raises nothing and the task does not move. The next message in Chat
  resumes it (`run_resumes.cause = 'conductor'`).

## Waiting on a peer

Agents on one epic coordinate (docs/design/agent-coordination.md). Waiting
on a peer's reply is waiting on a person, with a peer at the other end:

- **An open `peer_waits` row is part of `OpenAsk` and `HoldsTurn`**
  (`delivery/store.go`). `wait_for_reply` opens one (`how = 'wait'`); a
  turn that ends with the Run's newest question to a peer unanswered — a
  message opening a thread with no reply in it, the peer's or its own —
  opens one at the turn's end (`how = 'turn_ended'`,
  `delivery.WaitOnUnansweredTx`, from the translator's `idle`). Either way
  the turn is not done: the Run waits, does not complete, and its delivery
  does not move on.
- **Same grace, same park.** Past `parkAfterMinutes` it is parked
  (`dude_pause = 'person'`, reason "parked while it waits on a peer's
  reply"); the reply closes the wait and is queued for the Run as a
  directive, which is the resume's input. `run_resumes.woken` counts the
  wait's close.
- **Closing a question without an answer** is replying to one's own
  message, saying what one assumed: the wait closes (`closed_by = 'self'`)
  and the turn may end.
- **Waits die with their Run** (trigger `runs_close_peer_waits`, as
  questions do): `closed_by = 'run_ended'`, `agent.peer.wait_ended`.
  Messages to the Run's task stay for its next Run.
- **No conductor is woken by any of it.** A message is never queued on a
  conductor unless it is a reply the conductor itself waits on or the
  conductor is mid-turn; note changes are not told to conductors at a step.
  A conductor reads its epic's traffic in "Since you last looked, on this
  epic", in its next briefing or wake note (`delivery.ConductorDigestTx`).

## Identity and authority

- Each phase Run gets its own **bearer token**, minted at submit, stored
  hashed on the Run, sent to lux as a secret (`headers: [{name:
  Authorization, secret: DUDE_MCP_TOKEN}]`), revoked when the Run ends.
  The token names the Run; the Run names the organization, project, work
  item and role. A tool call can do nothing outside that project.
- **Which tools a role gets** is policy (project settings, defaults above);
  the server lists only those to that Run.
- Every call is a ledger event (`agent.tool.dude`) with its arguments and
  result, so the chat shows it like any other tool call.

## Where it runs

In the orchestrator process, on its own listener (`DUDE_MCP_LISTEN`), since
it acts on the same data and workflows. lux never lets a Run reach the lux
host (loopback, the host's addresses, the control plane's address), so the
listener must be reachable from lux's hosts some other way: in production
its own host name; locally, the machine's LAN address or a container on
lux's network. The egress rule for it is added to every spec.

## Order of work

1. Server skeleton: token minting and auth, `list_tasks`, `create_task`,
   the ledger event; Go tests calling it as an MCP client.
2. Spec: `workload.mcpServers` + the token secret + egress (behind a config
   flag until lux lands it; fake lux accepts and records it).
3. `search_memory`, `publish_artifact`, `ask_person`, `create_epic`.
4. `request_repository` + approval UI + resume with added repositories (#45,
   needs lux's add-repositories-on-resume).
5. Contract test on real lux once `mcpServers` lands: lux-fake calls a dude
   tool, the call appears in the chat and its effect in the API.
