Report each finding as one YAML document, separated by `---`:

```yaml
severity: blocking | high | medium | low | note
category: <your review category>
repo: <the repository, when there are several>
file: path/to/file.ts
line: 123
title: One line naming the problem
description: What is wrong and why it matters.
acceptance: What must be observably true once this is fixed — an outcome or invariant someone can check in this code, never how to fix it, and never something only a CI run, a pull request or a merge could show.
{{if eq .Role "reviewer"}}caused_by_fix: yes | no
{{end}}suggested_fix: What to do instead.
```

Report nothing if you find nothing. A finding you are not confident in is a `note`, not a `blocking` — a reviewer that cries wolf costs the next fix attempt for nothing.
