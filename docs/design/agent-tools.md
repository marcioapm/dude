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

| tool | what it does | capability |
|---|---|---|
| `create_task` | a new task in the same project (title, goal, criteria, epic, repositories) — not delivered; a person decides. The goal is required, at least 16 characters trimmed, as a person's is (`GoalMin`, `TASK_GOAL_MIN`) | `tasks.create` |
| `create_epic` | a new epic in the project | none: not built (no such tool) |
| `list_tasks` | the project's epics and tasks, with status and keys. With `peers: true` (`dude task list --peers`), the caller's peers instead (`task_peers`, migration 114: the other tasks on its epic; none off an epic), its own task marked `self`: each one's key, title, status, newest phase Run's phase and role, `run` live / parked / none, the peer it waits on, branch and head per repository, files by churn from the stored `run_diffs` snapshot (top 10 of `fileCount`), its agent's last message (one paragraph, ≤ 600 characters), and its unread messages to the caller (`messagesForYou`, `waitsOnYourReply`) | `tasks.read` |
| `search_memory` | search the project's history: tasks, findings, artifacts' text, PR titles (Postgres full-text first; embeddings later) | `memory` |
| `ask_person` | decisions only a person can make; the turn ends and the answers are the next input. The only way an agent asks: the question block is gone. Either today's `{question, choices?: string[], actions?}` or `{questions: [{header, question, choices?: [{label, description?, recommended?}], multiple?}]}` — 1 to 4 questions asked together (header ≤ 24 characters and required when several, question ≤ 4000, 0–6 choices, label ≤ 120, description ≤ 300, at most one recommended, labels distinct); exactly one of the two, refused readably otherwise. From the shell: `dude ask "Q" --choice C`, or `dude ask --questions-json '[…]'` with the same array, sent to the same tool. dude adds "Something else…" (the person's own words) to every question: the agent never lists an "other". Kept whole in `questions.items` (migration 104); `prompt` is the question for one, "4 questions: Retry scope, …" for several, and `options` one question's labels. The person answers every question at once in the question's turn (`{answers: [{choices, text}], note?, attachmentIds?}` on the answer route; `{text}` still answers one); the answers are kept in `questions.answers`, and the agent is told them as one message ("Answers to your 4 questions: …", each question with its answer, then "Also from <name>:" and the note). One question's answer and directive are exactly as before ("Answer to your question %q"), which the pull request gate and an escalation's question match on. A conductor's is answered in its task's Chat. A conductor's asked while its delivery waits at an escalation is that escalation's question — one question, never several: its `choices` each need an `actions` entry naming one of the escalation's actions; the owner picking a choice decides the escalation as the banner does, and the banner closes the question (`question.closed`). A brainstorm session's takes `to` too: the member named answers the whole ask | `ask` |
| `dude publish` | a file for people, from the shell: `dude publish FILE [--name NAME] [--description TEXT]`, the description one short line of what the file is for. It runs lux's `/.lux/bin/lux-shim publish` and answers with its JSON, `{"id","name","size","sha256"}`; a refusal is lux-shim's own words on stderr and its exit status. The file is listed with the task or session while the Run goes on (lux's `artifact.published` on the Run's stream, recorded by the translator), and the same name again is a new version. Agents are told only of `dude publish`, never a directory or `lux-shim`. On an older lux (`$LUX_ARTIFACTS` set) it copies the file there instead and drops the description, collected when the Run stops. With no file (`dude publish [--message M]`) it is the `publish` tool instead: a conductor's commits to the task branch | `dude publish FILE`: none, every Run (lux-shim, not a dude tool). `dude publish` with no file (the `publish` tool): `conduct` |
| `request_repository` | ask for another repository of the organization, read or write, with a reason; a person approves or denies (#45) | `run.basics` |
| `sync_base` | brings the base in without moving the checkout (an MCP tool only; no `dude` command): lux fetches each writable repository's base — the repository's default branch, or the base of the branch the task continues — into the checkout as `lux/<base>`, a sync in mode `fetch` with the Run's push credentials. Waits at most 20 s for lux's `git.sync` reports and answers, per repository, `lux/<base>`, `status` (`fetched`, `up-to-date`, `failed`, or `asked` when lux has not reported yet; a call whose own report has not come counts the newest fetch lux reported landing for an earlier call of the same Run, read for at most 5 s more, and says so, naming the SHA that fetch landed and giving no counts), commits behind and ahead, and the next step: `git merge-tree --write-tree HEAD lux/<base>`, then `git merge lux/<base>` — merge only, never a rebase or a force, since every move of the task branch is a fast-forward. Writers run the affected tests; the conductor never builds or tests, resolves only small merges, publishes, and delegates review and test. Refused on a lux without sync modes, with no writable repository, or while the container is not running. Tested against the fake lux only: real lux's `fetch` mode is assumed, not yet exercised | `base.sync` |
| `run_diff` | what a Run of the caller's task changed (`dude diff`): its checkout, uncommitted work included, against the commit it started from, as stored in `run_diffs` — one snapshot, no history. Without paths, the files by churn with counts and no lines (paged, 200 by default, at most 1,000); `nameStatus`, path and status only; with paths, those files as unified diff text, at most 2,000 lines a call. Another task's Run is refused. With `task` (a peer's key; `dude diff --task ABS-2 [PATH…]`), that peer's newest implement / fix / simplify Run, a live one first, read from the same stored snapshot with the same paging and bounds; a task that is not a peer is refused as another task's Run is | `run.basics` |
| `message` | text to one peer task (`dude message ABS-2 "…" [--reply-to MSG]`), never a Run, never a broadcast. Never blocks: returns `{id, to, lands: next_step \| next_run, notice?}`. It is a directive (no `interrupt`) on the Run that speaks for the task — its newest live implementer, fixer or investigator; a reply goes to the Run that asked, while live — read at its next step; with none, it is held for the task's next such Run's prompt and `notice` says "ABS-3 has no Run going; it will see this when its next Run starts." A task that is done, failed or aborted is refused ("ABS-3 is done: no Run will read this. …"). At most `SteerTextMax`; at most 5 per Run in any rolling hour, the sixth refused saying when the next is allowed. A reply to one's own message closes one's own question. Refusals are `agent.message.refused` | `coordination` |
| `wait_for_reply` | ends the turn until the reply to one of the caller's own messages (`dude wait MSG`); returns the reply at once if it has come, or the peer-ended line, opening nothing, if the peer's task has ended. One open wait per Run | `coordination` |
| `note` | pins a note on the caller's epic (`dude note "…" [--replaces N]`), numbered, never edited; with `replaces`, a new note in N's place. Returns `{id, n, replaces, told, current}`. At most 2,000 characters and 10 current notes ("This epic has 10 notes. Replace one (`dude note --replaces N`) instead."); a replace never counts. Agents cannot retire | `coordination` |
| `findings` | the caller's task's review findings (`dude findings [ID...]`), open and most severe first, at most 200: each one's id, severity, category, repository, `file:line`, status, who raised it and how it was settled — fixed by which Run, accepted by a person, superseded, or open after so many fix attempts. No text in the list: with `ids` (at most 20) those findings in full — title, description, suggested fix, resolution note — and `unknown` for ids that are not the task's | `delivery.read` |
| `pull_requests` | the caller's task's pull requests (`dude prs`): repository and number, URL, state, head branch and commit, base, checks (the roll-up and each check's status and conclusion), review (the roll-up and each reviewer's word), unresolved threads, and the feedback people left — the latest 50, oldest first, each with its author, kind (comment, line comment, review, changes requested), path, an excerpt of at most 280 characters on one line, whether a fixer Run was sent it (`actedOnBy`, matched on its repository, path, author and words in the Run's `pr_feedback`) and why it woke nobody (`ignored`); `feedbackTotal` counts all of it; and `lastPolledAt`, when dude last read it from GitHub (a webhook, the 15-minute reconciler, or a refresh), so the agent knows how old the rest is. A session's agent's has `lastPolledAt` on each line too | `delivery.read` |
| `refresh_pull_requests` | the caller's task's open pull requests read from GitHub now (`dude prs --refresh`), then listed as `pull_requests` lists them, with `refreshed` and, for any GitHub did not answer, `notRefreshed`. Checked in the call's transaction (open pull requests, the GitHub connection, the hourly reads under their lock) and recorded as one `agent.tool.attempted` per open pull request on GitHub, each one of the hourly GitHub reads (all must fit, or none is synced; a sync's several GitHub requests count as one read); each pull request is then synced through `prs.Syncer.Sync` with no transaction open (Sync opens its own and retries a sync that raced it), and the listing is read in a second transaction. A session's agent names a linked project's task (`task`, optional `project`). A separate tool rather than `pull_requests {refresh: true}`: `pull_requests` stays a plain read in one transaction, and the GitHub-read budget, its `countingAttempts` record and the hourly indexes' WHERE key on a tool name, which an argument would not give | `pr.refresh` |
| `run_chat` | a Run's Chat (`dude chat RUN [--limit N]`), as the web app's Run page shows it: its agent's messages (`agent.message`, blank ones skipped) and what it was told (`chat.message`, `run.steered`, `question.answered`), the latest `limit` (default 30, at most 100), oldest first, with `total` and `more`; each text cut at 8 KiB as the GitHub reads' are, and the whole within their 64 KiB result budget, dropping the oldest messages first. Each message says `from` (`agent`, `conductor`, `person`, `github`), `who`, `kind` (`message`, `steer`, `answer`) and `at`. People's and GitHub's words and names are quoted as external text under one random id, the note first, as the GitHub reads' are; the agent's and the conductor's are not. With it, `openQuestion`: the Run's question waiting for an answer — id, prompt, each question with its choices, `for` (one member by name, or the task's people / the session's members) and `askedAt`. A task's agent reads its own task's Runs only (every attempt, children included); a session's agent only Runs its session started (`source_kind = 'session'`, `parent_session_id`). Any other Run is refused in dude's words, the same as a Run that is not there. The call's ledger event keeps the result's size, not the text | `runs.read` |
| `limits` | the bounds the delivery holds the caller's task to (`dude limits`), each with its `source` (`delivery policy`, `organisation GitHub settings`, `dude`), value, `used` and `remaining`: pull request fix rounds per review round (the policy's `MaxPRFixIterations` frozen when a live delivery started, with `setIn`, the layer today's policy takes it from; when today's differs, `policyNow` carries that value and its `setIn` instead; with no delivery or an ended one, today's policy and nothing spent) and per pull request (the organisation's `fixRoundsPerPr`, each open pull request's fixes spent and `binding`: which of the two stops its next fix first, or `both`); `ciStuckMinutes`; `whenBehind`; failed-check reruns (each open pull request head's one rerun, used or not, and this Run's of `rerunsPerRun`); GitHub reads this hour; a publish's lines and files; this Run's machine moves. Read with the getters the checks call (`delivery.TaskLimitsTx`, `RoundFixesSpent`, `PullRequestFixesSpent`, `GitHubSettingsTx`, `RerunClaimedTx`, `PublishLimitsTx`; `callsMade`, `githubReadsThisHour`, `machineMoves`), so it answers what the next check finds. A bound people raised for the task (`request_limit`) also has `base` (the setting's value), `granted` (each raise: `extra`, `by`, `at`) and `line` ("delivery policy 5 + 3 granted by Márcio for this task = 8"); an ended delivery's fix-round raises are gone with it, its rerun raises on a head stay; with `verify`, `verifiesThisRun`. Read-only | `limits.read` |
| `request_limit` | ask the task's people to raise one of its delivery's limits for this task alone (`dude limit request LIMIT [--to N] [--pr PR] --reason R`): `perReviewRound` (the policy's `MaxPRFixIterations`), `perPullRequest` (the organisation's `fixRoundsPerPr`; refused when 0, no bound), each to `to` (above the limit now, by at most 10), or `rerun`, one more failed-check rerun on an open pull request's head (`pr`, else the task's only open one; refused while its rerun is unspent). It is the conductor's question (the one open ask a Run has; refused while another is open, and while a request for the same limit waits), for the task's people, with the reason and two choices: "Allow N more fix rounds per review round / per pull request for this task" or "Allow one more rerun on repo#n at sha", and "No". The request waits in the delivery's state (`limitRequests`); `limit.requested` records it. **Who approves** is who may change the setting today: the delivery policy and the GitHub settings are an organisation admin's (`requireOrgAdmin`), so a fix-round raise needs an admin; a rerun is a person's action on the pull request, which the task's owner (or an admin) may take. The answer route takes a limit request's answer from the owner or an admin (`ownerOrAdmin`); an approval by anyone who may not raise that limit is a 403 and records nothing. An approval appends a grant to the delivery's `limitGrants` (by, name, extra, the question) and `limit.granted`; No closes the request and grants nothing. The checks read the grants through `State.RoundFixLimit`, `PullRequestFixLimit` and `RerunsAllowed` (`prFixBoundFor`, `chargePRFix`, `CheckPRActionTx`), so they allow exactly what was granted; the setting itself is unchanged | `limits.request` |
| `verify` | have a commit checked without changing it (`dude verify [COMMIT] [--command C]... [--path P]... [--repo R] [--note N]`): a report-only child as `start_agent` starts one — the investigator's phase, `publishCode` false, its spec with no push branch — checking out `commit` (a full SHA; the task branch's head in `repo` when left out, `repo` the task's only one when left out) over the task's heads, with a brief to run `commands` (at most 10, one line each), or the repository's tests for `paths`, or the change's tests, change nothing, and end its reply with `{"verify": [{command, exit, tail}]}`. At settlement that block is read (its last one; at most 20 results, each tail 2,000 bytes) into `child_results.verify` (migration 167), and the parent is told, as its child result, `passed` only when the child completed, reported a result for every command asked (matched by its trimmed text) and every command exited 0, `failed` otherwise (with `no-report`, `incomplete` naming the asked commands with no result, or the child's status), each command's exit code, and the report as JSON with every reported command line and output tail quoted as external text under one random id (the summary names an asked command by the conductor's own text, `asked` in the JSON): the verifier wrote the one and the commands ran the repository's code, which anyone who can push to it wrote. Works at any stage while the delivery is live; changes nothing in it. At most 10 a Run (`verifiesThisRun` in `limits`): there was no child limit to count against | `verify` |
| `start_phase` | the decision the task's delivery waits on (`dude phase start PHASE`): a phase Run from the task's head — implement, review (`categories`, else those the change warrants), fix (`findings`, else every open one; at pull request feedback, the feedback), simplify or test — created by the workflow's own steps, marked with the conductor (`runs.conductor_run_id`) and its `note`. Refused past the policy's review rounds, fix attempts per finding or pull request fix rounds | `conduct`, while deciding |
| `decide` | the decision waited on (`dude decide ACTION`): `next` (what Deliver would do), `ask_person` (a question for the person in `note`; before the pull request the fixed question Open / Draft / Show me the diff / Another round, recorded with the heads it was asked at), `wait` (at pull request feedback) or `open_pull_request` — only when the latest gate question, asked at the task's current heads, was answered Open (or Draft: a draft) | `conduct`, while deciding |
| `dismiss_finding` | an open finding left as it is (`dude finding dismiss ID --reason R`): status accepted, the reason its resolution note | `conduct`, while deciding |
| `decide_escalation` | the escalation the delivery is stopped at (`dude escalation decide ACTION [--note N]`), decided by the conductor with one of its actions — only while the conducted delivery waits at `decide`, undecided, and the latest answered question about **this** escalation (`questions.escalation`) was answered by the task's owner (`answered_by_person`). Goes through `DecideEscalationTx`, as the banner does; `task.decided` records `by: conductor`, the question and who answered, and the note carries the proposal, the answer and the conductor's word. Refused otherwise, pointing to the banner | `conduct`, at an escalation |
| `update_task` | what Chat settled, written into the task (`dude task update`): its goal, its acceptance criteria (the whole list), or both; a `task.updated` event with what they were. Only while no implementer has started on the attempt | `conduct`, while deciding |
| `steer` | a steer for a live phase Run of the caller's task's current attempt (`dude steer RUN TEXT [--interrupt] [--feedback ID]...`), queued as a person's is (`delivery.Steer`): read at the agent's next step, `interrupt` stops its turn. Text only, at most a person's bound (`SteerTextMax`). With `feedbackIds`, people's open feedback items of the current attempt, to an implementer or fixer only: dude appends each item's id, author, anchor and words (`delivery.FeedbackText`), never the conductor's summary, and records them as given to that Run (`runs.feedback_item_ids`). The directive (`conductor_run_id`) and its `run.steered` event (actor the conductor's Run, `by: conductor`) are the conductor's. Returns the directive, the Run and `lands` once lux has said it. Refused for the conductor itself, another task's Run, an earlier attempt's, a branch preview, a Run ended or ending, a done, aborted or failed task, and a conductor superseded (the Chat lock, as decisions take it). Its read wakes the conductor (`steer_read`); its failure too (`steer_failed`, with why) | `conduct`, whoever decides |
| `feedback` | people's feedback on the caller's task's current attempt (`dude feedback [ID...] [--all]`, migration 127): open and delivered items by default, `all` the handled ones too, each with its id, author, anchor (a code range of a Run's stored diff with its excerpt, a preview page, or none), status and fix attempts; with ids (at most 20) those with their history, `unknown` for any other id. A draft is nobody's here: never listed, never found by id | `feedback.handle` |
| `feedback_reply` | answer an item without changing the code (`dude feedback reply ID TEXT`): status `replied`, which holds nothing; the person may reopen it. Refused for an item not open or delivered, another task's, an earlier attempt's | `feedback.handle` |
| `feedback_resolve` | resolve an item with the commit that fixed it (`dude feedback resolve ID --commit HEAD [--repo R]`; the CLI resolves HEAD with `git rev-parse` in the checkout). Only a publishing phase's Run. The commit is tied to the item's anchor's repository, or the named one, or the task's only writable one. The resolution is pending — it still holds the pull request and the merge — until the Run completes: the syncer then asks GitHub whether the commit is in the published head (compare commit...head, `behind_by` 0) and new since the Run's base (compare base...commit, `ahead_by` > 0); otherwise, or if the Run fails or is aborted, the item is open again | `feedback.resolve` |

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

## Capabilities

Which tools an agent has is its **capabilities**: named grants, each of
some dude tools and/or the harness's own web tools, defined in one place
(`orchestrator/internal/capabilities`). `capabilities.Resolve` is the only
place an agent's capabilities are decided; the tool server (its MCP list,
`GET /tools` for `dude tools`, and every call), the instruction file, and the
Run's harness configuration all ask it. A tool the agent is not granted is
not listed, and a call to it is refused as one the Run does not have. A
session agent (brainstorm) has its own definitions of some names
(`tools/session/`): a capability unlocks a name, and a session agent's own
definition answers it where there is one.

| capability | unlocks |
|---|---|
| `tasks.read` | `list_tasks`, `list_epics`, `list_repositories` |
| `tasks.create` | `create_task` |
| `memory` | `search_memory`, `get_memory`, `remember` |
| `run.basics` | `emit_event`, `run_diff`, `request_repository` |
| `ask` | `ask_person` |
| `machines` | `list_machine_sizes`, `request_machine` |
| `coordination` | `message`, `wait_for_reply`, `note` |
| `feedback.handle` | `feedback`, `feedback_reply` |
| `feedback.resolve` | `feedback_resolve` |
| `pr.act` | `reply_on_pull_request`, `resolve_thread` |
| `base.sync` | `sync_base` |
| `github.read` | `github_search`, `github_issue`, `github_pull_request`, `github_check_log` |
| `delivery.read` | `findings`, `pull_requests` |
| `conduct` | `start_phase`, `start_agent`, `steer`, `restart_run`, `decide`, `dismiss_finding`, `decide_escalation`, `update_task`, `publish`, `request_review`, `mark_ready`, `rerun_failed_checks` |
| `session` | `propose`, `relate`, `name_session` |
| `runs.read` | `run_chat` |
| `pr.refresh` | `refresh_pull_requests` |
| `limits.read` | `limits` |
| `limits.request` | `request_limit` |
| `verify` | `verify` |
| `web_search` | the harness's web search: OpenCode's `websearch` (`OPENCODE_ENABLE_EXA=1` and `mcp.exa.ai` on the egress list), Claude Code's `WebSearch`, Codex's `web_search` (pinned `live`) |
| `web_fetch` | the harness's web fetch: OpenCode's `webfetch`, Claude Code's `WebFetch` (Codex has none) |

Not granted, a harness's web tool is off its own way: OpenCode has no
`OPENCODE_ENABLE_EXA` and no `mcp.exa.ai` host, and `webfetch` set to
`deny` in its permission config; Claude Code names it in
`--disallowedTools`; Codex's `web_search` is `disabled`, which a tier's
`-c web_search` cannot override.

Default grants (a fixer's Run has role `implementer`; `""` is a Run whose
role none of these is):

| role | capabilities |
|---|---|
| conductor | `tasks.read`, `tasks.create`, `memory`, `run.basics`, `ask`, `machines`, `coordination`, `feedback.handle`, `pr.act`, `base.sync`, `github.read`, `delivery.read`, `conduct`, `runs.read`, `pr.refresh`, `limits.read`, `limits.request`, `verify`, `web_search`, `web_fetch` |
| brainstorm | `tasks.read`, `memory`, `ask`, `machines`, `github.read`, `delivery.read`, `session`, `runs.read`, `pr.refresh`, `web_search`, `web_fetch` |
| implementer | `tasks.read`, `tasks.create`, `memory`, `run.basics`, `ask`, `machines`, `coordination`, `feedback.handle`, `feedback.resolve`, `pr.act`, `base.sync`, `web_search`, `web_fetch` |
| reviewer, fixer, qa_browser | `tasks.read`, `memory`, `run.basics`, `machines`, `web_search`, `web_fetch` |
| simplifier | `tasks.read`, `memory`, `run.basics`, `machines`, `base.sync`, `web_search`, `web_fetch` |
| investigator | `tasks.read`, `tasks.create`, `memory`, `run.basics`, `ask`, `machines`, `coordination`, `web_search`, `web_fetch` |
| `""` | `tasks.read`, `memory`, `run.basics`, `web_search`, `web_fetch` |

A later settings UI will override these per organisation and per agent,
stored in the database and applied inside `capabilities.Resolve`.

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

Brainstorm and conductor may use `github_check_log` with a check id from
`github_pull_request` to read a failed Actions check's log before choosing a
rerun or a fix; its bounded tail, or the check-run output for other checks, is
marked as external text.

- Each phase Run gets its own **bearer token**, minted at submit, stored
  hashed on the Run, sent to lux as a secret (`headers: [{name:
  Authorization, secret: DUDE_MCP_TOKEN}]`), revoked when the Run ends.
  The token names the Run; the Run names the organization, project, work
  item and role. A tool call can do nothing outside that project.
- **Which tools a role gets** is policy (project settings, defaults above);
  the server lists only those to that Run.
- Every call is a ledger event (`agent.tool.dude`) with its arguments and
  result, so the chat shows it like any other tool call.
- The pull request actions (`request_review`, `resolve_thread`,
  `mark_ready`, `rerun_failed_checks`) also write `agent.tool.attempted`
  once a call passes dude's own checks and before GitHub is called. Their
  per-Run and per-minute budgets count that attempt, so a call GitHub
  refuses still spends them. The successful call's `agent.tool.dude` is
  marked `attempted` and is not counted a second time.

## Child sessions

A Run's agent may start a **child**: a subagent with a conversation of its
own (OpenCode's and Claude Code's task tool, Codex's `spawn_agent`). A
child is not a Run: it shares the Run's container, checkout, proxy key and
time limit, and dude neither starts nor steers it. dude records what it
does, nested under the parent's tool call that started it, so the
transcript shows it and the stall detector can tell a working child from a
hung call.

### Events

Every payload carries `childId` (the harness's id: OpenCode's session,
Codex's thread, Claude Code's task) and `callId` (the parent's
`agent.tool.called` that started it). A child of a child carries
`parentChildId`.

| event | payload | when |
|---|---|---|
| `agent.child.started` | `{childId, callId, parentChildId?, kind, title, agentType?, model?}` — `kind` `task` (OpenCode, Claude Code) or `spawn` (Codex); `title` the description the parent gave | the child exists |
| `agent.child.tool.called` | as `agent.tool.called` (`tool`, `callId` — the child's own call —, `input`, `title`) plus `childId`, `parentCallId` | a child's tool call starts |
| `agent.child.tool.completed` | as `agent.tool.completed` (`tool`, `callId`, `status`, the result) plus `childId`, `parentCallId` | it ends |
| `agent.child.message` | `{childId, callId, text, costUsd?, tokens?}` | a child's assistant message completes |
| `agent.child.waiting` | `{childId, callId, on: "permission", permission, patterns, answered, held, childCallId?}` — `held` false when lux answered it or the call it was for is already over | the harness asked a permission for the child; that child's next activity ends the wait |
| `agent.child.ended` | `{childId, callId, status, summary, costUsd?, tokens?, durationSecs}` — status `completed`, `failed` or `interrupted` | the child is done |

Only completed things are recorded: no deltas, no reasoning, one
`agent.child.message` per completed message. Inputs, outputs, texts and
summaries are cut as the parent's are (a message and a summary at 2 KB). A
child's `costUsd` is what its harness said; the Run's cost
(`run.cost.reported`, from the proxy) already includes it, and nothing adds
it again.

### Where each harness says it

- **Claude Code** (`claude.*`): the parent's `Agent` (or `Task`) tool_use
  is the call. `system task_started` names the child (`task_id`) and the
  call (`tool_use_id`); every child line carries `parent_tool_use_id`,
  the call's id, and is translated as the child's, never the parent's.
  A child line that arrives before any `task_started` for its call starts
  the child itself: its `childId` is then the `parent_tool_use_id`, its
  `title` and `agentType` the line's `task_description` and
  `subagent_type`, and a `task_started` after it keeps that id.
  `system task_notification` ends it with its status, summary and usage;
  a `task_progress` after it is ignored.
- **Codex** (`codex.*`): any notification on a thread other than the
  Run's (its session) is a child's. The parent's `collabAgentToolCall`
  item (`spawnAgent`) is the call; the `subAgentActivity` item that
  follows it (`kind: started`) ties the child's `agentThreadId` to it.
  Codex 0.144.1 sends no item for the spawn itself, only the
  `subAgentActivity` under the spawn call's id: the call is then recorded
  from it, as a `task` already done. A spawned agent outlives its spawn
  call; while it works, the parent's open `wait` calls count as busy. The
  child's `turn/completed` ends it; its `thread/tokenUsage/updated` are its
  tokens, not the Run's.
- **OpenCode** (`acp.*` is the parent alone; lux relays the child's bus
  events as `opencode.*`): `opencode.session` with a `parentID` is a
  child; `opencode.task` ties its session to the parent's call
  (`callID`); `opencode.part` is a child's tool call, `opencode.message` a
  completed message, `opencode.permission` an ask on its session. ACP
  reports the task tool with kind `think` and the description as title:
  a `think` call whose input names a `subagent_type` is recorded as tool
  `task`. A part's `input` is its JSON cut at 300 bytes: recorded as the
  object when it still parses, else as the string, with `truncated`. A
  task's `model` is OpenCode's `{modelID, providerID}`, recorded as
  `providerID/modelID`. A message's `tokens` are lux's flattened
  `{input, output, reasoning, cacheRead, cacheWrite}`. lux answers a
  child's ask itself (`answered: allow`, with a `lux.warning`); one it
  could not answer (`answered: none`) is relayed with a
  `lux.warning`, after child records that may already follow it. An ask
  is held only while nothing answered it and the call it names is still
  open. An ask for a call already completed (or of a child already
  ended) is recorded with `held: false` and changes nothing: no wait is
  set in `runs.children`, an earlier wait stays as it was, and the
  transcript does not show the thread as waiting. A held wait ends at the
  child's next activity: a `pending` part, a new call, a call's end, a
  message, or the child's end. `opencode.session`, a `running`
  `opencode.task`, and a repeated `running` part for a call already
  started do not end it.

### The stall detector

`runs.children` (migration 111) keeps, per open parent call, its child's
id, kind, title, last event time and a one-line last event, and what it
waits on. An open call is measured from the later of its opening and its
child's last event, so a call whose child works is not stalled; a child's
activity counts as the agent's (`agent_active_at`). A report names each
call's child, how long ago it last did something and what, and a
permission it waits on that nothing can answer.

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
