# What dude tells agents

The catalogue contains the migrated agent-facing text. Some fixed protocol identifiers and
labels remain in Go; they are listed below. Except as listed below, Go decides only which
sections a Run gets and fills in the data.

| Directory | What it holds |
| --- | --- |
| `instructions/` | Each Run's instruction file (`Instructions`): `charter.md` (How you work), `dude.md` (How dude works), `roles/<role>.md`, a reviewer's aspect (`aspects/reviewer/`), then `saved.md` (the saved prompts) and `notes.md` (the project's notes for the role). A role file is split at init (`parseRole`) into its title and purpose, its one `## How you work as <role>` section (the stance: the only part a saved prompt replaces, and what `BuiltinPrompt` and the settings page show as dude's) and every later section, which no saved prompt removes. |
| `contracts/` | Pieces of the instruction files (included with `{{template}}`) and of first messages, because the workflow relies on it: how a reviewer reviews and reports, how a fixer fixes, the tester's report, committing, merging the base, the task, findings, pull request and people's feedback, tools, CLI, publishing, the workspace and machine notes. `review-range/<of>.md` why a review covers only its range. `images.md` is what replaces an image reference in a text an agent reads, and `feedback-page-line.md` where on its page a preview item was made. |
| `briefings/` | What the conductor, a brainstorm and a Run on an epic are told about where they stand: the conductor's briefing and its digest, the session briefing, the epic section, the messages held for a task, the integration fixer's and reviewer's briefs (`integration-*`), and the replay of a conversation (`replay.md`, and `replay-lines.md`, one `{{define}}` per line an event renders as). |
| `tools/<tool>.md` | Each dude tool's description, as the MCP server and `dude tools` list it, then one `{{define "tools/<tool>/<parameter>"}}` per parameter description (`questions.choices.label` for a field of a list's items). A session agent's tools, which share names with the task agents', are `tools/session/<tool>.md`. `tools/common/` holds what several tools share and the MCP server's instructions. The tool's name, its parameters' names and its schema stay in Go: `agenttools.define` reads the text by the tool's name, and a description naming no parameter of the tool stops the orchestrator at startup. |
| `results/` | What a tool answers when it succeeds: `next` and its other words (`steer.md`, `ask-person.md`, `sync-base.md`, `machine-moved.md`…), GitHub reads' notes and refusals (`github.md`), the machine tools' own words (`machines.md`) and lux's refusals of a move (`machine-refused.md`), and the gate question's choices (`gate-choices.md`, one a line, Open and Draft first). |
| `refusals/` | What a tool refuses, as Go `printf` formats: one `{{define}}` per refusal, in a file named after the Go file that refuses (`tools/` for `agenttools`, `delivery/` for the delivery's `refusef`, `domain/` for the feedback, steer, escalation, attachment, message, note and relation refusals, which a person's API call can meet too). Go fills the verbs (`%s`, `%d`, `%q`) in the order they appear; keep them. `words.md` holds the publish refusals and the shared words refusals are built of (a blocker's state, a read-only checkout's cause, how a person decides an escalation). `http.md` is what the tool server's HTTP layer answers a client it cannot serve. |
| `wakes/` | The reasons dude wakes the conductor with, one line each, and the note they are delivered in (`note.md`); `facts.md` holds the facts a decision's reason carries (a Run's outcome, a review round, a checkout's sync, a test's verdict, people's open feedback) and the decision points' names. |
| `notices/` | What dude tells a Run outside its prompt: a peer's message and its notices, epic note changes, a nudge (`idle`, `resumed`, `stuck-turn`, `host-moved`), a machine move's words, a repository linked or failing to check out, and in `told.md` a person's answer, a restart, a changed task, a mention on GitHub, the external-text notes, Talk it through and a session's new owner. `start-how.md` names each way to start a task as its button does. |
| `testdata/` | The golden files: migrated prompt, briefing, tool, result, refusal, wake and notice text. Refusal goldens pin the generated `printf` formats; the generated Go constants restore compile-time format checking at call sites. The remaining Go-side protocol identifiers and unreachable legacy text are not claimed to be catalogue entries. |

