## Work in progress

This is someone's work in progress on pull request #9 (`ana/csv` in api), based on `main`: your checkout is its head. Carry it toward the goal below. Build on what is there; do not rewrite it, and keep its author's approach unless it cannot meet the goal. Read what it already does first (`git log origin/main..HEAD`, `git diff origin/main...HEAD`). If the goal already looks met, check it and meet the testing requirement in your instructions; if that needs no change, commit nothing and say so in your reply.

Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

Decided by people during this work (part of the task; do not ask again):
- Q: Which separator? Comma or semicolon.
  A: Comma, always.
- Q: Header row?
  A: Yes

## Feedback from people

People reviewing this task left these. Each is in its author's own words.

### fbi_code — Ana

On `api/export.go:10-12 (new side)` at commit abcdef1 (those lines have changed since):

```diff
+total += round(line)
 return total
```

> This rounds per line.
> Sum first.

reply (the fixer): > Will do.

reopened (Ana): > Not yet.

### fbi_uncommitted — Bo

On `web/page.tsx:3 (new side)`, uncommitted work of Run run_impl:

> Rename this.

### fbi_preview — Ana

On `https://app.preview.example/invoices?month=3 · button.export`:

Route /invoices in a 1280×720 window · the element is 92×38 at 152, 432

Picture: image-2.png, attached (the element, outlined)

The element's text: Export CSV now

Component ExportButton at `web/ExportButton.tsx:9`

> The button is cut off.

### fbi_general — Cy

> Looks good otherwise.

Handle every item; none may be left unanswered: fix it and resolve it with your commit (feedback_resolve), or reply saying why you will not (feedback_reply).

No repository is checked out for this work: it changes no code. Its result is what you publish and what you reply.

## Base

This work continues an existing branch. Its changes are measured against its base, not the default branch:
- `api`: `main`. Writers call sync_base, then merge lux/main; reviewers use the available local base refs with `git diff <base>...HEAD`, and report if a base ref is unavailable. Credentials are not in the checkout.