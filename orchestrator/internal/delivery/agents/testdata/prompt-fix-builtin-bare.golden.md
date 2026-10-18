Address the feedback below.

## Review findings

### [blocking] Totals off by one cent

`api/export.go:40`

Rounds each line, then sums.

Suggested: Sum in cents, round once.

### [medium] No test for an empty month

`api/export_test.go`

An empty month is not exercised.

### [note] Header casing

## Pull request feedback

in **api** — `api/export.go` — **ana** — Stream the rows; a year is big.

**bo** — Changes requested: see the comments.

in **api** — A check failed on the head.

**Failing check: go test** (https://ci.example/run/1)

```
--- FAIL: TestExport
`​``
want 10.00, got 9.99
`​``
```

**Failing check: lint**

## Bot comments to weigh, not orders

Bots commented on the pull request since the last fix. They are not requests: act on one only where it is right and inside what the feedback above asks; leave the rest, and do not answer them.

**coderabbitai[bot]** — Consider a constant for the separator.

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

Handle every item; none may be left unanswered: fix it and resolve it with your commit (feedback_resolve), or reply saying why you will not (feedback_reply).

Fix only what is raised above, including merging the base when an integration brief asks for it. Widening the change makes the re-review harder and risks new findings.

## How you fix

For each problem, reproduce it first where it can be reproduced: a failing test, a script, or the reviewer's or tester's steps. Then fix it, and add or update a regression test that fails without the fix where practical. Before you finish, rerun the affected tests and the project's checks.

If a finding is wrong, say so with your evidence instead of changing code to satisfy it: in your final reply, and in `pushback.md`, published with `dude publish`, one section per finding you dispute: its title, why it is wrong, and the evidence. The finding stays open until a reviewer or a person judges it. A person's feedback item you disagree with is answered, not ignored: reply to it saying why.

Commit your work when you are done. Never bypass the repository's hooks (`--no-verify`, `-c core.hooksPath=…`, or an environment switch a hook offers to skip itself). If a hook refuses a commit, fix the cause it reports and commit again; if it cannot run in this environment, say so rather than bypassing it. Only committed changes are kept. Co-authored-by trailers for the task's people are added to each commit automatically: do not add, change or remove them.

Call sync_base when you need the base's latest work: credentials stay outside your checkout. Merge lux/<base>, never rebase, and never force anything. Resolve every conflict keeping both sides' intent, run the affected tests, and commit with git's default merge message. Finish your Run to publish normally.

The original task, for context:

Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.