# Real-model runs

What happened when real models ran dude's delivery loop end to end: the
first time the loop ran on anything other than the scripted agent. Rerun
this after any change to prompts, the findings parser or the policy, and add
a section.

## 2026-09-23: OpenCode on lux, three work items

**Setup.** dude's orchestrator and backend on this machine, on a detached
lux (`run_tests.py --serve`) with two hosts, and the `dude-runtime:dev`
image. OpenCode through llmproxy: implementer and simplifier
`claude-sonnet-5`, reviewer `claude-opus-5`, one `correctness` reviewer.
Project context: "Python 3.11. Run the tests with `python3 -m pytest -q`."
Repository: `marcioapm/dude-factory-scratch`, a small Python package
(`textkit`: word counting, slugs, wrapping). Default policy: `blocking` and
`high` findings start a fix; lower ones go into the PR. GitHub could not
reach the local backend's webhook, so PR activity came through the
reconciler, set to every 2 minutes for the runs.

| Work item | Runs | Fix loop | Findings | Agent time | PR |
| --- | --- | --- | --- | --- | --- |
| 1. Add `reading_time` (function and tests) | implement, review, simplify* | none needed | 2 low, 2 note | 1m 27s | #3 |
| 2. Fix a planted off-by-one in `wrap` | implement, review, simplify, PR fix | none needed; one PR comment fixed | 1 low | 1m 20s | #4 |
| 3. `stats()` and a `python -m textkit stats` CLI | implement, review, simplify | **should have run, did not** | 1 medium, 1 low, 1 note | 4m 39s | #5 |
| 3, rerun with the prompt fix | implement, review, fix, review, simplify, PR fix | 1 iteration, converged | 1 high + 1 medium fixed; 1 low, 1 note open | 6m 47s | #6 |

\* the first simplify was lost with a lux environment taken down under it;
see below.

Wall-clock time from delivery to open PR was the agent time plus a few
seconds per phase: lux started every container within 1–3 seconds.

**Cost: unknown.** OpenCode reports `usage_update` with a cost of 0 for
llmproxy models, because the provider config gives them no prices, so every
Run's `agent_cost_usd` is 0. Context size is reported (`used`), tokens per
request are not. Budgets as loop bounds need a cost source: prices in the
provider config, or llmproxy's own accounting.

### What worked

- **The implementer does the job.** Every implement Run read the code,
  reused what was there (`count_words`), wrote tests, ran them, and committed
  with a sensible message. It found the planted bug in `wrap` from the task
  description alone, in 29 seconds, and added boundary tests that fail on the
  old code.
- **The reviewer is good, and checks its claims.** Opus ran the code
  before reporting: it wrote throwaway scripts for edge cases (`wpm=0`,
  undecodable input), and for item 2 it ran the new tests against the
  pre-fix code to show which of them actually catch the bug. Its findings
  were specific and correct: a UTF-8 file crashing the CLI with a
  traceback, a sentence counter counting punctuation-only fragments, a test
  asserting `>= 1` where `== 1` was meant.
- **Findings parsed every time.** Four reviews, 13 findings, all in the
  requested YAML, all recorded with file and line. Nothing needed the
  parser's tolerance.
- **The loop converged.** On the rerun of item 3, one fix Run addressed
  both findings, the re-review found nothing blocking, and the PR opened.
  Both findings were retired as "file rewritten" by the re-review rule.
- **PR comments reach a fixer.** A comment on the PR, seen by the
  reconciler, started a fix Run that did what was asked and pushed to the
  PR branch. No force-push; the per-Run branches were cleaned up.
- **The simplifier shows restraint.** On a one-line fix it changed nothing
  and said why; on item 3 it removed a regex that had been duplicated
  between two modules.

### What went wrong, and what changed

1. **A reviewer's `medium` finding was an unmet acceptance criterion, and
   the PR opened anyway.** Item 3 asked for "a missing file exits 1 with a
   message, not a traceback"; the reviewer showed an undecodable file giving
   a traceback, and called it `medium`. Only `blocking` and `high` start a
   fix, so it went into the PR as an open finding. The reviewer did not
   know what its severity would cause. **Fix:** the review prompt now says
   so, from the delivery's policy: which severities go back for a fix, that
   the rest go to a person, and that an unmet acceptance criterion must be
   one that goes back. On the rerun the same
   problem came back as `high` and was fixed before the PR opened.
2. **A PR fix was handed every open finding, not just the comment.** The
   fix Run for a PR comment also got the review's open `low` and `note`
   findings — ones deliberately left for a person — and fixed them all,
   widening a one-line request into three changes across five files
   (`dd658ce` on #6). **Fix:** a fix for PR feedback is handed that feedback
   only. After the fix, a comment on #4 produced one test and nothing else.
3. **A Run lux lost was followed forever.** The detached lux environment
   was taken down while item 1's simplifier ran (another session's test run
   reclaimed it). The orchestrator got 404 from lux and retried the stream
   every second, for good; the phase never finished. **Fix:** 404 on the
   stream fails the Run, and a failed simplify does not fail the work item,
   so item 1 went on to its PR.
