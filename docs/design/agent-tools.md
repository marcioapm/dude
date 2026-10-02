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
| `list_tasks` | the project's epics and tasks, with status and keys | all |
| `search_memory` | search the project's history: tasks, findings, artifacts' text, PR titles (Postgres full-text first; embeddings later) | all |
| `ask_person` | a question for a person; the turn ends and the answer is the next input. The only way an agent asks: the question block is gone. A conductor's is answered in its task's Chat | implementer (and so the fixer), investigator, conductor |
| `publish_artifact` | write a file for people (name, content) — same as `$LUX_ARTIFACTS`, for agents that prefer a tool | all |
| `request_repository` | ask for another repository of the organization, read or write, with a reason; a person approves or denies (#45) | all |
| `run_diff` | what a Run of the caller's task changed (`dude diff`): its checkout, uncommitted work included, against the commit it started from, as stored in `run_diffs` — one snapshot, no history. Without paths, the files by churn with counts and no lines (paged, 200 by default, at most 1,000); `nameStatus`, path and status only; with paths, those files as unified diff text, at most 2,000 lines a call. Another task's Run is refused | all |
| `findings` | the caller's task's review findings (`dude findings [ID...]`), open and most severe first, at most 200: each one's id, severity, category, repository, `file:line`, status, who raised it and how it was settled — fixed by which Run, accepted by a person, superseded, or open after so many fix attempts. No text in the list: with `ids` (at most 20) those findings in full — title, description, suggested fix, resolution note — and `unknown` for ids that are not the task's | conductor |
| `pull_requests` | the caller's task's pull requests (`dude prs`): repository and number, URL, state, head branch and commit, base, checks (the roll-up and each check's status and conclusion), review (the roll-up and each reviewer's word), unresolved threads, and the feedback people left — the latest 50, oldest first, each with its author, kind (comment, line comment, review, changes requested), path, an excerpt of at most 280 characters on one line, whether a fixer Run was sent it (`actedOnBy`, matched on its author and words in the Run's `pr_feedback`) and why it woke nobody (`ignored`); `feedbackTotal` counts all of it | conductor |

A task's **conductor** (the agent people talk to in its Chat) reads with
these and changes nothing: it has no tool that edits, and its checkout is
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
- **No wall-clock limit.** Runs have none; agents work for days. dude sends
  no `timeout`, which lux takes as no limit (lux d748aa5). An operator can
  set one (`DUDE_AGENT_TIMEOUT`); lux counts only time spent running, so
  parked time never counts. lux keeps a stopped Run's state until it is
  cancelled.
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
