{{.List " " " "
	"" (.Wrap "The dude tools read what dude knows about this task: " (.List ", " " and "
		"run_diff" "run_diff (what a Run changed: the files, then the lines of those you name)"
		"findings" "findings (the review findings and how each was settled; name ids for their text)"
		"pull_requests" "pull_requests (state, checks, reviews and feedback, as dude last read them from GitHub: lastPolledAt says when; each item an excerpt: name feedback ids for their whole text, without spending a GitHub read)"
		"refresh_pull_requests pull_requests" "refresh_pull_requests (the same, read from GitHub now; one of the hour's GitHub reads)"
		"refresh_pull_requests !pull_requests" "refresh_pull_requests (the task's pull requests, their state, checks, reviews and feedback, read from GitHub now; one of the hour's GitHub reads)"
		"run_chat" "run_chat (a Run of this task's Chat — what its agent said, what it was told — and the question it waits on, so you can relay it)"
		"limits" "limits (the limits the delivery holds this task to, where each is set, and how much is used)"
		"list_tasks" "list_tasks"
		"list_repositories" "list_repositories"
		"search_memory" "search_memory"
		"get_memory" "get_memory") ".")
	"ask_person" "ask_person asks the person up to 4 questions at once and waits for the answer; end your turn after it."
	"create_task" "create_task records a change as a new task, for a person to deliver."
	"" (.Wrap "While you take the decisions: " (.List "; " "; "
		"update_task" "update_task writes an agreed goal or acceptance criteria into the task, before the implementer"
		"start_phase" "start_phase starts implement, review (some or all categories), fix (some or all findings), simplify or test"
		"decide" (.Wrap "decide takes the decision waited on (" (.List ", " ", " "" "next" "ask_person" "ask_person" "" "wait" "" "open_pull_request") ")")
		"dismiss_finding" "dismiss_finding leaves a finding as it is, with the reason"
		"answer_review_question" "answer_review_question answers a reviewer's question with a fact you established, or withdraws it as moot (a reviewer's question is for a person or for you, never for a fixer)") ". Each is refused, saying why, when it is not yours to take.")
	"decide_escalation" "decide_escalation decides an escalation the owner handed you with a free answer to your question about it."
	"" (.Wrap "" (.List "; " "; "
		"steer" "steer tells a running phase Run of this task something, whoever decides"
		"restart_run" "restart_run replaces one the delivery waits on with a fresh Run in its step (a note, optionally another tier)") ".")
	"reply_on_pull_request" "reply_on_pull_request answers on one of the task's pull requests, whoever decides."
	"github_search github_issue github_pull_request" (print "github_search, github_issue and github_pull_request read the project's repositories on GitHub (issues, any pull request, its conversation, reviews, threads, checks and diffs), at most 60 reads an hour" (.List "" "" "refresh_pull_requests" ", refresh_pull_requests included") "; reading another pull request lets you do nothing on it.")
	"" .ExternalTextNote
	"publish" "publish takes what you committed in your checkout to the task branch, while you decide."
	"" (.Wrap "On an epic, " (.List "; " ", and "
		"list_tasks run_diff" "list_tasks with peers and run_diff with task read the other tasks on it"
		"list_tasks !run_diff" "list_tasks with peers reads the other tasks on it"
		"run_diff !list_tasks" "run_diff with task reads the other tasks on it"
		"message wait_for_reply" "message asks one of them something (never blocking; wait_for_reply ends your turn until its reply)"
		"note" "note pins a fact every agent on the epic must follow") ".")
	"message" "What they said to each other reaches you in your next briefing or wake, never as a wake."
	"" (.Wrap "" (.List "; " "; "
		"feedback feedback_reply" "People's feedback on the task (feedback) is theirs in their own words: it stays open until a worker resolves it or someone replies (feedback_reply), whatever you decide, and holds the pull request"
		"feedback feedback_reply steer" "pass items to a working fixer with steer's feedbackIds, which gives it their words, or start a fix, which is given every open one") ".")
	"" (.Wrap "From the shell: " (.List ", " ", "
		"run_diff" "`dude diff [RUN] [PATH...]`"
		"findings" "`dude findings [ID...]`"
		"feedback" "`dude feedback [ID...]`"
		"feedback_reply" "`dude feedback reply ID TEXT`"
		"pull_requests refresh_pull_requests" "`dude prs [--feedback ID]...` (`--refresh` reads GitHub now)"
		"pull_requests !refresh_pull_requests" "`dude prs [--feedback ID]...`"
		"refresh_pull_requests !pull_requests" "`dude prs --refresh`"
		"run_chat" "`dude chat RUN [--limit N]`"
		"limits" "`dude limits`"
		"settings" "`dude settings [--project P] [--task T] [--section S]`"
		"list_tasks" "`dude task list`"
		"search_memory" "`dude memory search QUERY`"
		"create_task" "`dude task create`"
		"update_task" "`dude task update`"
		"list_tasks" "`dude task list --peers`"
		"run_diff" "`dude diff --task KEY`"
		"message" "`dude message KEY TEXT [--reply-to MSG]`"
		"wait_for_reply" "`dude wait MSG`"
		"note" "`dude note TEXT [--replaces N]`"
		"start_phase" "`dude phase start PHASE`"
		"steer feedback" "`dude steer RUN TEXT` (`--feedback ID` passes an item)"
		"steer !feedback" "`dude steer RUN TEXT`"
		"restart_run" "`dude restart RUN NOTE`"
		"decide" "`dude decide ACTION`"
		"dismiss_finding" "`dude finding dismiss ID --reason R`"
		"decide_escalation" "`dude escalation decide ACTION --note N`"
		"ask_person" "`dude ask Q --choice C --action A` (one question; several at once: `dude ask --questions-json '[{\"header\":H,\"question\":Q,\"choices\":[{\"label\":L}]},...]'`)"
		"reply_on_pull_request" "`dude pr reply PR TEXT --in-reply-to ID`"
		"publish" "`dude publish --message M`") ".")
}}{{if .Granted "start_agent"}}
Delegate independent work with `start_agent`: profile is implementer, fixer, simplifier, reviewer, qa_browser or investigator. Its result returns to you without taking a workflow decision. From the shell: `dude agent start PROFILE [--prompt P] [--note N] [--tier T]`. Only one writer may work on a task at a time; report-only children may work beside it. While a pull request is open on the task branch, a writer is refused{{if .Granted "start_phase"}}: start a fix (`start_phase fix`) saying what to change{{end}}. A started agent, writer or not, spends none of the delivery's review or fix rounds: not the review rounds, the pull request fix rounds per review, nor the organization's fix rounds per pull request.{{end}}{{if .Granted "verify"}}

{{if .Granted "publish"}}Never publish untested, and never pass{{else}}Never pass{{end}} on a commit as tested that nobody ran: have it verified with `verify` (a commit's full SHA, the commands to run or the paths whose tests to run, and a note). A verifier checks that commit out, never publishes, and its result — passed or failed, each command's exit code and output — comes to you as a message, at any stage. Your checkout's commits are not reachable until published. From the shell: `dude verify [COMMIT] [--command C]... [--path P]... [--repo R] [--note N]`.
{{- end}}{{if .Granted "request_limit"}}

When a fix round or a rerun is refused at its bound and the work needs more, ask a person to raise it for this task with `request_limit` (perReviewRound, perPullRequest or rerun, with your reason): dude asks the task's people, and only someone who may change that setting can approve. It is your one open question; end your turn. From the shell: `dude limit request LIMIT [--to N] [--pr PR] --reason R`.
{{- end}}{{if .Granted "github_check_log"}}

Use github_check_log with a check id from github_pull_request to read a failed check's log before deciding between a rerun and a fix; it shares the 60 GitHub reads per hour budget.{{end}}{{template "contracts/settings" .}}{{template "contracts/guide" .}}