Where each is used: `instructions.go` (`Instructions`, `BuiltinPrompt`), `prompts.go` (`Prompt`, `ConductorPrompt`),
`session.go` (`BrainstormPrompt`, the session briefing), `briefing.go`,
`coordination_brief.go`, `adopted_prompt.go`, `feedback.go`
(`FeedbackText`, the conductor's feedback wake line), `machine_choices.go`,
`conduct.go` (`AcceptedBy`, what the conductor's decision tools answer),
`agents_facts.go`, `refusals.go` and `refusals_domain.go` (the handles of
`wakes/facts`, `notices/told` and the refusals), `replay.go`, `wakes.go`
and the files that record each wake. Other packages render through
`MustText` (a handle resolved at init), `ToolText` and `ToolParameter`:
`agenttools` (descriptions, results, refusals, GitHub words, HTTP error
bodies), `phases` (nudges, move and checkout notices, a read-only checkout's
cause) and `api` (a session's handover line).

The generator keeps a small manifest of existing Go handle names and Markdown keys. Regenerate
with `go -C orchestrator generate ./internal/refusalgen`; the generated files are marked and
must not be edited. `internal/refusalgen`'s ordinary test compares rendered bytes with the
checked-in files, so stale generated files fail even if text goldens are updated. The generator
is deliberately independent of delivery and does not duplicate call-site argument types.
A new refusal is a `{{define}}` plus one line in `internal/refusalgen/<dir>.manifest`: a define
under `refusals/tools/`, `refusals/delivery/` or `refusals/domain/` that no manifest lists fails
the generator, and so does a manifest line whose define is missing.

Known text that still reaches an agent from Go (paths under `orchestrator/internal/`; this inventory is not exhaustive):

- `delivery/conduct.go:623` — `DismissedByConductor`, the resolution-note prefix of a finding
  the conductor dismissed; `AcceptedBy` (`:628`) parses it back, so it stays a Go constant.
- `agenttools/server.go:198`, `:203`, `:213` — the JSON API's own error bodies ("no tool <name>
  for this Run", "request too large", "internal error"), not yet in Markdown.

JSON enum and state identifiers remain Go data; where one selects agent text, the text is in
Markdown keyed by it:

- `delivery/relations.go:49-50` — the task statuses a blocker can have, each the key of its
  `refusals/words/blocker-state/<status>` word; another status is written with its `_` as spaces.
- `delivery/start.go:21-22` — `HowDeliver`, `HowTalk`, the keys of `notices/start-how/<how>`.
- `delivery/session.go:29-30` and `api/sessions.go:1648`, `:1681` — a handover's `keep`
  (`chat`, `read`, `leave`), which picks the clause of `notices/told/session-owner`.

`wakes/facts/checkout-kept-as-is` is a wake fact in Markdown, used through `checkoutKeptAsIs`
(`delivery/agents_facts.go:10`). Its only caller, `delivery/edits.go:740`, cannot be reached:
a `kept` checkout with nothing dirty, diverged or ahead already returns as `off-branch` at
`:727-731`. No golden pins it.

## Editing

- A file's text is everything in it but its one trailing newline. Keep
  that newline; add no other blank line at the end, or the agent gets it.
- Every directory but `roles/` holds Go
  [`text/template`](https://pkg.go.dev/text/template) files, named by their
  path without `.md` (`contracts/commit`). A file that keys several texts
  (`refusals/`, `results/github.md`, `briefings/replay-lines.md`) holds
  one `{{define "<its name>/<key>"}}` each, and no words outside them.
  One includes another with `{{template "contracts/machine-kept"}}`. The data each is given is the
  struct its caller in Go passes to `render`. `{{-` and `-}}` trim the
  whitespace around an action: most multi-branch templates use them so
  each branch can sit on its own line.
- The functions a template may call are in `agents.go` (`agentFuncs`):
  `oneLine`, `quote`, `quoteLines`, `clip`, `criteria`, `short`, `join`,
  `fenced`, `add1`. They format data; they hold no words.
- A saved prompt's `{{task.title}}`, `{{task.goal}}`, `{{task.criteria}}`,
  `{{run.branch}}` and `{{run.base_ref}}` are a different mechanism
  (`fill` in `prompts.go`) applied to an organisation's or project's text,
  never to these files.
- Every file is parsed when the orchestrator starts, and every template
  name the Go code renders or a file includes with `{{template}}` is
  looked up then (`mustTemplate`, `MustText` and the handles in
  `agents.go`, `agents_facts.go` and the refusal files). A `{{define}}` the
  code names that its file lacks panics naming the file. A file that is
  missing, empty or does not parse stops the orchestrator at startup, and
  `go test ./internal/delivery` fails.

## Golden files

`testdata/<case>.golden.md` is each text exactly as an agent receives it,
rendered by `golden_test.go`, `golden_notices_test.go` and
`golden_lines_test.go` and `golden_labels_test.go` (no database) and
`golden_db_test.go` and `golden_db_labels_test.go` (seeded through
`dbtest`); the tools' (`tool-*`, `results-*`) by `agenttools`'
`golden_*_test.go`, the syncer's nudges (`notices-run`) and a read-only
checkout's cause (`read-only-cause`) by `phases`' `golden_*_test.go`, and
a session's handover line (`session-handover`) by `api`'s
`golden_session_handover_test.go`. Any change to a file here shows up there as a
readable diff. After editing, regenerate and review
that diff:

```bash
go test -C orchestrator ./internal/delivery ./internal/agenttools ./internal/phases ./internal/api -run Golden -update
git -C orchestrator diff internal/delivery/agents/testdata
```

For refusal constants, regenerate first with `go -C orchestrator generate ./internal/refusalgen`.

Without `-update` the test fails on any byte that differs and names the
first line.

The briefing goldens (`briefing-*`, `epic-*`, `digest-*`,
`brainstorm-question-withdrawn`, the `*-with-briefing*` prompts,
`results-machines`, `results-server-http`, `auto-started-notice`,
`decide-escalation-refusals`, `read-only-cause` and `session-handover`) render over a Postgres database: they need `DUDE_TEST_PG=host:port` (default
`localhost:5433`, user and password `dude`) and `bun` on the `PATH`, which
runs the migrations. Without a database those tests are skipped, and
`go test -v` prints `SKIP … no test database` for each: it means they were
not checked. With `-update` and no database they fail instead.

## How a Run gets its instructions

- **First message, task only.** `Prompt`, `ConductorPrompt` and `BrainstormPrompt` carry the task and
  this Run's data: findings, feedback, the review range, checkouts, the conductor's note, a restart's
  note, the epic briefing, the machine line. Wakes and briefings stay messages.
- **Instruction file, pinned per Run.** `phases.buildSpec` (through `runSpec`) puts
  `Instructions(...)` into the spec's `init.script`, computed once at submit from the prompt versions
  `LoadPrompts` records on the Run (`runs.prompt_version_id`, `project_prompt_version_id`). The script
  (`phases/instructions.go`) is POSIX `sh`, `set -eu`, a quoted here-document with a delimiter derived
  from the text's SHA-256 (refused if the text holds it as a line), written to `/workspace/AGENTS.md.tmp`
  then renamed; `CLAUDE.md` is written the same way and contains exactly `@AGENTS.md`. Agents start in
  `/workspace`. lux runs init before the workload on every placement from the spec it keeps, so every
  placement of a Run, moves and resumes included, gets the same bytes; a new Run gets the current text.
  An init over 256 KiB is refused at submit.
- **Network snapshot, task Runs.** After the spec resolves its final network,
  `runSpec` prepends the fixed `instructions/network` section from that exact
  policy, including required model, harness and tool hosts. It is outside saved
  role replacements. The bounded, externally quoted list never contains endpoint
  URLs or credentials. A resume sends no replacement init; old Runs retain their
  original bytes, including Runs submitted before this section existed. Session
  agents retain their separate web-reach contract and do not acquire project access.
- **Not a secret.** lux registers every secret value with its redactor and would blank the
  instructions wherever an agent quotes them; the file goes in `init`, never `secrets`.

## What a saved prompt can and cannot change

An organisation's prompt replaces the role's stance (`## How you work as <role>`); a project's adds
after it (`add`) or replaces both it and the organisation's (`replace`). Saved text appears under
`# This organisation` / `# This project`. No saved prompt removes How you work, How dude works, the
role's other sections, a reviewer's aspect, what the role hands back, or the project's notes.

The `checks:` block the implementer, fixer and simplifier are asked to end with is enforced before their work is published: dude reads the last block, rejects a missing or malformed one, and has the organisation's judgement tier compare reported passes with the commands the Run recorded. A self-reported `fail` or `not-run` that no person's attempt waiver covers is asked about too (`notices/checks/preexisting`): it passes as `pre-existing` when the Run's calls show the same command failing the same way at the commit of the target branch the task builds on (GitHub's merge base of that branch and the Run's `runs.base_shas`, given to the judge in `notices/checks/context`; with none, the failure is rejected unasked), or as `deferred` when it is a browser or manual check and the attempt's policy runs a test phase; with no judgement tier it is rejected. Unsupported work is sent back within the configured bound, then waits for a person's decision. When no judgement tier is configured, dude publishes and visibly says that checks were not model-checked. A turn that leaves every repository the Run may push at its starting commit, with nothing staged, unstaged or untracked, as read through lux at the turn's end, needs no report: it is recorded as `unchanged` and named "no changes; checks not needed", never as backed. A tester is given its attempt's writers' recorded check outcomes (`contracts/writer-checks`).
