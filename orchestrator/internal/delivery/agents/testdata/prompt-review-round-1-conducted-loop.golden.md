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

## Round 1

This is the loop's first review round: review the whole change.

The conductor triages every finding before anything is fixed. It asks of each: can it happen through a supported path — so its `scenario` starts at a real entry point (a request, a command, a step of the workflow) and names the state it needs; and did this change introduce or worsen it — `introduced: yes`, or `introduced: no` for a problem that was there before. A pre-existing problem is still worth reporting, marked `introduced: no`: the conductor files it to be fixed later rather than here.

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.