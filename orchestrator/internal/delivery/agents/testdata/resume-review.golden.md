## A new round

You reviewed this branch before, and it has changed since: your checkout is now at the head this round reviews. What changed since your last review: in `api`, `git log aaa111..HEAD`.

The commits since were made by another agent. Read them as new code: a change can break something outside the lines it touched — a caller, a contract, an error path, a test it weakened. Check what the change relies on and what relies on it, not only the lines in the range. Your earlier conclusions are not evidence: judge each finding by reading and running the code as it is now. Report anything new you find, wherever it is.

## Findings from the last review

A fix has been made since. For each finding below, read the code as it is now, run it where you can, and say whether it is fixed — never judge by which files the fix touched. A finding with a `Done when` is judged against it: `fixed` only when it holds. Where it asks for what only a CI run, a pull request, a merge or a deploy could show, judge the part these checkouts show: `fixed` when that holds. Judge every one, whatever its category or the reviewer that raised it: you are the one judging it this round. Report a problem that is still there in your verdicts, not as a new finding; report anything new as a finding as usual.

**F1** [blocking] Totals off by one cent — `api/export.go:40`
Rounds each line, then sums.
Done when: An export's total equals the invoice page's total to the cent.

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

## Questions reviewers asked

Reviewers of your category asked these of a person earlier in this work. An answer is a fact to judge by.

**Q1** Do invoices close in UTC or in the organisation's zone? (about: Totals off by one cent)
Not answered yet: judge without it; do not ask it again.

**Q2** Is a retry expected after a timeout?
Answered by a person: Yes, once, after 30 seconds.

**Q3** Are refunds in scope?
Answered by the task's conductor: No: the task names invoices only.

## The last fix

This round follows a fix, whose commits are in `api`, `git log aaa111..bbb222`. Read them to answer each new finding's `caused_by_fix`: `yes` when the problem is in code those commits wrote, `no` otherwise.

Report each finding as one YAML document, separated by `---`:

```yaml
severity: blocking | high | medium | low | note
category: <your review category>
repo: <the repository, when there are several>
file: path/to/file.ts
line: 123
title: One line naming the problem
description: What is wrong and why it matters.
acceptance: What must be observably true once this is fixed — an outcome or invariant someone can check on these checkouts, never how to fix it, nor something only a CI run, a pull request, a merge or a deploy could show.
caused_by_fix: yes | no
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