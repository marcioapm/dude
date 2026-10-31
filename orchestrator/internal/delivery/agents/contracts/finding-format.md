{{if and .ConductedLoop (eq .Role "reviewer")}}Report each finding as one YAML document, separated by `---`:

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

`introduced` is `yes` when this round's range introduced or worsened it, `no` when it was there before (left unfixed, or out of the range and made reachable). Report nothing if you find nothing.{{else}}Report each finding as one YAML document, separated by `---`:

```yaml
severity: blocking | high | medium | low | note
category: <your review category>
repo: <the repository, when there are several>
file: path/to/file.ts
line: 123
title: One line naming the problem
description: What is wrong and why it matters.
acceptance: What must be observably true once this is fixed — an outcome or invariant someone can check on these checkouts, never how to fix it, nor something only a CI run, a pull request, a merge or a deploy could show.
{{if eq .Role "reviewer"}}caused_by_fix: yes | no
{{end}}suggested_fix: What to do instead.
```

Report nothing if you find nothing. A finding you are not confident in is a `note`, not a `blocking` — a reviewer that cries wolf costs the next fix attempt for nothing.{{end}}
