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

Review `aaa111..HEAD`: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. This review covers only the fix made for the open pull request's feedback (comments, a review, failing checks), and any fix of it since: the rest of the branch was reviewed before the pull request opened. Judge whether the fix is correct and changes nothing else. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.

## Round 1

This is the loop's first review round: review all of the range above.

The conductor triages every finding before anything is fixed. It asks of each: can it happen through a supported path — its `scenario` starts at a real entry point (a request, a command, a step of the workflow); and did this change introduce or worsen it (`introduced`). A pre-existing problem is still worth reporting, marked `introduced: no`: the conductor files it to be fixed later rather than here.

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.