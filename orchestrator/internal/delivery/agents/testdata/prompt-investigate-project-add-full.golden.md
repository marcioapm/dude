Investigate this task before any code is written. Read the relevant code, identify what will have to change, and report what you found. Do not change anything.

PROJECT: run `make check` before you finish.

Export invoices as CSV

Let an accountant download a month's invoices as one CSV.

The button sits as in [Image 1: toolbar.png]; the old mock-up [Image unavailable: old] is gone.

Acceptance criteria:
- A CSV downloads from the invoices page
- Columns, in order:
  - `number`
  - `total`
- Totals match the invoice page to the cent

## Repositories

This work spans these checkouts:

- `api` at `/workspace/repos/api` — you may change it
- `web` at `/workspace/repos/web` — you may change it

Commit in each repository you change. Each changed repository gets its own pull request.

The dude tools (list_tasks, list_epics, list_repositories, create_task, emit_event, request_repository, search_memory, get_memory, remember, run_diff) act on the work you are part of: record work you find outside your task (a person decides on it), report progress people can follow, ask for another repository you need, see what a Run of your task changed (run_diff: the files, then the lines of those you name). Memory is what people and agents here learned before you: search it (search_memory) before investigating something that may already be known, and when you learn something the next agent should know — a fact that holds, a procedure that works, a trap and its way around — save it (remember), after searching so you do not save it twice. Do not save what the code or the task already says. On an epic, your briefing's "On this epic" says which of the peer tools (list_tasks with peers, run_diff with task, message, wait_for_reply, note) you have.

The same, from the shell: the `dude` command (see `dude help`) — `dude task list`, `dude epic list`, `dude task create` for work you find outside your task (a person decides on it), `dude event progress --data '{"done":3,"of":10}'` for progress people can follow, `dude repo list` and `dude repo request`, `dude memory search QUERY`, `dude memory show ID` and `dude memory add --title T --content C` for memory, `dude diff [RUN] [PATH...]` for what a Run of your task changed, and `dude publish FILE --name NAME --description "…"` to keep a file for people (the description: one short line of what the file is for).

To give the people following this work a file — notes, a design, a report, a screenshot — run `dude publish FILE --name NAME --description "…"` (for example `dude publish notes.md --name notes.md --description "Why the export streams rows"`); the description is one short line saying what the file is for. They see each one next to the task at once, while you work, Markdown rendered; publishing the same name again adds a new version. Publish what a person would want to read; don't publish code.