4. **The PR body said "4 findings across 4 reviewers" from one reviewer.**
   It counted finding categories. **Fix:** it counts review Runs.
5. **Every tool completion was recorded as a tool named "tool".**
   OpenCode's completion updates carry neither title nor kind. **Fix:** a
   completion keeps the name its call started with.
6. **The runtime image had no pytest.** The project's context tells agents
   to run it; the first implementer found it missing. **Fix:** added to the
   image.

### Still open

- **Cost.** See above.
- **Scope creep from the implementer.** Nothing yet, but the tasks were
  small. Watch for it on bigger ones.
- **One reviewer.** Only `correctness` ran; the fan-out picks reviewers
  by changed paths, and a Python package matched nothing else. A project
  should be able to name the reviewers it always wants.
- **Webhooks from GitHub to a laptop.** The reconciler carried every PR
  event here. `gh webhook forward` would exercise the real path.
- **Review took 14–18 seconds each time.** Fast enough that a second
  reviewer flavour costs little; worth trying on item 3's size.

## 2026-09-24: steering, pausing, resuming and asking, live

Same setup, on lux built from `da4b3bd` (which acknowledges the prompt and
relays turn usage). The scratch project now requires `correctness` and
`security` reviewers.

**Steer, pause, resume** ("Add a Markdown table formatter"). While the
Sonnet implementer was reading the code, a steer with interrupt asked it to
add an `align` parameter. lux acknowledged it; the interrupt ended the turn
(a turn with zero tokens), and the agent took the steer as its next turn and
built the parameter. Pausing mid-turn stopped the lux Run with its session
kept; resuming brought it back on a second placement — and there it sat.
**A resumed ACP agent waits for input**, and one paused mid-turn never
finished its task, so the plain Resume button left it idle for good. The
fake agent carried on by itself after a resume, which is why no test had
caught it. **Fix:** a resume without a queued directive tells the agent to
continue where it left off; the fake now waits for input after a resume, as
a real agent does. Given that input, the agent remembered its earlier work
("tests already passed (20/20)") and finished; the item went on through
review, a fix round, re-review and simplify.

**Asking** ("Truncate long words"). The task left a product decision open
and said so. Sonnet stopped with a well-formed question block and three
choices — ellipsis character, three dots, or nothing — before writing any
code. The work item showed as needing a person; nothing was pushed. The
answer ("Ellipsis character") reached it as its next turn, quoting the
question; it wrote `word[:limit] + "…"` and documented the choice. Both
required reviewers ran.

Two things the UI showed on this run and changed: the question appeared
twice, once as the fenced block in the agent's message and once as the
question card — the block is now left out of the message; and a turn's
token totals are shown on its closing message rather than a line of their
own.

Still to see: whether agents ask when they should not. The prompt tells
them most tasks need no question; the runs so far that had no ambiguity
asked none.

## 2026-09-24: colour, and findings judged by the reviewer

**Colour** ("Add a word frequency table", PR #12). With colour forced in
the agent's environment, 10 of the implementer's tool calls came back with
escape codes, and the chat renders them: `git diff` with its bold headers,
cyan hunk markers and red/green lines, pytest's summary coloured, and a
failing command's `exit 1` on its collapsed row
(`docs/images/coloured-tool-output.png`). The agent reads past the codes
without trouble; nothing in its replies referred to them.

**Findings.** A re-review is now shown the findings its fixer was sent and
answers for each, fixed or still; nothing else resolves a finding.

## 2026-09-25: parked while it waits for a person

OpenCode on the private lux, the project's grace period set to one minute.
Each work item told the agent to ask before deciding a product question.

**TEXT-18, word frequency.** The implementer asked with `ask_person`
("case-insensitive, or case-sensitive?", with two choices) and ended its
turn. A minute later dude parked it: lux reported the Run `stopped`, so
it held no host. The answer resumed the same lux Run in the same agent
session. The agent then noticed the work named no repository, asked for
`textkit` to change, and ended its turn on the request. It was parked
again, and the approval resumed it with the repository checked out. It
implemented the change case-insensitively, as answered, and committed. The
run then escalated as "no changes": lux pushes only to a branch the spec
named at submit, and dude named one only for work with a repository to
change. Fixed (64d9b0e): a publishing phase always names its branch.

**TEXT-19, sentence count.** Parked for its question, answered, resumed,
and parked again on its repository request. After the approval it never
came back. dude had declared the forge token as a secret with no
repository using it, so lux treated it as the workload's and refused it as
the added repository's credential (422, retried forever). Fixed (05c78af):
the token is declared only with a repository that uses it, and a resume
lux refuses for good now fails the Run with lux's reason. The fake lux
checks the same rule now.

**TEXT-20, paragraph count.** Asked, parked, answered, resumed, requested
`textkit`, parked, approved, resumed with it. It implemented the change,
review and simplify passed, and it opened PR #15. The implementer's ledger
reads: session started, parked, taken back up, parked, taken back up,
commit. One agent session throughout.

Seen along the way: the agent asked only about the decision it was told
to leave to a person, and waited correctly both times. Both times it also
explained in plain words why it was ending its turn.
