Simplify the changes on this branch without changing what they do.

Remove needless complexity, improve names and structure, delete dead code the change introduced, and consolidate obvious duplication.

Do not widen the scope, do not add features, and do not change behaviour. Run the tests: if they do not pass, your simplification was not behaviour-preserving.

Commit your work when you are done. Only committed changes are kept. Co-authored-by trailers for the task's people are added to each commit automatically: do not add, change or remove them.

Call sync_base when you need the base's latest work: credentials stay outside your checkout. Merge lux/<base>, never rebase, and never force anything. Resolve every conflict keeping both sides' intent, run the affected tests, and commit with git's default merge message. Finish your Run to publish normally.

The task this branch implements:

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