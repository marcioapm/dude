Review the changes on this branch for **security**.

Look for missing authentication or authorization checks, data crossing a tenant boundary, injection, untrusted input treated as trusted, and credentials that reach somewhere they should not: logs, clients, other tenants.

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

Review in `web`, `bbb222..HEAD`: `git log` and `git diff` over that range are what is under review; a checkout not named here is not. This review covers only the fix made for the open pull request's feedback (comments, a review, failing checks), and any fix of it since: the rest of the branch was reviewed before the pull request opened. Judge whether the fix is correct and changes nothing else. Read the changed code and what it relies on — its callers, contracts, schemas and tests — before you judge a line. A problem in code outside the range is out of scope unless the range makes it reachable.

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

Severity decides what happens next: `blocking`, `high`, `medium` send the change back to be fixed before a pull request opens; anything else goes into the pull request for a person to weigh. So a finding that means an acceptance criterion is not met must be one of `blocking`, `high`, `medium`, however small the fix.

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — you may change it
- `web` at `/workspace/repos/web` — you may change it

Name the repository in each finding's `repo` field.

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.