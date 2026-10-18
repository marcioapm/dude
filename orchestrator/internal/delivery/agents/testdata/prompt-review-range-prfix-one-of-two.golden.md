Review the changes on this branch for **security**.

The task under review:

Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

Review in `web`, `bbb222..HEAD`: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. This review covers only the fix made for the open pull request's feedback (comments, a review, failing checks), and any fix of it since: the rest of the branch was reviewed before the pull request opened. Judge whether the fix is correct and changes nothing else. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — you may change it
- `web` at `/workspace/repos/web` — you may change it

Name the repository in each finding's `repo` field.