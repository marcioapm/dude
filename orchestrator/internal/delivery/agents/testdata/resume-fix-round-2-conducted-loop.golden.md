## A new round

You worked on this branch before, and there is more to fix: your checkout is now at the head this round starts from. What changed since your last push: `git log aaa111..HEAD`.

## Review findings

### [blocking] Totals off by one cent

`api/export.go:40`

Rounds each line, then sums.

Done when: An export's total equals the invoice page's total to the cent.

Suggested: Sum in cents, round once.

## Fix round 2

You fix review round 2's findings. The review after you is round 3: it reads only what changed since round 2, your commits, for what they introduce and what they leave unfixed. Keep the change to what was raised: anything else you touch is reviewed as new.

- Red first: for each finding, a test that fails at the head you start from, then the fix that makes it pass. Prove each test can fail for the behaviour it covers with one targeted mutation of the fixed code, and say which.
- Commit each finding's fix as soon as its test passes, one commit per finding: do not batch them for the end.
- If you are still reading after about 20 minutes, implement the first finding and commit it.
- Near your time limit, stop: report what is done and what is left, and end your checks document with `outcome: partial` and an `outcome_note` saying what is left. A partial result reported honestly is a success; the commits you made are kept. Say `outcome: blocked` when you cannot go on without someone.
- A finding whose premise is false at this head is fixed by no change: say so with the evidence (the test that passes, the code path that rules it out), and change nothing for it.
- Never edit a golden or generated file by hand: regenerate it. No test that inspects source text or pins a rendered substring.