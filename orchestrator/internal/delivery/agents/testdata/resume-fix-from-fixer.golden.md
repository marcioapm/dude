## A new round

You worked on this branch before, and there is more to fix: your checkout is now at the head this round starts from. What changed since your last push: in `api`, `git log aaa111..HEAD`.

## Review findings

### [blocking] Totals off by one cent

`api/export.go:40`

Rounds each line, then sums.

Suggested: Sum in cents, round once.