Review the changes on this branch for **correctness**.

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

## Findings from the last review

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. A finding with a `Done when` is judged against it: `fixed` only when it holds. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.
Done when: An export's total equals the invoice page's total to the cent.

**F2** [medium] No test for an empty month — `api/export_test.go`
An empty month is not exercised.

**F3** [note] Header casing

Answer for every one, as one more YAML document:

```yaml
verdicts:
  F1: fixed | still
  F2: fixed | still
  F3: fixed | still
```

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — you may change it
- `docs` at `/workspace/repos/docs` — read only: for reference, do not change it — changes there are never kept

Name the repository in each finding's `repo` field.