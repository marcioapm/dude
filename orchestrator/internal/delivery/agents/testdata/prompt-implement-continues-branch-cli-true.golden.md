## Work in progress

This is someone's work in progress on `ana/csv` in api, based on `develop`: your checkout is its head. Carry it toward the goal below. Build on what is there; do not rewrite it, and keep its author's approach unless it cannot meet the goal. Read what it already does first (`git log origin/develop..HEAD`, `git diff origin/develop...HEAD`). If the goal already looks met, check it and meet the testing requirement in your instructions; if that needs no change, commit nothing and say so in your reply.

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

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `main`. Writers call sync_base, then merge lux/main; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.