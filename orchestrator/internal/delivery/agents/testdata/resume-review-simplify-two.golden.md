## A new round

You reviewed this branch before, and it has changed since: your checkouts are now at the head this round reviews. What changed since your last review: in `api`, `git log aaa111..HEAD`; in `web`, `git log ccc000..HEAD`. This review covers only what the simplifier changed after the branch was reviewed: a behaviour-preservation check of the simplifier's diff, and of any fix of it since. The simplifier was told to change no behaviour; a change in behaviour, a removed check, an altered error or edge case is a finding.

The commits since were made by another agent. Read them as new code: a change can break something outside the lines it touched — a caller, a contract, an error path, a test it weakened. Check what the change relies on and what relies on it, not only the lines in the range. Your earlier conclusions are not evidence: judge each finding by reading and running the code as it is now. Report anything new you find, wherever it is.

## Questions reviewers asked

Reviewers of your category asked these of a person earlier in this work. An answer is a fact to judge by.

**Q1** Is a retry expected after a timeout?
Answered by a person: Yes, once, after 30 seconds.

Report each finding as one YAML document, separated by `---`:

```yaml
severity: blocking | high | medium | low | note
category: <your review category>
repo: <the repository, when there are several>
file: path/to/file.ts
line: 123
title: One line naming the problem
introduced: yes | no
trivial: yes | no
scenario: The entry point, the state it needs, and what goes wrong.
evidence_kind: failing_test | mutation | command | query_plan | trace
evidence: |
  The test and its failure, the mutation and the test that survived it,
  the command and its output, the plan, or the trace with file:line steps.
description: What is wrong and why it matters.
acceptance: What must be observably true once this is fixed — an outcome or invariant someone can check on these checkouts, never how to fix it, nor something only a CI run, a pull request, a merge or a deploy could show.
caused_by_fix: yes | no
repair: The smallest local change that removes it.
```

`introduced` is `yes` when this round's range introduced or worsened it, `no` when it was there before (left unfixed, or out of the range and made reachable). Report nothing if you find nothing.

End your reply with a summary of the review, as one more YAML document; it is not a finding:

```yaml
review_summary:
  read: the code you read
  ran: each command you ran, and its result
  not_checked: what you could not check, and why
```