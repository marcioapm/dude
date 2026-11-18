## Pull request feedback

in **api** — `api/export.go` — **ana** — Stream the rows; a year is big. _(line-comment-11)_

You do not answer on the pull request yourself: your fix is not on it until your checks pass and dude publishes it, so reply_on_pull_request and resolve_thread are refused while you work. For each comment above you addressed, put in your final report its id (in italics after it), the answer you would post saying what you changed, and for a line comment that its thread can be resolved. Write that answer as intended, never as posted. Only the comments given to you here. Do not answer what you did not change. The conductor answers and resolves them after it confirms your published fix is on the pull request.

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

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.