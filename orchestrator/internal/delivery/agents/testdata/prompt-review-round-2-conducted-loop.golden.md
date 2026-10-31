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

Review `aaa111..HEAD`: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. This review round covers only what changed since the previous round reviewed the branch: the fix made for that round's findings, and anything else committed since. The rest of the branch was reviewed in earlier rounds. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.

## Findings from the last review

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. A finding with a `Done when` is judged against it: `fixed` only when it holds. Where it asks for what only a CI run, a pull request, a merge or a deploy could show, judge the part these checkouts show, or its description where they show none: `fixed` when that part holds. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.
Done when: An export's total equals the invoice page's total to the cent.

Answer for every one, as one more YAML document:

```yaml
verdicts:
  F1: fixed | still
```

## Round 2

This is review round 2, of what changed since round 1 reviewed the branch (the range above). Report what those commits introduce, and what they were sent to fix and left unfixed. A problem elsewhere in the branch is out of scope unless these commits make it reachable.

Decided in earlier rounds — do not raise these again, nor restate one in other words:
- fnd_1 [medium] Totals are rounded per line: judged fixed by run_fix1
- fnd_2 [low] The header row is optional: dismissed by the conductor: the person asked for it
- fnd_3 [high] Export drops the last row: accepted by a person

The conductor triages every finding before anything is fixed. It asks of each: can it happen through a supported path — its `scenario` starts at a real entry point (a request, a command, a step of the workflow); and did this change introduce or worsen it (`introduced`). A pre-existing problem is still worth reporting, marked `introduced: no`: the conductor files it to be fixed later rather than here.

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.