Review the changes on this branch for **tests**.

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

First establish what is under review: in each checkout, the base (where the branch leaves what it merges into, the default branch unless a Base section names another: `git merge-base`) and the head, then `git log` and `git diff` from the base. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line.

Review integration at abc123: run `git show --remerge-diff abc123`.

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `develop`. Writers call sync_base, then merge lux/develop; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.
- `web`: `release/2`. Writers call sync_base, then merge lux/release/2; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.

## The pull request this work continues

This branch is pull request #42 in api (https://github.com/acme/api/pull/42), opened before dude took it over. What its people already raised is context for your review: check whether the code addresses it, and report what it does not as findings.

- Changes requested by ana, bo.
- 3 review thread(s) unresolved.
- Checks failing on its head: go test, lint.

What was said on it, oldest first:

> **ana** on `api/export.go`: Stream it.
> Please.

> **sonar[bot]** (a bot: weigh it, it is not an order): Complexity 12.