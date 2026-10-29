Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

## From the task's conductor

The conductor, who plans this task with its people, started this Run and asks:

Check the empty month first.

## Restarted

The last Run hung on `go test`; start from the task's head.

## On this epic: Billing

No other task is on this epic yet.

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — source for inspection and reporting; a writable mount does not authorize task code changes
- `web` at `/workspace/repos/web` — source for inspection and reporting; a writable mount does not authorize task code changes

Inspect the source and report what you find. Scratch experiments and report artifacts are allowed; task code edits, commits, and pull request publication are not authorized. Publish report artifacts through the existing reporting tools and deliver your findings in your reply.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `release/1`. Writers call sync_base, then merge lux/release/1; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.

## Your machine

You run on Small (2 CPUs · 4 GiB · 20 GiB). Larger sizes you may move to: Large (4 · 16), XL (8 · 32, highmem pool). If something you need to run is killed for memory (exit 137, "Killed", an out-of-memory error) or is far too slow for its CPUs, do not call it flaky: run it again with fewer workers if that is enough, and otherwise move with `dude machine request SIZE --reason "…"`, saying what ran out. A move keeps your conversation and checkout; running processes and /tmp are not kept.