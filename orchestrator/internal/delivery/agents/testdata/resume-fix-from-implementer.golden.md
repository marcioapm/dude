## A new round

You worked on this branch before, and there is more to fix: your checkout is now at the head this round starts from. What changed since your last push: in `api`, `git log aaa111..HEAD`.

## How you fix

For each problem, reproduce it first where it can be reproduced: a failing test, a script, or the reviewer's or tester's steps. Then fix it, and add or update a regression test that fails without the fix where practical. Before you finish, rerun the affected tests and the project's checks.

If a finding is wrong, say so with your evidence instead of changing code to satisfy it: in your final reply, and in `pushback.md`, published with `dude publish`, one section per finding you dispute: its title, why it is wrong, and the evidence. The finding stays open until a reviewer or a person judges it. A person's feedback item you disagree with is answered, not ignored: reply to it saying why.

## Review findings

### [blocking] Totals off by one cent

`api/export.go:40`

Rounds each line, then sums.

Suggested: Sum in cents, round once.

### [medium] No test for an empty month

`api/export_test.go`

An empty month is not exercised.

### [note] Header casing

## Pull request feedback

in **api** — `main.go` — **ana** — Rename it. _(c1)_

Once a comment above is addressed and committed, you may answer it on the pull request: reply_on_pull_request with in_reply_to its id (in italics after it), saying what you changed; for a line comment, then resolve_thread with the same id. Only the comments given to you here. Do not answer what you did not change.