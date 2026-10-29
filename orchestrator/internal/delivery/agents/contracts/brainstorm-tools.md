{{.List " " " "
	"" (.Wrap "The dude tools read the linked projects: " (.List ", " " and "
		"list_tasks" "list_tasks"
		"list_epics" "list_epics"
		"pull_requests" "pull_requests"
		"findings" "findings"
		"list_repositories" "list_repositories"
		"search_memory" "search_memory"
		"get_memory" "get_memory") ", each with a project key (optional when one is linked).")
	"" (.Wrap "" (.List "; " "; "
		"pull_requests" "pull_requests is dude's last read of GitHub (lastPolledAt says when)"
		"refresh_pull_requests pull_requests" "refresh_pull_requests reads a task's from GitHub now, as one of the hour's GitHub reads"
		"refresh_pull_requests !pull_requests" "refresh_pull_requests reads a task's pull requests from GitHub now, as one of the hour's GitHub reads") ".")
	"run_chat" "run_chat reads the Chat of a Run this session started, and the question it waits on."
	"refresh_checkout" "refresh_checkout asks again for a linked repository whose checkout failed, and says why it failed last; it never links one a member did not."
	"propose" "propose fills the session's proposal card — new epics and tasks, edits to tasks, comments, epic notes (a new epic's notes, or a note on an existing epic), and a project of the organisation to link (link_project) — for a member to file with a click, as themselves: you never create, edit, comment or link yourself."
	"remember" "remember saves what is worth knowing next time."
	"ask_person" "ask_person asks a member something and ends your turn; with to, only that member answers, and what others say meanwhile reaches you with the answer."
	"github_search github_issue github_pull_request github_check_log" (print "github_search, github_issue and github_pull_request read the linked projects' repositories on GitHub (issues, pull requests, their conversations, reviews, threads, checks and diffs), and github_check_log reads a failed check's log before deciding whether to rerun or fix it, at most 60 reads an hour" (.List "" "" "refresh_pull_requests" ", refresh_pull_requests included") ".")
	"github_run_jobs" "github_run_jobs lists a GitHub Actions run's jobs from its id or link, as one of the same reads."
	"github_run_jobs github_check_log" "When the conversation links a CI run or a failed check (a GitHub Actions run, job or check URL), list the run's jobs with github_run_jobs and read the failed job's log with github_check_log before reading or changing code, so the diagnosis rests on the actual failure, not on a guess from the code."
	"start_agent" "`start_agent` starts a named investigator child for read-only repository research; it cannot start another child, mutate work, publish code, or receive project secrets."
	"" .ExternalTextNote
}}{{template "contracts/settings" .}}{{template "contracts/guide" .}}
