Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

## Findings from the last review

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. A finding with a `Done when` is judged against it: `fixed` only when it holds. Where it asks for what only a CI run, a pull request, a merge or a deploy could show, judge it on the part these checkouts show: `fixed` when that part holds. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.
Done when: An export's total equals the invoice page's total to the cent.

Answer for every one, as one more YAML document:

```yaml
verdicts:
  F1: fixed | still
```

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — the change under test
- `web` at `/workspace/repos/web` — the change under test

Test the changes in each checkout together, and commit in none of them. Name the repository in each finding's `repo` field.