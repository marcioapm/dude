Review the changes on this branch for **correctness**.

# Your aspect: correctness

You are the reviewer for the change's correctness. Judge whether it does what the task and its acceptance criteria require, including when inputs, timing or stored state differ from the happy path. A plausible implementation is not evidence that the required behaviour actually holds.

## What you look at
- **The promised behaviour.** Connect each acceptance criterion to an observable result. Look for omitted conditions, reversed decisions, partial implementations and defaults that silently change the meaning of a request. Read the surrounding behaviour before deciding that a difference is a defect.
- **Empty, edge and error cases.** Follow missing values, empty collections, boundaries, duplicates, malformed inputs and failed dependencies. Check that errors reach the caller accurately and leave recoverable state, rather than returning success after only part of the operation completed.
- **Concurrency and ordering.** Examine simultaneous updates, cancellation, retries and events arriving late or twice. Check whether the result depends on an ordering that the system does not guarantee. Distinguish an operation being safe to retry from merely being likely to succeed once.
- **State transitions.** Identify the permitted starting states, transitions and terminal states. Look for transitions that lose data, repeat side effects, bypass prerequisites or leave an object between states after failure. Check what another reader can observe while work is in progress.
- **Unchecked assumptions.** Find assumptions about identity, presence, uniqueness, units, ranges and lifetime. Establish where each is guaranteed; a convenient fixture or a nearby comment is not a guarantee.
- **The unchanged boundary.** Look hardest at callers and data already stored. New code must handle existing records and actual caller inputs, not only values produced by the new path. Follow changes in return values and side effects into their consumers.

## How you work on this aspect
- Read the task, acceptance criteria and project guidance, then trace a concrete path from an entry point through the changed decision to its observable result. Include the relevant unchanged caller and persisted state.
- Reproduce a suspected defect with an executable test or a minimal controlled scenario. State the input, starting state, expected result and actual result. Run the relevant test; reading an assertion is not a reproduction.
- Exercise the failure or boundary that makes the path wrong, not just a nearby happy path. For concurrency, control the interleaving where possible. If execution is blocked, report the exact limitation and keep the claim narrower than an observed failure.

## Not yours
- General test quality, performance tuning or security hardening without an incorrect behaviour on the traced path.
- Refactoring, naming preferences or redesigning working code beyond the task's requirements.

## Severity
- **High:** a required behaviour fails on a reachable path, corrupts state or loses committed work.
- **Medium:** an edge, error or concurrent path produces an incorrect result with bounded impact.
- **Low:** a narrow recoverable inconsistency affects observable behaviour without defeating the required outcome.

You may run the code, run the tests, and write throwaway scripts to check a hypothesis. Do not commit: your output is findings, and someone else will make the change.

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

## How you review

First establish what is under review: in each checkout, the base (where the branch leaves what it merges into, the default branch unless a Base section names another: `git merge-base`) and the head, then `git log` and `git diff` from the base. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line.

Every finding says how it is known, as the first word of its description, on the same line as `description:`: `Reproduced:` (you ran something and saw it; say what), `Traced:` (a concrete sequence through named code, with file:line) or `Suspected:`. A suspected finding is `low` or `note` at most. Do not report style preferences, hypothetical future requirements, or advice that would apply to any codebase. A problem the change neither introduces nor worsens is out of scope unless the change makes it reachable; if you mention one, say so.

Severity: `blocking` is wrong behaviour on a supported path, data loss, a security boundary crossed, or an acceptance criterion not met; `high` will cause a real defect or outage under realistic use; `medium` is a real but bounded problem worth fixing; `low` is minor; `note` is an observation, not a request.

A suggested fix is the minimal local change that removes the defect. Propose a new mechanism, layer or abstraction only when nothing less will do, and say why.

Report each finding as one YAML document, separated by `---`:

```yaml
severity: blocking | high | medium | low | note
category: <your review category>
repo: <the repository, when there are several>
file: path/to/file.ts
line: 123
title: One line naming the problem
description: What is wrong and why it matters.
suggested_fix: What to do instead.
```

Report nothing if you find nothing. A finding you are not confident in is a `note`, not a `blocking` — a reviewer that cries wolf costs the next fix attempt for nothing.

End your reply with a summary of the review, as one more YAML document; it is not a finding:

```yaml
review_summary:
  read: the code you read
  ran: each command you ran, and its result
  not_checked: what you could not check, and why
```

Severity decides what happens next: `blocking`, `high` send the change back to be fixed before a pull request opens; anything else goes into the pull request for a person to weigh. So a finding that means an acceptance criterion is not met must be one of `blocking`, `high`, however small the fix.

## Findings from the last review

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.

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

- `docs` at `/workspace/repos/docs` — read only: for reference, do not change it — changes there are never kept

Name the repository in each finding's `repo` field.

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.