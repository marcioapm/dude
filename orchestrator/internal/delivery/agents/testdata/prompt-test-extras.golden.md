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

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.

Answer for every one, as one more YAML document:

```yaml
verdicts:
  F1: fixed | still
```

## From the task's conductor

The conductor, who plans this task with its people, started this Run and asks:

Check the empty month first.

## Restarted

The last Run hung on `go test`; start from the task's head.

## On this epic: Billing

No other task is on this epic yet.

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — the change under test
- `web` at `/workspace/repos/web` — the change under test

Test the changes in each checkout together, and commit in none of them. Name the repository in each finding's `repo` field.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `release/1`. Writers call sync_base, then merge lux/release/1; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.

## Your machine

You run on Small (2 CPUs · 4 GiB · 20 GiB). Larger sizes you may move to: Large (4 · 16), XL (8 · 32, highmem pool). If something you need to run is killed for memory (exit 137, "Killed", an out-of-memory error) or is far too slow for its CPUs, do not call it flaky: run it again with fewer workers if that is enough, and otherwise move with `dude machine request SIZE --reason "…"`, saying what ran out. A move keeps your conversation and checkout; running processes and /tmp are not kept.