{{define "notices/checks"}}Your checks report was not verified. Nothing from this Run has been pushed.
{{range .Checks}}
- {{.Command}}: {{.Reason}}{{with .JudgeReason}} — {{.}}{{end}}{{with .Decision}} (verdict: {{.}}){{end}}
{{end}}
{{- if .Failed}}
A reported failure that predates this task passes once this Run's own tool calls show the same command failing the same way at the commit of the target branch this task builds on{{with .Bases}} ({{.}}){{end}}: run it in a separate checkout of that commit (for example `git worktree add` of it beside your checkout), not by setting your change aside, since the commit you started from holds this task's earlier work, and report both results in its evidence. A failure that commit does not have, or one in code this task changed, must be fixed.
{{end}}
Run the missing or unsuccessful checks as separate commands, read their output, and correct the report. End your next reply with a checks: YAML block. If you cannot proceed, ask a person what you need.{{end}}
{{define "notices/checks/no-conductor"}}no conductor: only a person can pass a reported failure{{end}}
{{define "notices/checks/unconducted-reject"}}not verified by the conductor: only a person can pass a reported failure{{end}}
{{define "notices/checks/conductor-rejected"}}not passed by the conductor: {{.}}{{end}}
{{define "notices/checks/many"}}the report lists {{.Count}} checks, more than the {{.Max}} that are verified; report at most {{.Max}}, one per command you ran, not one per test or file{{end}}
{{define "notices/checks/skip"}}Checks not verified: no conductor decides this delivery. Hand the task's decisions to its conductor to have checks reports verified before publication.{{end}}
{{define "notices/checks/unconducted"}}Checks not verified by the conductor: it did not decide the report in time.{{end}}
{{define "notices/checks/unparseable"}}missing or unparseable checks YAML block{{with .Syntax}}: the YAML does not parse ({{.}}). Wrap every command and evidence value in single quotes, doubling any ' inside: evidence: 'it''s done: exit 0'{{end}}{{if .Shape}}: checks must be a list of entries, each starting with "- " and holding command, result and evidence{{end}}{{end}}
{{define "notices/checks/unchanged"}}No changes; checks not needed. When the turn ended, every repository this Run may push was at the commit it started from, with nothing staged, unstaged or untracked.{{end}}
{{define "notices/checks/account-compact"}}- **{{.Role}}** — {{.Status}}
  - {{.Count}} {{if eq .Count 1}}check{{else}}checks{{end}} reported; checks omitted from this account: see the task's Activity.{{end}}
{{define "notices/checks/account-omitted"}}Checks account omitted: see the task's Activity.{{end}}
{{define "notices/checks/account-judged"}}Checks were verified against the Run's recorded commands; the conductor passed failures that fail the same at the commit of the target branch the task builds on (pre-existing) and checks a later phase runs (deferred).{{end}}
{{define "notices/checks/account-carried"}}- {{.Role}}: carried the {{.From}}'s waiver of the same failure: {{.Note}}{{end}}
{{define "notices/checks/account-carried-from"}}carried from {{.By}}'s waiver of the {{.Role}}'s report ({{.Run}}){{end}}
{{define "notices/checks/writer-carried-from"}}carried from {{.By}}'s waiver of the {{.Role}}'s report ({{.Run}}, turn {{.Turn}}): {{.Note}}{{end}}
