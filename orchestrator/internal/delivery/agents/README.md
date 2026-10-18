# What dude tells agents

The catalogue contains the migrated agent-facing text. Some fixed protocol identifiers and
labels remain in Go; they are listed below. Except as listed below, Go decides only which
sections a Run gets and fills in the data.

| Directory | What it holds |
| --- | --- |
| `roles/<role>.md` | A role's built-in instructions: what an organisation's saved prompt replaces and a project's adds to or replaces, and what the settings page shows as dude's (`BuiltinPrompt`). Plain text, not a template. Paragraphs are separated by one blank line and stay separate: the fixer's first paragraph goes before the feedback it is given and the rest after it. |
| `contracts/` | The framing every Run gets whatever its instructions say, because the workflow relies on it: how a reviewer reviews and reports, how a fixer fixes, the tester's report, committing, merging the base, the task, findings, pull request and people's feedback, tools, CLI, publishing, the workspace and machine notes. `review-focus/<category>.md` is each review category's focus; `review-range/<of>.md` why a review covers only its range. `images.md` is what replaces an image reference in a text an agent reads, and `feedback-page-line.md` where on its page a preview item was made. |
| `briefings/` | What the conductor, a brainstorm and a Run on an epic are told about where they stand: the conductor's briefing and its digest, the session briefing, the epic section, the messages held for a task, the integration fixer's and reviewer's briefs (`integration-*`), and the replay of a conversation (`replay.md`, and `replay-lines.md`, one `{{define}}` per line an event renders as). |
| `tools/<tool>.md` | Each dude tool's description, as the MCP server and `dude tools` list it, then one `{{define "tools/<tool>/<parameter>"}}` per parameter description (`questions.choices.label` for a field of a list's items). A session agent's tools, which share names with the task agents', are `tools/session/<tool>.md`. `tools/common/` holds what several tools share and the MCP server's instructions. The tool's name, its parameters' names and its schema stay in Go: `agenttools.define` reads the text by the tool's name, and a description naming no parameter of the tool stops the orchestrator at startup. |
| `results/` | What a tool answers when it succeeds: `next` and its other words (`steer.md`, `ask-person.md`, `sync-base.md`, `machine-moved.md`…), GitHub reads' notes and refusals (`github.md`), the machine tools' own words (`machines.md`) and lux's refusals of a move (`machine-refused.md`), and the gate question's choices (`gate-choices.md`, one a line, Open and Draft first). |
| `refusals/` | What a tool refuses, as Go `printf` formats: one `{{define}}` per refusal, in a file named after the Go file that refuses (`tools/` for `agenttools`, `delivery/` for the delivery's `refusef`, `domain/` for the feedback, steer, escalation, attachment, message, note and relation refusals, which a person's API call can meet too). Go fills the verbs (`%s`, `%d`, `%q`) in the order they appear; keep them. `words.md` holds the publish refusals and the shared words refusals are built of (a blocker's state, a read-only checkout's cause, how a person decides an escalation). `http.md` is what the tool server's HTTP layer answers a client it cannot serve. |
| `wakes/` | The reasons dude wakes the conductor with, one line each, and the note they are delivered in (`note.md`); `facts.md` holds the facts a decision's reason carries (a Run's outcome, a review round, a checkout's sync, a test's verdict, people's open feedback) and the decision points' names. |
| `notices/` | What dude tells a Run outside its prompt: a peer's message and its notices, epic note changes, a nudge (`idle`, `resumed`, `stuck-turn`, `host-moved`), a machine move's words, a repository linked or failing to check out, and in `told.md` a person's answer, a restart, a changed task, a mention on GitHub, the external-text notes, Talk it through and a session's new owner. `start-how.md` names each way to start a task as its button does. |
| `testdata/` | The golden files: migrated prompt, briefing, tool, result, refusal, wake and notice text. Refusal goldens pin the generated `printf` formats; the generated Go constants restore compile-time format checking at call sites. The remaining Go-side protocol identifiers and unreachable legacy text are not claimed to be catalogue entries. |

Where each is used: `prompts.go` (`Prompt`, `ConductorPrompt`),
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
