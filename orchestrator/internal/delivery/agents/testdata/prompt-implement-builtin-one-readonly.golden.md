Implement this task. Run the project's formatter, type checks and tests before you finish — handing over code that does not build is not finishing.

Before you finish, exercise the changed behaviour yourself, and add a test for each acceptance criterion that can be tested. A later independent tester verifies your work; it does not replace your own testing.

Commit your work when you are done. Only committed changes are kept. Co-authored-by trailers for the task's people are added to each commit automatically: do not add, change or remove them.

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

## Repositories

This work spans these checkouts:

- `docs` at `/workspace/repos/docs` — read only: for reference, do not change it — changes there are never kept

Commit in each repository you change. Each changed repository gets its own pull request.

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.