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

Review `aaa111..HEAD`: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. This review covers only what the simplifier changed after the branch was reviewed: a behaviour-preservation check of the simplifier's diff, and of any fix of it since. The simplifier was told to change no behaviour; a change in behaviour, a removed check, an altered error or edge case is a finding. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.

## Round 2

This is review round 2: review the range above.

Decided in earlier rounds — do not raise these again, nor restate one in other words:
- fnd_1 [medium] Totals are rounded per line: judged fixed by run_fix1
- fnd_2 [low] The header row is optional: dismissed by the conductor: the person asked for it
- fnd_3 [high] Export drops the last row: accepted by a person

The conductor triages every finding before anything is fixed. It asks of each: can it happen through a supported path — so its `scenario` starts at a real entry point (a request, a command, a step of the workflow) and names the state it needs; and did this change introduce or worsen it — `introduced: yes`, or `introduced: no` for a problem that was there before. A pre-existing problem is still worth reporting, marked `introduced: no`: the conductor files it to be fixed later rather than here.

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.