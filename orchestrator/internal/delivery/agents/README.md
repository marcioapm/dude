# What dude tells agents

Every word dude sends an agent in a prompt or a briefing is in these files,
compiled into the orchestrator with `go:embed`. Edit them here; Go decides
only which sections a Run gets and fills in the data.

| Directory | What it holds |
| --- | --- |
| `roles/<role>.md` | A role's built-in instructions: what an organisation's saved prompt replaces and a project's adds to or replaces, and what the settings page shows as dude's (`BuiltinPrompt`). Plain text, not a template. Paragraphs are separated by one blank line and stay separate: the fixer's first paragraph goes before the feedback it is given and the rest after it. |
| `contracts/` | The framing every Run gets whatever its instructions say, because the workflow relies on it: how a reviewer reviews and reports, how a fixer fixes, the tester's report, committing, merging the base, the task, findings, pull request and people's feedback, tools, CLI, publishing, the workspace and machine notes. `review-focus/<category>.md` is each review category's focus; `review-range/<of>.md` why a review covers only its range. |
| `briefings/` | What the conductor, a brainstorm and a Run on an epic are told about where they stand: the conductor's briefing and its digest, the session briefing, the epic section and the messages held for a task. |
| `testdata/` | The golden files: every prompt and briefing as an agent receives it. |

Where each is used: `prompts.go` (`Prompt`, `ConductorPrompt`),
`session.go` (`BrainstormPrompt`, the session briefing), `briefing.go`,
`coordination_brief.go`, `adopted_prompt.go`, `feedback.go`
(`FeedbackText`, the conductor's feedback wake line), `machine_choices.go`,
`conduct.go` (`AcceptedBy`, what the conductor's decision tools answer).

Some reach agents outside a prompt too: `contracts/machine-*.md` through
the machine tools' descriptions and refusals (`agenttools/machines.go`),
`contracts/feedback.md` through steer messages and directives
(`conduct_steer.go`, `feedback.go`), and `briefings/finding-accepted.md`
through the conductor's `findings` tool (`agenttools/conductor.go`).

Not every sentence an agent receives is here yet. These are still Go
literals, and moving them is a follow-up:

- `agenttools/tools.go:500-503`, the `ask_person` result: the same "End
  your turn now. The person's answer will be your next message." as
  `contracts/conductor-asked.md`, in a second copy.
- `prwait.go:105`, the wait-on-pull-request result.
- `escalations.go:256-260`, the wake line telling the conductor an
  escalation was decided, and `:494`, the `decide_escalation` result.
- `agenttools/conductor.go:333`, `:369` and `:437`, the steer, restart and
  publish results.
- `agenttools/brainstorm.go:720-722`, a brainstorm question's result.
- `conduct.go:367`, `GateChoices`, the gate question's answers in the
  conductor's `ask` result.

## Editing

- A file's text is everything in it but its one trailing newline. Keep
  that newline; add no other blank line at the end, or the agent gets it.
- `contracts/` and `briefings/` are Go
  [`text/template`](https://pkg.go.dev/text/template) files, named by their
  path without `.md` (`contracts/commit`). One includes another with
  `{{template "contracts/machine-kept"}}`. The data each is given is the
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
  looked up then (`mustTemplate` and the handles in `agents.go`). A file
  that is missing, empty or does not parse stops the orchestrator at
  startup, and `go test ./internal/delivery` fails.

## Golden files

`testdata/<case>.golden.md` is each prompt and briefing exactly as an agent
receives it, rendered by `golden_test.go` (no database) and
`golden_db_test.go` (seeded through `dbtest`). Any change to a file here
shows up there as a readable diff. After editing, regenerate and review
that diff:

```bash
cd orchestrator
go test ./internal/delivery -run Golden -update
git diff internal/delivery/agents/testdata
```

Without `-update` the test fails on any byte that differs and names the
first line.

The briefing goldens (`briefing-*`, `epic-*`, `digest-*`,
`brainstorm-question-withdrawn` and the `*-with-briefing*` prompts) render
over a Postgres database: they need `DUDE_TEST_PG=host:port` (default
`localhost:5433`, user and password `dude`) and `bun` on the `PATH`, which
runs the migrations. Without a database those tests are skipped, and
`go test -v` prints `SKIP … no test database` for each: it means they were
not checked. With `-update` and no database they fail instead.
