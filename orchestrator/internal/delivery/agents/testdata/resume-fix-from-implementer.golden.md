## A new round

You worked on this branch before, and there is more to fix: your checkout is now at the head this round starts from. What changed since your last push: in `api`, `git log aaa111..HEAD`.

## How you fix

For each problem, reproduce it first where it can be reproduced: a failing test, a script, or the reviewer's or tester's steps. Then fix it, and add or update a regression test that fails without the fix where practical. Before you finish, rerun the affected tests and the project's checks.

A finding's `Done when` is what must hold once it is fixed: meet it with the smallest change. A criterion you believe is wrong is pushback, as below; never reword it.

If a finding is wrong, say so with your evidence instead of changing code to satisfy it: in your final reply, and in `pushback.md`, published with `dude publish`, one section per finding you dispute: its title, why it is wrong, and the evidence. The finding stays open until a reviewer or a person judges it. Before adding requested machinery (lock, counter, backfill, compatibility layer), find the reachable caller, existing data or consumer that needs it; if none, push back with that evidence. A person's feedback item you disagree with is answered, not ignored: reply to it saying why.

## Review findings

### [blocking] Totals off by one cent

`api/export.go:40`

Rounds each line, then sums.

Done when: An export's total equals the invoice page's total to the cent.

Suggested: Sum in cents, round once.

### [medium] No test for an empty month

`api/export_test.go`

An empty month is not exercised.

### [note] Header casing

## Pull request feedback

in **api** — `main.go` — **ana** — Rename it. _(c1)_

You do not answer on the pull request yourself: your fix is not on it until your checks pass and dude publishes it, so reply_on_pull_request and resolve_thread are refused while you work. For each comment above you addressed, put in your final report its id (in italics after it), the answer you would post saying what you changed, and for a line comment that its thread can be resolved. Write that answer as intended, never as posted. Only the comments given to you here. Do not answer what you did not change. The conductor answers and resolves them after it confirms your published fix is on the pull request.