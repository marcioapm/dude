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

- `api` at `/workspace/repos/api` — source for inspection and reporting; a writable mount does not authorize task code changes
- `web` at `/workspace/repos/web` — source for inspection and reporting; a writable mount does not authorize task code changes

Inspect the source and report what you find. Scratch experiments and report artifacts are allowed; task code edits, commits, and pull request publication are not authorized. Publish report artifacts through the existing reporting tools and deliver your findings in your reply.