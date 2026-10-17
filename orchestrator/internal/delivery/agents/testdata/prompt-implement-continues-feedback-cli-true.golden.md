Implement this task. Run the project's formatter, type checks and tests before you finish — handing over code that does not build is not finishing.

## Work in progress

This is someone's work in progress on pull request #9 (`ana/csv` in api), based on `main`: your checkout is its head. Carry it toward the goal below. Build on what is there; do not rewrite it, and keep its author's approach unless it cannot meet the goal. Read what it already does first (`git log origin/main..HEAD`, `git diff origin/main...HEAD`). If the goal already looks met, check it and meet the testing requirement below; if that needs no change, commit nothing and say so in your reply.

Before you finish, exercise the changed behaviour yourself, and add a test for each acceptance criterion that can be tested. A later independent tester verifies your work; it does not replace your own testing.

Commit your work when you are done. Never bypass the repository's hooks (`--no-verify`, `-c core.hooksPath=…`, or an environment switch a hook offers to skip itself). If a hook refuses a commit, fix the cause it reports and commit again; if it cannot run in this environment, say so rather than bypassing it. Only committed changes are kept. Co-authored-by trailers for the task's people are added to each commit automatically: do not add, change or remove them.

Call sync_base when you need the base's latest work: credentials stay outside your checkout. Merge lux/<base>, never rebase, and never force anything. Resolve every conflict keeping both sides' intent, run the affected tests, and commit with git's default merge message. Finish your Run to publish normally.

Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

Decided by people during this work (part of the task; do not ask again):
- Q: Which separator? Comma or semicolon.
  A: Comma, always.
- Q: Header row?
  A: Yes

## Feedback from people

People reviewing this task left these. Each is in its author's own words.

### fbi_code — Ana

On `api/export.go:10-12 (new side)` at commit abcdef1 (those lines have changed since):

```diff
+total += round(line)
 return total
```

> This rounds per line.
> Sum first.

reply (the fixer): > Will do.

reopened (Ana): > Not yet.

### fbi_uncommitted — Bo

On `web/page.tsx:3 (new side)`, uncommitted work of Run run_impl:

> Rename this.

### fbi_preview — Ana

On `https://app.preview.example/invoices?month=3 · button.export`:

Route /invoices in a 1280×720 window · the element is 92×38 at 152, 432

Picture: image-2.png, attached (the element, outlined)

The element's text: Export CSV now

Component ExportButton at `web/ExportButton.tsx:9`

> The button is cut off.

### fbi_general — Cy

> Looks good otherwise.

Handle every item; none may be left unanswered. Fix what it asks, commit, then `dude feedback resolve ID --commit HEAD`. If you will not change it — it is wrong, or out of scope — say why with `dude feedback reply ID "…"`. `dude feedback` lists what is still open.

If you cannot go on without a decision only a person can make — the task is ambiguous in a way that changes what you build, or two reasonable readings conflict — ask with the dude tool ask_person, then end your turn. Several such decisions go in one call (up to 4 questions, each with a short header and choices); dude always lets the person answer in their own words. The answers come back as your next message, even if the person takes hours to answer: your work is kept meanwhile. Do not ask about anything you can decide or find out yourself; most tasks need no question at all.

No repository is checked out for this work: it changes no code. Its result is what you publish (see below) and what you reply.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `main`. Writers call sync_base, then merge lux/main; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.

The dude tools (list_tasks, list_epics, list_repositories, create_task, emit_event, request_repository, search_memory, get_memory, remember, run_diff) act on the work you are part of: record work you find outside your task (a person decides on it), report progress people can follow, ask for another repository you need, see what a Run of your task changed (run_diff: the files, then the lines of those you name). Memory is what people and agents here learned before you: search it (search_memory) before investigating something that may already be known, and when you learn something the next agent should know — a fact that holds, a procedure that works, a trap and its way around — save it (remember), after searching so you do not save it twice. Do not save what the code or the task already says. On an epic, your briefing's "On this epic" says which of the peer tools (list_tasks with peers, run_diff with task, message, wait_for_reply, note) you have.

The same, from the shell: the `dude` command (see `dude help`) — `dude task list`, `dude epic list`, `dude task create` for work you find outside your task (a person decides on it), `dude event progress --data '{"done":3,"of":10}'` for progress people can follow, `dude repo list` and `dude repo request`, `dude memory search QUERY`, `dude memory show ID` and `dude memory add --title T --content C` for memory, `dude diff [RUN] [PATH...]` for what a Run of your task changed, and `dude publish FILE --name NAME --description "…"` to keep a file for people (the description: one short line of what the file is for).

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.