Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

This branch was tested before, and has changed since by more than a next round goes on from, or in a way it could not: this round tests afresh. Run every criterion's check and report every criterion with fresh evidence. No criterion may be carried from an earlier round.

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — the change under test
- `web` at `/workspace/repos/web` — the change under test

Test the changes in each checkout together, and commit in none of them. Name the repository in each finding's `repo` field.