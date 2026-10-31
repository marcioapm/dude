package delivery

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// goldenLines renders named texts as one golden: each under its name, so a
// change to one shows as a diff under it.
func goldenLines(t *testing.T, name string, lines [][2]string) {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("## " + l[0] + "\n\n" + l[1] + "\n\n")
	}
	golden(t, name, b.String())
}

// What the conductor's tools answer, past the decision results the
// conductor-result-* goldens render through the database.
func TestGoldenToolResults(t *testing.T) {
	goldenLines(t, "results-delivery", [][2]string{
		{"pr-phase-started", prPhaseStarted(PhaseReview)},
		{"escalation-decided", EscalationDecidedNext(false)},
		{"escalation-decided-amend", EscalationDecidedNext(true)},
		{"gate-choices", strings.Join(GateChoices, "\n")},
	})
}

// The reasons dude wakes the conductor with, one line each, and the note
// they are delivered in.
func TestGoldenWakeLines(t *testing.T) {
	goldenLines(t, "wake-lines", [][2]string{
		{"decision-waiting-start", decisionWaitingNote(PointStart, "")},
		{"decision-waiting-implemented", decisionWaitingNote(PointImplemented, "Run run_impl finished: 2 files changed.")},
		{"decision-waiting-reviewed", decisionWaitingNote(PointReviewed, "")},
		{"decision-waiting-fixed", decisionWaitingNote(PointFixed, "")},
		{"decision-waiting-before-pr", decisionWaitingNote(PointBeforePR, "")},
		{"decision-waiting-pr-feedback", decisionWaitingNote(PointPRFeedback, "")},
		{"decision-waiting-unknown", decisionWaitingNote("somewhere", "")},
		{"run-failed", runFailedLine("review · security", "run_rev", "lux: the host\nwent away "+strings.Repeat("x", 130))},
		{"escalated-stuck", escalatedLine(&Escalation{Reason: "stuck", Step: "fix"})},
		{"escalated-test-failed", escalatedLine(&Escalation{Reason: ReasonTestFailedCriteria, Step: "fix", Detail: map[string]any{
			"criteria": []map[string]any{{"criterion": 2, "status": "fail", "text": "Paying sends\na receipt"}}}})},
		{"escalated-test-blocked", escalatedLine(&Escalation{Reason: ReasonTestBlocked, Step: "test", Detail: map[string]any{
			"criteria": []map[string]any{{"criterion": 1, "status": "blocked", "text": "The export downloads"}}}})},
		{"escalated-checks", escalatedLine(&Escalation{Reason: ReasonChecksUnverified, Detail: map[string]any{"runId": "run_impl",
			"message": "Your checks report was not verified. Nothing from this Run has been pushed.\n\n" +
				"- npm run test:legacy: not-run — not passed by the judge: no later phase runs the legacy Vue jest suite (judgement: no)\n" +
				"- missing checks: the legacy Vue jest run covers the changed component (judgement: yes)\n\nRun the missing or unsuccessful checks."}})},
		{"escalated-checks-many", escalatedLine(&Escalation{Reason: ReasonChecksUnverified, Detail: map[string]any{"runId": "run_impl",
			"message": "held", "unverified": slices.Repeat([]map[string]any{{"command": "make check", "reason": "exit 1"}}, escalatedItemsMax+3)}})},
		{"escalation-answered", escalationAnsweredLine("qst_1", "retry")},
		{"escalation-on-banner", escalationOnBannerLine("accept", "qst_2", false, "")},
		{"escalation-on-banner-said", escalationOnBannerLine("retry", "qst_2", false, "The task's owner, Ola Nordmann, said:\n\nSync to main.")},
		{"escalation-checks-finished", escalationOnBannerLine("wait", "qst_2", true, "")},
		{"steer-read", steerReadLine("implement", "run_impl", "dir_1")},
		{"steer-failed", steerFailedLine("dir_1", "fix", "run_fix", "the Run\nended "+strings.Repeat("y", 130))},
		{"pr-merged", closeOutLine("api", 12, forge.StateMerged)},
		{"pr-closed", closeOutLine("api", 12, forge.StateClosed)},
		{"published", publishedLine("pub_1", []string{"api@1111111", "web@2222222"}, 40, 3, nil)},
		{"published-in-part", publishedLine("pub_1", []string{"api@1111111"}, 40, 3, []string{"web: the branch moved"})},
		{"publish-refused", publishRefusedLine("pub_2", "the checkout\nis behind "+strings.Repeat("z", 230))},
		{"publish-stalled", publishStalledLine("pub_3", []string{"api to 1111111", "web to 2222222"})},
		{"unblocked", unblockedLine("GLD-8", "GLD-7", false)},
		{"unblocked-auto", unblockedLine("GLD-8", "GLD-7", true)},
		{"run-asks", runAsksLine("implement", "run_impl", "qst_3")},
		{"review-question", OpenReviewQuestion{ID: "rvq_1", RunID: "run_rev", Category: "correctness", NeededFrom: "product",
			Question: "Do invoices close in UTC\nor in the organisation's zone? " + strings.Repeat("w", 80)}.wakeLine()},
		{"review-question-no-needed-from", OpenReviewQuestion{ID: "rvq_2", RunID: "run_rev", Category: "security", Question: "Who may export?"}.wakeLine()},
		{"review-question-answered", reviewQuestionAnsweredLine("rvq_1", "In UTC.\nAlways.", false)},
		{"review-question-withdrawn", reviewQuestionAnsweredLine("rvq_1", "the code is gone", true)},
		{"base-moved", baseMovedLine("main", "GLD-8", 14)},
		{"wait-held", renderWaitDigest(waitEval{held: 2, of: 2, waitedAll: true, items: []waitItem{
			{Kind: "run", Who: "review", RunID: "run_rev", Status: "failed", Ended: true, Error: "lux: the host\nwent away " + strings.Repeat("x", 130), rank: rankBad},
			{Kind: "pr", PR: "api#12", Head: headA, State: "open", Checks: "failing", Failing: []string{"lint", "unit"}, rank: rankBad},
			{Kind: "run", Who: "implement", RunID: "run_impl2", Was: "run_impl", Status: "completed", Ended: true, rank: rankGood},
			{Kind: "pr", PR: "web#3", Head: headA, State: "open", Checks: "passing", rank: rankGood}}},
			"cwt_1", false, false, nil, "")},
		{"wait-deadline", renderWaitDigest(waitEval{held: 1, of: 2, waitedAll: true, items: []waitItem{
			{Kind: "pr", PR: "api#12", Head: headA, State: "open", MovedTo: headB, rank: rankBad},
			{Kind: "run", Who: "investigator", RunID: "run_inv", Status: "running", rank: rankWaiting},
			{Kind: "pr", PR: "web#3", Head: headA, State: "open", Checks: "unknown", Polled: "2026-10-30T12:00:00Z", rank: rankWaiting}}},
			"cwt_2", true, false, nil, "then decide")},
		{"wait-any-meanwhile", renderWaitDigest(waitEval{held: 1, of: 2, items: []waitItem{
			{Kind: "pr", PR: "api#12", Head: headA, State: "merged", rank: rankGood},
			{Kind: "pr", PR: "web#3", Head: headA, State: "closed", rank: rankBad}}},
			"cwt_3", false, false, []waitCount{{"commented", 2}, {"reviewed", 1}, {"feedback", 1}, {"messages", 3}}, "")},
		{"wait-timer", renderWaitDigest(waitEval{waitedAll: true}, "cwt_4", true, false, []waitCount{{"messages", 1}}, "check the deploy")},
		{"wait-holds-now", renderWaitDigest(waitEval{held: 1, of: 1, waitedAll: true, items: []waitItem{
			{Kind: "run", Who: "verifier", RunID: "run_ver", Status: "completed", Ended: true, rank: rankGood}}}, "", false, true, nil, "")},
		{"recovered", recoveredLine(RecoveryEnd{ID: "rcv_1", RunID: "run_impl", Branch: "dude/GLD-8/recovered-run_impl", Status: RecoveryDone,
			Results: []RecoveredRepo{{Repo: "api", Status: "pushed", Commit: headA}, {Repo: "web", Status: "rejected", Error: "the branch moved"}},
			Dirty:   []string{"api"}})},
		{"recovered-nothing", recoveredLine(RecoveryEnd{ID: "rcv_2", RunID: "run_impl", Branch: "dude/GLD-8/recovered-run_impl",
			Status: RecoveryFailed, Error: "nothing committed beyond its start"})},
		{"recovered-secret", recoveredLine(RecoveryEnd{ID: "rcv_3", RunID: "run_impl", Branch: "dude/GLD-8/recovered-run_impl",
			Status: RecoveryRefused, Error: "its work contains the project secret IMPL_TOKEN in config/app.toml; a person must remove it"})},
		{"stalled", goldenStall().Text(ConductorOptions(false))},
		{"stalled-quiet", goldenQuietStall().Text(ConductorOptions(false))},
		{"stalled-child", goldenStall().Text(ConductorOptions(true))},
		{"note-one", WakeNote([]string{"A decision waits."})},
		{"note-many", WakeNote([]string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"})},
	})
}

// What the checks gate asks the judge about a person's attempt waiver, and
// what it says of a failure the waiver did not cover.
func TestGoldenChecksWaiverTexts(t *testing.T) {
	w := map[string]any{"Role": "Implementer", "RunID": "run_impl", "Note": "Needs a hardware clock\nCI lacks.",
		"Command": "make test", "Evidence": "TestClock needs a hardware clock", "Calls": []string{"- bash {\"command\":\"make test\"}\n  status: error, exit code 1"},
		"Omitted": 3}
	goldenLines(t, "checks-waiver", [][2]string{
		{"question", MustText("notices/checks/waived").Render(Check{Command: "make test", Result: CheckFail})},
		{"context", MustText("notices/checks/waived-context").Render(w)},
		{"not-covered", MustText("notices/checks/not-covered").Render(map[string]string{"Role": "Implementer", "Reason": "TestGreeting fails too"})},
	})
}

// What the checks gate asks the judge about a reported failure or not-run
// check no waiver covers, and the send-back of one it did not pass.
func TestGoldenChecksPreexistingTexts(t *testing.T) {
	type q struct {
		Check
		TestPhase bool
	}
	failed := Check{Command: "npx tsc --noEmit", Result: CheckFail, Evidence: "95 errors on the parent, 94 with the change"}
	browser := Check{Command: "open the settings page", Result: CheckNotRun, Evidence: "no browser"}
	type verdict struct{ Command, Result, Reason, WaiverReason, JudgeReason, Decision string }
	goldenLines(t, "checks-preexisting", [][2]string{
		{"fail", MustText("notices/checks/preexisting").Render(q{failed, true})},
		{"not-run with a test phase", MustText("notices/checks/preexisting").Render(q{browser, true})},
		{"not-run without a test phase", MustText("notices/checks/preexisting").Render(q{browser, false})},
		{"send-back", MustText("notices/checks").Render(map[string]any{"Failed": true, "Bases": "api: main at 1111111, web: release/2 at 2222222",
			"Checks": []verdict{{Command: failed.Command, Result: CheckFail, Reason: failed.Evidence, Decision: "unsure",
				JudgeReason: MustText("notices/checks/not-preexisting").Render("no run at the task's base is recorded")}}})},
	})
}

// A stalled Run with everything a stall says: open calls of each kind, a
// child waiting on a permission, processes, usage, files, tools, plan.
func goldenStall() Stall {
	cpu, net := 12.0, int64(3<<20)
	changed := time.Date(2026, 1, 2, 10, 15, 0, 0, time.UTC)
	return Stall{RunID: "run_impl", Role: "implementer", Phase: "implement", Round: 2, Category: "security", Tier: "fast",
		RunningSecs: 5400, WindowSecs: 900, Reasons: []string{"call", "silent"}, SilentSecs: 840,
		Calls: []OpenCall{{Tool: "bash", Input: "go test ./...", OpenSecs: 900, Command: true, Alive: true},
			{Tool: "task", Input: "explore", OpenSecs: 600},
			{Tool: "bash", Input: "sleep 999", OpenSecs: 30, Command: true}},
		Children: []ChildFact{{Tool: "task", Title: "explore", Kind: "subagent", ChildID: "ses_child_0123456789",
			LastEvent: "asked to run make", LastEventSecs: 300,
			WaitingOn: &WaitingOn{Permission: "bash", Patterns: []string{"make *", "rm *"}}}},
		Processes: []Process{{PID: 1, Elapsed: "15:00", CPU: 99.5, Args: "go test ./..."}, {PID: 2, Elapsed: "1:00", CPU: 0, Args: "sh"}},
		CPUSecs:   &cpu, NetBytes: &net, UsageSecs: 900,
		FilesChangedAt: &changed, ToolsInWindow: map[string]int{"bash": 3, "read": 1},
		Plan: []string{"write the export", "test it"}, LastMessage: "Running the tests.",
	}
}

// A stalled reviewer with no calls, processes unreadable and no usage.
func goldenQuietStall() Stall {
	return Stall{RunID: "run_rev", Role: "reviewer", Phase: "review", RunningSecs: 50, WindowSecs: 7200,
		Reasons: []string{"silent"}, SilentSecs: 7200, PSError: "ps: not found",
		Calls: []OpenCall{{Tool: "bash", Input: "x", OpenSecs: 7200, Command: true}}}
}

// What an agent is told about its peers' messages and the epic's notes.
func TestGoldenCoordinationNotices(t *testing.T) {
	goldenLines(t, "notices-coordination", [][2]string{
		{"message", MessageDirective("GLD-8's implementer", "pm_1", "", "Which endpoint?\nThe month one.")},
		{"reply", MessageDirective("GLD-8's conductor", "pm_2", "pm_1", "GET /invoices")},
		{"held", heldMessageNotice("GLD-9")},
		{"already-answered", alreadyAnsweredNotice("pm_2")},
		{"peer-ended", PeerEndedLine("GLD-9", "awaiting_input")},
		{"note-added", noteAddedLine(3, "Ana", "Amounts are integer cents.")},
		{"note-replaced", noteReplacedLine(2, 4, "GLD-8's implementer", "Columns: number, total", "Old columns.")},
		{"note-retired", noteRetiredLine(1, "Bo", "Use UTC.")},
		{"notes-limit", NotesLimitText},
	})
}

// What a fixer integrating its base, and a reviewer of the merge, are told.
func TestGoldenIntegrationBriefs(t *testing.T) {
	i := &Integration{Repo: "api", Base: "main", BaseHead: "0123456789abcdef", MergeSHA: "fedcba9876543210"}
	goldenLines(t, "integration-briefs", [][2]string{
		{"fix", i.brief()},
		{"review", integrationReviewBrief(i.MergeSHA)},
		{"merge-when", IntegrationMergeWhen("release/2", "abc123")},
	})
}

// The replay's heading, lead and every line an event renders as.
func TestGoldenReplay(t *testing.T) {
	events := []replayEvent{
		ev("chat.message", "run_c", "Ana", `{"text":"Ship it? ![the toolbar](attachment:att_1)","attachments":[{"id":"att_2","name":"plan.png"},{"id":"att_3"}]}`),
		ev("chat.message", "run_c", "", `{"text":"Done.","by":"conductor","github":{"repo":"api","number":12}}`),
		ev("chat.message", "run_c", "", `{"text":"LGTM","github":{"login":"bo"}}`),
		{Type: "run.steered", RunID: "run_c", ActorID: "github:cy", Payload: map[string]any{"text": "steer words"}, Heard: true},
		{Type: "run.steered", RunID: "run_c", Payload: map[string]any{"text": "from the conductor", "by": "conductor"}, Heard: true},
		{Type: "agent.message", RunID: "run_c", Payload: map[string]any{"text": "never heard"}},
		ev("agent.message", "run_c", "", `{"text":"On it."}`),
		ev("question.asked", "run_c", "", `{"prompt":"Old style?"}`),
		ev("question.asked", "run_c", "", `{"items":[{"header":"Scope","question":"Which?","choices":[{"label":"A"},{"label":"B"}]},
			{"header":"When","question":"Now?"}]}`),
		ev("question.answered", "run_c", "Ana", `{"answer":"A","note":"and soon"}`),
		ev("question.closed", "run_c", "", `{"by":"withdrawn"}`),
		ev("question.closed", "run_c", "", `{"by":"decision"}`),
		ev("conductor.woken", "run_c", "", `{"text":"dude woke you; one thing:\n- A decision waits."}`),
		ev("conductor.decided", "run_c", "", `{"point":"after_review","action":"start_phase","phase":"fix","note":"the\nrounding"}`),
		ev("conductor.decided", "run_c", "", `{"point":"elsewhere","action":"next"}`),
		ev("review.finding_resolved", "run_c", "", `{"by":"conductor","findingId":"fnd_1","note":"Dismissed by the conductor: out of scope"}`),
		ev("task.updated", "run_c", "", `{"by":"conductor","goal":"g","acceptanceCriteria":[]}`),
		ev("session.renamed", "run_c", "Bo", `{"title":"Usage","by":"per_bo"}`),
		ev("session.proposed", "run_c", "", `{"proposalId":"pp1","items":[{"kind":"epic","project":"BL","title":"E"},
			{"kind":"edit","task":"BL-1"},{"kind":"note","project":"BL","epic":"E","text":"n"},
			{"kind":"note","project":"BL","epic":"E","text":"n","replaces":2},{"kind":"relation"}]}`),
		ev("session.filed", "", "", `{"proposalId":"pp0","filed":[{"item":1,"key":"BL-9"}]}`),
		ev("agent.session.replaced", "run_c", "", `{}`),
		ev("agent.tool.called", "run_c", "", `{"tool":"read","callId":"c9","input":{"path":"x"}}`),
		ev("agent.tool.completed", "run_c", "", `{"callId":"c8","status":"completed"}`),
		ev("agent.message", "run_d", "", `{"text":"A new Run."}`),
	}
	body := renderReplay(events)
	goldenLines(t, "replay", [][2]string{
		{"heading", replayHeading(body)},
		{"summary", joinReplay(replayStart{summary: "  We agreed on CSV.  ", run: "run_c"}, replayEntries(events[6:7]), 3)},
	})
}

// How a refused machine move is said, to the agent and to a person.
func TestGoldenMoveRefusals(t *testing.T) {
	pool := "highmem"
	xl := Machine{Name: "XL", CPUs: 8, MemoryMiB: 32768, DiskGiB: 80, Pool: &pool}
	large := Machine{Name: "Large", CPUs: 4.5, MemoryMiB: 1536}
	var lines [][2]string
	for _, c := range []struct {
		name string
		r    MoveRefusal
	}{
		{"old-lux-pools", MoveRefusal{Status: 400, Size: xl, OtherPool: true}},
		{"old-lux-stopped", MoveRefusal{Status: 400, Size: large}},
		{"unknown-pool", MoveRefusal{Status: 422, Code: "unknown_pool", Size: xl}},
		{"platform", MoveRefusal{Status: 422, Code: "platform_mismatch", Size: xl, PoolPlatforms: []string{"linux/arm64", "linux/riscv"},
			ImagePlatforms: []string{"linux/amd64"}}},
		{"platform-an", MoveRefusal{Status: 422, Code: "platform_mismatch", Size: xl, PoolPlatforms: []string{"linux/amd64"},
			ImagePlatforms: []string{"linux/arm64"}}},
		{"platform-unknown", MoveRefusal{Status: 422, Code: "platform_mismatch", Size: large}},
		{"nested", MoveRefusal{Status: 422, Code: "pool_unfit", Size: xl, NoNested: true}},
		{"nested-said", MoveRefusal{Status: 422, Code: "pool_unfit", Size: large, luxMessage: "no nested containers here"}},
		{"too-large", MoveRefusal{Status: 422, Code: "pool_unfit", Size: large}},
		{"invalid-spec", MoveRefusal{Status: 422, Code: "invalid_spec", Size: xl}},
		{"not-resumable", MoveRefusal{Status: 409, Code: "not_resumable", Size: xl}},
		{"other", MoveRefusal{Status: 500, Code: "boom", Size: xl}},
	} {
		lines = append(lines, [2]string{c.name + " (agent)", c.r.ForAgent()}, [2]string{c.name + " (person)", c.r.ForPerson()})
	}
	goldenLines(t, "move-refusals", lines)
}

// Every refusal format under agents/refusals/tools and /delivery, in the
// golden's order, then any the golden lacks.
func TestGoldenRefusals(t *testing.T) {
	var lines [][2]string
	for _, prefix := range []string{"refusals/tools/", "refusals/delivery/"} {
		lines = append(lines, refusalLines(prefix, "refusals")...)
	}
	goldenLines(t, "refusals", lines)
}

// refusalLines are the {{define}}s under prefix, ordered as golden lists
// them, then any it lacks by name.
func refusalLines(prefix, golden string) [][2]string {
	var keys []string
	for _, tm := range agentTemplates.Templates() {
		if name := tm.Name(); strings.HasPrefix(name, prefix) && strings.Count(name, "/") == 3 {
			keys = append(keys, name)
		}
	}
	want, _ := os.ReadFile(filepath.Join("agents", "testdata", golden+".golden.md"))
	order := map[string]int{}
	for _, line := range strings.Split(string(want), "\n") {
		if key, ok := strings.CutPrefix(line, "## "); ok {
			order[key] = len(order)
		}
	}
	slices.SortFunc(keys, func(a, b string) int {
		ia, oka := order[a]
		ib, okb := order[b]
		switch {
		case oka && okb:
			return ia - ib
		case oka != okb:
			if oka {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	var lines [][2]string
	for _, key := range keys {
		lines = append(lines, [2]string{key, render(mustTemplate(key), nil)})
	}
	return lines
}

// Every refusal format under agents/refusals/domain/, in the golden's order.
func TestGoldenDomainRefusals(t *testing.T) {
	goldenLines(t, "refusals-domain", refusalLines("refusals/domain/", "refusals-domain"))
}
