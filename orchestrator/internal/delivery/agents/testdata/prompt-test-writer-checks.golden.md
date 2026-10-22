Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

## What the writers before you checked

These are the checks the implementer, fixers and simplifier of this attempt reported, as dude recorded them, oldest first, each with the commits that Run published. They were run on those commits, not necessarily on the head you test: a check that passed there says nothing about a later commit. Use them to know which failures are already known and accepted, not as evidence for any criterion, and do not re-run them as routine.

- Implementer (api@1111111): waived: Three tests fail on main as well; tracked separately.
- Fixer (api@2222222, web@3333333): backed
  - go test ./export/...: pass (the recorded call says so)
- Simplifier: no changes; checks not needed

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — the change under test
- `web` at `/workspace/repos/web` — the change under test

Test the changes in each checkout together, and commit in none of them. Name the repository in each finding's `repo` field.