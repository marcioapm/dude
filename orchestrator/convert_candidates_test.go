package orchestrator_test

// Every fix reaches its pull request directly: the boot sweep moves every
// delivery still on the candidate path to direct fixes (delivery.ConvertCandidates),
// whatever state its candidate is in, and a delivery's step entry converts
// one the sweep left. Through the real workflow, API and fake lux and GitHub.

import (
	"context"
	"encoding/json"
	"maps"
	"os/exec"
	"sync"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
)

// convertCandidates runs the sweep once, and returns its counts.
func (w *world) convertCandidates() delivery.CandidatesConverted {
	w.t.Helper()
	n, err := delivery.ConvertCandidates(context.Background(), w.app)
	if err != nil {
		w.t.Fatal(err)
	}
	return n
}

// candidateWorld is a pull request fix world whose delivery froze
// candidate fixes, as every delivery did before direct ones.
func candidateWorld(t *testing.T) *prFixWorld {
	t.Helper()
	w := newPRFixWorld(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{policy,pullRequestFixes}', '"candidate"') WHERE task_id = $1`, w.wi)
	return w
}

// midCandidateReview is a candidate world whose person's item (fbi_x) a
// fixer resolved on the candidate, now under its candidate's review, which
// hangs: returns the hanging review Run and the candidate's From. No live
// path opens a candidate now, so the delivery is stored as a dude from
// before the candidate path was deleted left it: the direct fix and its
// narrow review run, then the fix's commit is moved to the candidate
// branch, the pull request's head put back where it was, and the state,
// Runs and resolution written as the candidate's.
func midCandidateReview(t *testing.T) (*prFixWorld, string, string, map[string]string) {
	t.Helper()
	w := candidateWorld(t)
	ana := w.person("Ana")
	w.assignOwner(w.wi, ana)
	w.resolveAsItPublishes(w.wi, "fbi_x", itsNewHead, nil)
	w.play = func(phase string, n int, b *fakelux.Behaviour) {
		if phase == "review" && n == 1 {
			b.Hang = true
		}
	}
	w.armed.Store(true)
	if code, body := w.feedback(ana, w.wi, "fbi_x", "Log the retry count at info, not debug."); code != 201 {
		t.Fatalf("submit: %d %v", code, body)
	}
	var review string
	w.until("the fix's review at work", func() bool {
		review = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'running'
			AND lux_state = 'running'`, w.wi)
		return review != ""
	})
	if status, _ := w.resolution("fbi_x"); status != "resolved" {
		t.Fatalf("fbi_x is %s, want resolved by the fix", status)
	}
	fixed := w.gh.SHA(w.branch)
	if fixed == w.before {
		t.Fatalf("the fix never published")
	}
	branch := delivery.CandidateBranch(w.wi, 1)
	for _, args := range [][]string{{"update-ref", "refs/heads/" + branch, fixed, ""}, {"update-ref", "refs/heads/" + w.branch, w.before, fixed}} {
		if out, err := exec.Command("git", append([]string{"-C", w.gh.Repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	from := map[string]string{"target": w.before}
	mustExec(t, w.owner, `UPDATE runs SET branch = $2 WHERE task_id = $1 AND kind = 'agent'
		AND created_at >= (SELECT max(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix')`, w.wi, branch)
	mustExec(t, w.owner, `UPDATE feedback_items SET resolved_verified_at = NULL WHERE id = 'fbi_x'`)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state - 'fixRange' || jsonb_build_object('candidate',
		jsonb_build_object('round', 1, 'branch', $2::text, 'from', $3::jsonb, 'base', $3::jsonb)) WHERE task_id = $1`,
		w.wi, branch, `{"target":"`+w.before+`"}`)
	var c delivery.Candidate
	if err := json.Unmarshal([]byte(w.state("candidate")), &c); err != nil || c.Branch != branch {
		t.Fatalf("no candidate under review: %q %v\n%s", w.state("candidate"), err, w.describeRuns())
	}
	return w, ana, review, from
}

// noCandidateTrace checks the delivery carries nothing of the candidate
// path the sweep selects on (delivery.CandidateTraceSQL): the retired key
// out of its policy, no candidate, hand-over, integration polls or
// promotion step.
func (w *prFixWorld) noCandidateTrace() {
	w.t.Helper()
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND `+delivery.CandidateTraceSQL, w.wi); n != 0 {
		w.t.Errorf("a trace of the candidate path is left: step %s, %s", w.step(), w.str(`SELECT state::text FROM workflow_runs WHERE task_id = $1`, w.wi))
	}
}

func (w *prFixWorld) step() string {
	return w.str(`SELECT step FROM workflow_runs WHERE task_id = $1`, w.wi)
}

// stopped has ana stop the delivery, and waits for it to stop.
func (w *prFixWorld) stopped(ana string) {
	w.t.Helper()
	w.stop(ana, w.wi, map[string]any{"note": "hold on"})
	w.until("stopped", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
}

// headsBackAt checks the delivery's heads are where its candidate started.
func (w *prFixWorld) headsBackAt(from map[string]string) {
	w.t.Helper()
	var got map[string]string
	_ = json.Unmarshal([]byte(w.state("heads")), &got)
	if !maps.Equal(got, from) {
		w.t.Errorf("heads %v, want the candidate's from %v", got, from)
	}
}

// directlyFixedAgain waits for the fix the sweep sent the delivery to: a
// fix Run with no branch of its own, given fbi_x, published to the pull
// request's head; the candidate branch still on GitHub.
func (w *prFixWorld) directlyFixedAgain(after string) {
	w.t.Helper()
	candidate := w.gh.SHA(delivery.CandidateBranch(w.wi, 1))
	var fix string
	w.until("the direct fix published", func() bool {
		w.sync()
		fix = w.str(`SELECT COALESCE(max(id), '') FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'
			AND COALESCE(branch, $2) = $2 AND created_at > (SELECT created_at FROM runs WHERE id = $3)`, w.wi, w.branch, after)
		return fix != "" && w.gh.SHA(w.branch) != w.before
	})
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND 'fbi_x' = ANY(feedback_item_ids)`, fix); n != 1 {
		w.t.Errorf("the new fixer %s is not given the reopened item", fix)
	}
	if got := w.str(`SELECT COALESCE(heads->'target'->>'sha', '') FROM runs WHERE id = $1`, fix); got == "" || got != w.gh.SHA(w.branch) {
		w.t.Errorf("the pull request's head %s is not the new fix's %q", w.gh.SHA(w.branch), got)
	}
	if candidate == "" || w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != candidate {
		w.t.Errorf("the candidate branch went: was %q, now %q", candidate, w.gh.SHA(delivery.CandidateBranch(w.wi, 1)))
	}
	if w.state("candidate") != "" {
		w.t.Errorf("a candidate again: %s", w.state("candidate"))
	}
}

// candidateDropped checks what the sweep did to a delivery with a candidate:
// no candidate, its heads back where the candidate started, direct, the
// item reopened, and one notice in Chat.
func (w *prFixWorld) candidateDropped(from map[string]string) {
	w.t.Helper()
	if w.state("candidate") != "" {
		w.t.Errorf("the candidate is still in state: %s", w.state("candidate"))
	}
	w.headsBackAt(from)
	w.noCandidateTrace()
	if status, _ := w.resolution("fbi_x"); status != "open" {
		w.t.Errorf("fbi_x is %s, want reopened", status)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'candidate_converted'`, w.wi); n != 1 {
		w.t.Errorf("%d notices of the conversion, want 1", n)
	}
}

// State B (§3.3's world test): a delivery mid-candidate-review. Its review
// is aborted, the candidate gone, its heads where the candidate started,
// and its fix is redone directly: the new fix Run publishes to the pull
// request's head, with the reopened item; the candidate branch stays.
func TestConvertCandidatesRedoesAFixUnderReviewDirectly(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	if n := w.convertCandidates(); n.Converted != 1 || n.StoppedMidPromotion != 0 || n.Left != 0 {
		t.Fatalf("swept %+v, want one converted", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND NOT keep`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted\n%s", w.describeRuns())
	}
	if got := w.step(); got != "prFix" {
		t.Errorf("step %s, want prFix", got)
	}
	w.candidateDropped(from)
	w.directlyFixedAgain(review)
	// Converted once: a second sweep finds nothing.
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
}

// A delivery whose step the old pod still leases when the sweep runs is
// left (CandidateLeft), and no sweep comes back for it: once the lease
// lapses and the workflow resumes it, its step's entry converts it as the
// sweep would have, before any step runs on its candidate. Its fix is
// redone directly; the candidate's unreviewed fix never reaches the pull
// request's head.
func TestConvertCandidatesLeftByTheSweepIsConvertedOnResume(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	unreviewed := w.gh.SHA(delivery.CandidateBranch(w.wi, 1))
	mustExec(t, w.owner, `UPDATE workflow_runs SET locked_by = 'old-pod', locked_until = now() + interval '1 hour' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{Left: 1}) {
		t.Fatalf("swept %+v, want it left to the old pod", n)
	}
	if w.state("candidate") == "" {
		t.Fatalf("the sweep converted a leased delivery")
	}
	// The old pod is gone: its lease lapses, and the workflow resumes the
	// step it was at. The pull request's record shows its head as GitHub
	// does (a candidate never moved it).
	w.sync()
	mustExec(t, w.owner, `UPDATE workflow_runs SET locked_by = NULL, locked_until = now() - interval '1 second',
		status = 'waiting', wake_at = now() WHERE task_id = $1`, w.wi)
	w.until("converted on resume", func() bool { return w.state("candidate") == "" })
	// Converted as the sweep converts: one notice, its heads put back
	// where the candidate started, the item reopened for the redo, and
	// nothing traced left.
	w.noCandidateTrace()
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'candidate_converted'
		AND payload->'heads' = $2::jsonb`, w.wi, `{"target":"`+w.before+`"}`); n != 1 {
		t.Errorf("%d notices of the conversion with the candidate's from, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted\n%s", w.describeRuns())
	}
	// Nothing of the candidate's reached the pull request's head: the
	// first Run after the conversion is the direct fix, started from the
	// candidate's from (heads == candidate.from) with the reopened item.
	var first string
	w.until("the first Run after the conversion", func() bool {
		first = w.str(`SELECT COALESCE((SELECT id FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL
			AND created_at > (SELECT min(e.occurred_at) FROM events e WHERE e.task_id = $1 AND e.payload->>'about' = 'candidate_converted')
			ORDER BY created_at, id LIMIT 1), '')`, w.wi)
		return first != ""
	})
	if got := w.str(`SELECT phase::text || ' ' || COALESCE(base_refs->>'target', '') || ' ' || ('fbi_x' = ANY(feedback_item_ids))::text
		FROM runs WHERE id = $1`, first); got != "fix "+w.before+" true" {
		t.Errorf("the first Run after the conversion is %q, want a fix from %s given fbi_x\n%s", got, from["target"],
			w.str(`SELECT string_agg(phase::text || ' ' || created_at::text || ' ' || COALESCE(base_refs->>'target', '') || ' ' || COALESCE(creation_key, ''), E'\n' ORDER BY created_at) FROM runs WHERE task_id = $1`, w.wi)+"\n"+
				w.str(`SELECT string_agg(event_type || ' ' || occurred_at::text || ' ' || COALESCE(payload->>'about', ''), E'\n' ORDER BY occurred_at) FROM events WHERE task_id = $1`, w.wi))
	}
	w.directlyFixedAgain(review)
	if got := w.gh.SHA(w.branch); got == unreviewed {
		t.Errorf("the candidate's unreviewed fix %s reached the pull request's head", unreviewed)
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
}

// A delivery the sweep left whose pull request merges before its lease
// lapses: the pr.ended that wakes it ends it (endable) before its step's
// entry converts it, as the sweep's conversion would leave it to. The
// signal is not consumed by the conversion's move to the redo: no fix
// works or publishes on the merged pull request, and the task is done
// the way an end makes it, with the candidate's Runs aborted.
func TestConvertCandidatesLeftByTheSweepEndsOnAMergeBeforeConverting(t *testing.T) {
	w, _, review, _ := midCandidateReview(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET locked_by = 'old-pod', locked_until = now() + interval '1 hour' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{Left: 1}) {
		t.Fatalf("swept %+v, want it left to the old pod", n)
	}
	head := w.gh.SHA(w.branch)
	merged := w.str(`SELECT now()::text`)
	w.gh.Merge(1)
	w.until("the merge recorded", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM pull_requests WHERE task_id = $1 AND state = 'merged'`, w.wi) == 1
	})
	if w.state("candidate") == "" {
		t.Fatalf("converted while the old pod leased it")
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET locked_by = NULL, locked_until = now() - interval '1 second',
		status = 'waiting', wake_at = now() WHERE task_id = $1`, w.wi)
	w.until("the delivery ended", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
	if got := w.taskStatus(w.wi); got != "done" {
		t.Errorf("task %s, want done by the merge", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'task.status_changed'
		AND payload->>'reason' = 'pull requests merged'`, w.wi); n != 1 {
		t.Errorf("%d moves to done by the merge (endable's), want 1\n%s", n,
			w.str(`SELECT string_agg(event_type || ' ' || payload::text, E'\n' ORDER BY occurred_at) FROM events WHERE task_id = $1 AND event_type LIKE 'task.%'`, w.wi))
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'
		AND created_at > $2::timestamptz`, w.wi, merged); n != 0 {
		t.Errorf("%d fix Runs completed on the merged pull request\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL
		AND status NOT IN ('aborted', 'completed', 'failed')`, w.wi); n != 0 {
		t.Errorf("%d phase Runs still live after the end\n%s", n, w.describeRuns())
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted\n%s", w.describeRuns())
	}
	if got := w.gh.SHA(w.branch); got != head {
		t.Errorf("the merged pull request's branch moved from %s to %s", head, got)
	}
}

// Two sweeps at once (two orchestrators booting) convert the delivery once.
func TestConvertCandidatesTwiceAtOnceConvertsOnce(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	var wg sync.WaitGroup
	var a, b delivery.CandidatesConverted
	var errA, errB error
	wg.Add(2)
	go func() { defer wg.Done(); a, errA = delivery.ConvertCandidates(context.Background(), w.app) }()
	go func() { defer wg.Done(); b, errB = delivery.ConvertCandidates(context.Background(), w.app) }()
	wg.Wait()
	if errA != nil || errB != nil {
		t.Fatal(errA, errB)
	}
	if a.Converted+b.Converted != 1 {
		t.Errorf("converted %d and %d, want once", a.Converted, b.Converted)
	}
	w.candidateDropped(from)
	w.directlyFixedAgain(review)
}

// State A: a candidate delivery with no candidate open (waiting on its pull
// request) is made direct, and nothing else: its next fix publishes to the
// pull request.
func TestConvertCandidatesMakesADeliveryWithNoCandidateDirect(t *testing.T) {
	w := candidateWorld(t)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if got := w.step(); got != "awaitPullRequest" {
		t.Errorf("step %s, want it left waiting", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("a notice for a delivery with no candidate")
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before
	})
	if _, branch, _ := w.fixRun(); branch != "" && branch != w.branch {
		t.Errorf("the fix published to %q", branch)
	}
	if w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
}

// stoppedMidPromotion checks a delivery stopped mid-promotion (by the
// sweep or by the deleted step): its candidate let go with its heads back
// where it started, the pull request's head never moved, the item
// reopened, one notice in Chat, and the delivery waiting on a person at
// decide on a stopped_mid_promotion escalation whose retry fixes again
// (prFix), with retry, wait and stop offered.
func (w *prFixWorld) stoppedMidPromotion(from map[string]string) {
	w.t.Helper()
	if w.state("candidate") != "" || w.state("promotedRefresh") != "" {
		w.t.Errorf("candidate %q, refresh %q left", w.state("candidate"), w.state("promotedRefresh"))
	}
	w.headsBackAt(from)
	if w.gh.SHA(w.branch) != w.before {
		w.t.Errorf("the pull request's head moved: %s, was %s", w.gh.SHA(w.branch), w.before)
	}
	w.noCandidateTrace()
	if got := w.str(`SELECT step || '/' || status || '/' || COALESCE(state->'escalation'->>'reason', '') || '/' ||
		COALESCE(state->'escalation'->>'step', '') FROM workflow_runs WHERE task_id = $1`, w.wi); got != "decide/waiting/stopped_mid_promotion/prFix" {
		w.t.Errorf("step/status/reason/retry %s, want decide/waiting/stopped_mid_promotion/prFix", got)
	}
	if got := w.str(`SELECT status::text FROM tasks WHERE id = $1`, w.wi); got != "awaiting_input" {
		w.t.Errorf("task %s, want awaiting_input", got)
	}
	if got := w.str(`SELECT COALESCE(max(payload->>'actions'), '') FROM events WHERE task_id = $1 AND event_type = 'question.asked'
		AND payload->>'reason' = 'stopped_mid_promotion'`, w.wi); got != `["retry", "wait", "stop"]` {
		w.t.Errorf("escalation actions %s, want retry, wait, stop", got)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'stopped_mid_promotion'
		AND payload->>'text' LIKE '%moving its fix onto the pull request when dude was upgraded%'`, w.wi); n != 1 {
		w.t.Errorf("%d notices of the stop, want 1", n)
	}
	if status, _ := w.resolution("fbi_x"); status != "open" {
		w.t.Errorf("fbi_x is %s, want reopened", status)
	}
}

// State C: a candidate being promoted when dude was upgraded. The code
// that would finish the move is gone, and a branch may have moved: the
// sweep stops it for a person (stoppedMidPromotion), counted, and their
// retry fixes again directly, published to the pull request's head.
func TestConvertCandidatesStopsAPromotionMidway(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', status = 'waiting', wake_at = now() + interval '1 hour',
		awaiting_signals = '[]' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{StoppedMidPromotion: 1}) {
		t.Fatalf("swept %+v, want one stopped mid-promotion", n)
	}
	w.stoppedMidPromotion(from)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE `+delivery.CandidateRowsSQL+` AND task_id = $1`, w.wi); n != 0 {
		t.Errorf("selected again after the stop")
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
	if code, body := w.decide(ana, w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.directlyFixedAgain(review)
}

// loopMovedMidRefresh stores a delivery as an old dude left a loop
// candidate's promotion mid-refresh: movedOnPR cleared the candidate
// before it set the refresh, so the row holds no candidate, and, frozen
// before the retired key, no key: only the refresh bound for the pull
// request's opening (next beforePR) at refreshPromoted, due at wake. The
// loop's fix changed acme/web too, where no pull request is open yet: its
// commit is on the task branch there. Returns web's head.
func loopMovedMidRefresh(t *testing.T, w *prFixWorld, wake string) string {
	t.Helper()
	webID := "repo_web_" + w.org
	mustExec(t, w.owner, `INSERT INTO repositories (id, organization_id, project_id, name, url, default_branch)
		VALUES ($1, $2, $3, 'web', 'git://127.0.0.1/acme/web.git', 'main') ON CONFLICT DO NOTHING`, webID, w.org, w.project)
	mustExec(t, w.owner, `INSERT INTO task_repositories (organization_id, task_id, repository_id, access)
		VALUES ($1, $2, $3, 'write')`, w.org, w.wi, webID)
	if out, err := exec.Command("git", "-C", w.web.Repo, "update-ref", "refs/heads/"+w.branch, "refs/heads/main", "").CombinedOutput(); err != nil {
		t.Fatalf("web branch: %v %s", err, out)
	}
	web := w.web.CommitFiles(w.branch, "the loop's fix", map[string]string{"PAGE.md": "page\n"})
	if web == "" {
		t.Fatalf("no commit on web")
	}
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'refreshPromoted', status = 'waiting', wake_at = now() + $3::interval,
		awaiting_signals = '[]', state = (state - 'candidate' #- '{policy,pullRequestFixes}')
		  || jsonb_build_object('promotedRefresh', '{"repos":["target"],"next":"beforePR"}'::jsonb)
		  || jsonb_build_object('heads', state->'heads' || jsonb_build_object('web', $2::text))
		WHERE task_id = $1`, w.wi, web, wake)
	return web
}

// loopMoveFinished checks a loop's completed move went on as a direct fix
// that landed: the pull request opened for the newly changed web, at its
// head, with no fix Run started (nothing to fix: the move is done), no
// person asked, nothing traced left, and the delivery back waiting on its
// pull requests.
func (w *prFixWorld) loopMoveFinished(web, since string) {
	w.t.Helper()
	w.until("a pull request for web", func() bool {
		if w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'failed', 'aborted', 'dead_lettered')`, w.wi) != 0 {
			w.t.Fatalf("the workflow ended: %s\n%s", w.step(), w.describeRuns())
		}
		return len(w.web.Pulls()) == 1 && w.step() == "awaitPullRequest"
	})
	if p := w.web.Pull(1); p.Head != w.branch || w.web.SHA(w.branch) != web {
		w.t.Errorf("web's pull request is on %s at %s, want %s at %s", p.Head, w.web.SHA(w.branch), w.branch, web)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL AND created_at > $2::timestamptz`, w.wi, since); n != 0 {
		w.t.Errorf("%d Runs started after the move, want none\n%s", n, w.describeRuns())
	}
	if w.state("escalation") != "" || w.state("promotedRefresh") != "" {
		w.t.Errorf("escalation %q, refresh %q left", w.state("escalation"), w.state("promotedRefresh"))
	}
	if w.gh.SHA(w.branch) != w.before {
		w.t.Errorf("target's pull request's head moved: %s, was %s", w.gh.SHA(w.branch), w.before)
	}
	w.noCandidateTrace()
}

// State C after a loop candidate's promotion, as an old dude stored it
// (loopMovedMidRefresh): its only trace is the refresh, and the sweep
// selects it. The move is complete: nothing is fixed again, no person is
// asked; the delivery goes on as a direct fix that landed, which opens
// the newly changed repository's pull request.
func TestConvertCandidatesFinishesALoopMovesRefresh(t *testing.T) {
	w := newPRFixWorld(t)
	since := w.str(`SELECT clock_timestamp()::text`)
	web := loopMovedMidRefresh(t, w, "1 hour")
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE `+delivery.CandidateRowsSQL+` AND task_id = $1`, w.wi); n != 1 {
		t.Fatalf("a row whose only trace is the refresh is not selected")
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{Converted: 1}) {
		t.Fatalf("swept %+v, want one converted", n)
	}
	if got := w.step(); got != "prFixDone" {
		t.Errorf("step %s, want prFixDone", got)
	}
	w.noCandidateTrace()
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
	w.loopMoveFinished(web, since)
}

// The same delivery resumed by the workflow before any sweep reached it:
// its step's entry converts it as the sweep would have.
func TestDeletedPromotionStepFinishesALoopMovesRefresh(t *testing.T) {
	w := newPRFixWorld(t)
	since := w.str(`SELECT clock_timestamp()::text`)
	web := loopMovedMidRefresh(t, w, "0 seconds")
	w.loopMoveFinished(web, since)
}

// The deleted step: a delivery stored at promoteCandidate with its
// candidate that the workflow resumes without the sweep (left as moving,
// or raced) moves no branch, neither panics nor completes: it stops for a
// person as the sweep would have.
func TestDeletedPromotionStepStopsForAPerson(t *testing.T) {
	t.Run("promoteCandidate", func(t *testing.T) {
		w, ana, review, from := midCandidateReview(t)
		mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', status = 'waiting', wake_at = now(),
			awaiting_signals = '[]' WHERE task_id = $1`, w.wi)
		w.until("stopped for a person", func() bool {
			if w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'failed', 'aborted')`, w.wi) != 0 {
				t.Fatalf("the workflow ended at the deleted step: %s\n%s", w.step(), w.describeRuns())
			}
			return w.step() == "decide"
		})
		w.stoppedMidPromotion(from)
		if code, body := w.decide(ana, w.wi, "retry", ""); code != 200 {
			t.Fatalf("retry: %d %v", code, body)
		}
		w.directlyFixedAgain(review)
	})
}

// Selection: a stopped delivery whose only trace is the deleted step (its
// candidate gone) is selected, and stopped mid-promotion with nothing to
// let go: off the deleted step, its retry goes to the pull request step,
// which starts no fix with nothing to fix and waits on the pull request.
func TestConvertCandidatesStopsAStoppedDeliveryAtTheDeletedStepWithNoCandidate(t *testing.T) {
	w := newPRFixWorld(t)
	ana := w.person("Ana")
	w.assignOwner(w.wi, ana)
	w.stopped(ana)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', state = ((state #- '{policy,pullRequestFixes}') - 'candidate')
		|| CASE WHEN state ? 'stopped' THEN jsonb_build_object('stopped', state->'stopped' || '{"step":"promoteCandidate"}') ELSE '{}' END
		|| CASE WHEN state ? 'halt' THEN jsonb_build_object('halt', state->'halt' || '{"at":"promoteCandidate"}') ELSE '{}' END
		WHERE task_id = $1`, w.wi)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE `+delivery.CandidateRowsSQL+` AND task_id = $1`, w.wi); n != 1 {
		t.Fatalf("a stopped row whose only trace is the deleted step is not selected")
	}
	since := w.str(`SELECT clock_timestamp()::text`)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{StoppedMidPromotion: 1}) {
		t.Fatalf("swept %+v, want one stopped mid-promotion", n)
	}
	w.noCandidateTrace()
	if got := w.step(); got != "openPullRequest" {
		t.Errorf("step %s, want openPullRequest", got)
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.until("back on the pull request", func() bool { return w.step() == "awaitPullRequest" && w.waiting() })
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND kind = 'agent' AND phase IS NOT NULL AND created_at > $2::timestamptz`, w.wi, since); n != 0 {
		t.Errorf("%d Runs started with nothing to fix\n%s", n, w.describeRuns())
	}
	if w.gh.SHA(w.branch) != w.before {
		t.Errorf("the pull request's head moved")
	}
}

// State C stopped: a delivery stopped by a person while its candidate was
// being promoted (step and stopped.step promoteCandidate). The sweep lets
// its candidate go and moves it off the deleted step, so a second sweep
// selects nothing and tells Chat nothing more; the person's retry fixes
// again directly.
func TestConvertCandidatesStopsAStoppedPromotionOffTheDeletedStep(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	w.stopped(ana)
	// A person's Stop records a halt (and a stop, where a Run was at
	// work): each at the deleted step.
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', state = state
		|| jsonb_build_object('stopped', COALESCE(state->'stopped', '{}') || '{"step":"promoteCandidate"}')
		|| CASE WHEN state ? 'halt' THEN jsonb_build_object('halt', state->'halt' || '{"at":"promoteCandidate"}') ELSE '{}' END
		WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{StoppedMidPromotion: 1}) {
		t.Fatalf("swept %+v, want one stopped mid-promotion", n)
	}
	w.noCandidateTrace()
	if got := w.str(`SELECT step || '/' || COALESCE(state->'stopped'->>'step', '') || '/' || COALESCE(state->'halt'->>'at', 'prFix')
		FROM workflow_runs WHERE task_id = $1`, w.wi); got != "prFix/prFix/prFix" {
		t.Errorf("step/stopped.step/halt.at %s, want prFix/prFix/prFix", got)
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice' AND payload->>'about' = 'stopped_mid_promotion'`, w.wi); n != 1 {
		t.Errorf("%d notices of the stop, want 1", n)
	}
	w.headsBackAt(from)
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.directlyFixedAgain(review)
}

// A promotion an old dude held for a reviewer's question
// (holdForQuestions at promoteCandidate, QuestionsHeld) holds nothing once
// converted: QuestionsHeld goes with it, or the task's waits would stay
// blocked (waitBlocked) for the rest of the delivery and a later
// conductor-question wait would never be ended. Both promotion shapes.
func TestConvertCandidatesLetsGoOfAPromotionsQuestionHold(t *testing.T) {
	for _, c := range []struct{ name, want string }{
		{"promoteCandidate", "StoppedMidPromotion"},
		{"refreshAfterALoopsMove", "Converted"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w *prFixWorld
			if c.name == "promoteCandidate" {
				w, _, _, _ = midCandidateReview(t)
				mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'promoteCandidate', status = 'waiting', wake_at = NULL,
					awaiting_signals = $2::jsonb WHERE task_id = $1`, w.wi, `["`+delivery.SignalReviewQuestion+`", "human.feedback"]`)
			} else {
				w = newPRFixWorld(t)
				loopMovedMidRefresh(t, w, "1 hour")
			}
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"questionsHeld": true}' WHERE task_id = $1`, w.wi)
			n := w.convertCandidates()
			if got := map[string]int{"Converted": n.Converted, "StoppedMidPromotion": n.StoppedMidPromotion}[c.want]; got != 1 || n.Left != 0 {
				t.Fatalf("swept %+v, want one %s", n, c.want)
			}
			if got := w.state("questionsHeld"); got != "" && got != "false" {
				t.Errorf("questionsHeld %s after the conversion", got)
			}
			// waitBlocked's own reading of a live delivery's hold.
			if n := w.count(`SELECT count(*) FROM workflow_runs wf WHERE wf.task_id = $1 AND wf.status IN ('running', 'waiting')
				AND COALESCE((wf.state->>'questionsHeld')::boolean, false)`, w.wi); n != 0 {
				t.Errorf("the task's waits are still blocked by the promotion's question hold")
			}
		})
	}
}

// State D: parked on a person with a candidate open. The candidate goes,
// the escalation stays, and a retry fixes again directly from the pull
// request.
func TestConvertCandidatesLeavesAParkedDeliveryParkedAndItsRetryDirect(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'decide', status = 'waiting', wake_at = NULL,
		awaiting_signals = '["human.decision"]', state = state || '{"escalation":{"reason":"review_failed","step":"review","at":"awaitReview"}}'
		WHERE task_id = $1`, w.wi)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, w.wi)
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if got := w.step(); got != "decide" {
		t.Errorf("step %s, want it left at decide", got)
	}
	if got := w.str(`SELECT state->'escalation'->>'step' || '/' || COALESCE(state->'escalation'->>'at', '') FROM workflow_runs WHERE task_id = $1`, w.wi); got != "prFix/prFix" {
		t.Errorf("escalation step/at %s, want prFix/prFix", got)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted'`, review); n != 1 {
		t.Errorf("the candidate's review was not aborted")
	}
	w.candidateDropped(from)
	if code, body := w.decide(ana, w.wi, "retry", ""); code != 200 {
		t.Fatalf("retry: %d %v", code, body)
	}
	w.directlyFixedAgain(review)
}

// Every shape a delivery with an open candidate can be in when the sweep
// runs, under one rule (ConvertCandidateTx): the candidate goes, Retries
// moves on so the redo's Runs are keyed apart from every earlier one's,
// and the fix is redone from the pull request's head (prFix for a pull
// request fix's candidate, the loop's writer for a loop's): at once when
// it runs or waits on its conductor's decision (dropped), on the person's
// answer, whatever resuming one, when it is parked on an escalation.
// Whatever the shape: a fix Run started after the sweep publishes to the
// pull request's head, no Run from before the sweep is waited on, and the
// workflow never completes under a running task.
func TestConvertCandidatesRedoesEveryShapeWithAFreshFix(t *testing.T) {
	type shape struct {
		name   string
		loop   bool
		step   string // "" running as midCandidateReview left it
		point  string // conductorDecision's
		reason string // decide's
		action string
		note   string
	}
	shapes := []shape{
		{name: "running/pr_fix"},
		{name: "running/loop", loop: true},
		{name: "conductorDecision/after_fix", loop: true, step: "conductorDecision", point: delivery.PointFixed},
		{name: "conductorDecision/after_review", loop: true, step: "conductorDecision", point: delivery.PointReviewed},
	}
	for _, d := range []struct {
		reason  string
		actions []string
	}{
		{"stuck", []string{"retry", "accept"}},
		{"exhausted", []string{"retry", "accept"}},
		{delivery.ReasonNotConverging, []string{"retry", "accept", "amend"}},
		{delivery.ReasonChecksUnverified, []string{"retry", delivery.ActionWaive}},
		{delivery.ReasonTestBlocked, []string{"retry", delivery.ActionWaive}},
	} {
		for _, a := range d.actions {
			note := ""
			if delivery.NeedsNote(d.reason, a) {
				note = "The greeting may stay in English."
			}
			// A loop's writer escalates stuck, exhausted and not converging;
			// a test or a held check, a pull request fix's candidate too.
			loop := d.reason != delivery.ReasonChecksUnverified && d.reason != delivery.ReasonTestBlocked
			shapes = append(shapes, shape{name: "decide/" + d.reason + "/" + a, loop: loop, step: "decide",
				reason: d.reason, action: a, note: note})
		}
	}
	for _, c := range shapes {
		t.Run(c.name, func(t *testing.T) {
			w, ana, review, from := midCandidateReview(t)
			wake := "" // the untaken decision's wake ConvertInFlight records
			if c.loop {
				mustExec(t, w.owner, `UPDATE workflow_runs SET state = jsonb_set(state, '{candidate,loop}', 'true') WHERE task_id = $1`, w.wi)
			}
			switch c.step {
			case "conductorDecision":
				mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'conductorDecision', status = 'waiting', wake_at = NULL,
					awaiting_signals = '["conductor.decision"]',
					state = state || jsonb_build_object('decision', jsonb_build_object('point', $2::text, 'policy', 'review'))
					WHERE task_id = $1`, w.wi, c.point)
				// As boot runs them: the quiet conductor's sweep first, which
				// records the untaken decision's wake.
				w.convert()
				wake = w.str(`SELECT COALESCE(max(key), '') FROM conductor_wakes WHERE task_id = $1 AND key LIKE '%:decision:%'
					AND delivered_at IS NULL`, w.wi)
				if wake == "" {
					t.Fatalf("no pending decision wake after ConvertInFlight")
				}
			case "decide":
				at, step := "awaitFix", "fix"
				if !c.loop {
					at, step = "awaitReview", "review"
				}
				mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'decide', status = 'waiting', wake_at = NULL,
					awaiting_signals = '["human.decision"]', state = state || jsonb_build_object('testRunId', $5::text,
					'escalation', jsonb_build_object('reason', $2::text, 'step', $3::text, 'at', $4::text,
					  'atSignals', '["phase.finished"]'::jsonb, 'detail', jsonb_build_object('runId', $5::text)))
					WHERE task_id = $1`, w.wi, c.reason, step, at, review)
				mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, w.wi)
			}
			before := w.str(`SELECT clock_timestamp()::text`)
			if n := w.convertCandidates(); n.Converted != 1 {
				t.Fatalf("swept %+v", n)
			}
			w.candidateDropped(from)
			if w.state("decision") != "" {
				t.Errorf("the conductor's decision about the candidate is still there: %s", w.state("decision"))
			}
			if c.step == "conductorDecision" {
				// The decision's wake is settled with it: no turn is started
				// for a decision that no longer exists.
				if n := w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND key = $2
					AND delivered_at IS NULL`, w.wi, wake); n != 0 {
					t.Errorf("the cleared decision's wake %s is still pending", wake)
				}
			}
			if c.step == "decide" {
				if got := w.step(); got != "decide" {
					t.Errorf("step %s, want it left at decide", got)
				}
				if code, body := w.decide(ana, w.wi, c.action, c.note); code != 200 {
					t.Fatalf("%s: %d %v", c.action, code, body)
				}
			}
			w.directlyFixedAgain(review)
			// The fix's own review: a Run after the sweep, not the one from
			// before it whose key a reset Iteration would match.
			w.until("the new fix's own review finished", func() bool {
				if wake != "" {
					// Quiet now (ConvertInFlight): the redo's own decisions
					// lapse to their defaults.
					w.lapse(w.wi)
				}
				if w.count(`SELECT count(*) FROM workflow_runs wf JOIN tasks t ON t.id = wf.task_id
					WHERE wf.task_id = $1 AND wf.status = 'completed' AND t.status = 'running'`, w.wi) != 0 {
					t.Fatalf("the workflow completed with its task running\n%s", w.describeRuns())
				}
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'review' AND status = 'completed'
					AND created_at > (SELECT min(created_at) FROM runs WHERE task_id = $1 AND phase = 'fix' AND created_at > $2::timestamptz)`, w.wi, before) > 0
			})
			w.until("the delivery past the new fix's review", func() bool {
				if wake != "" {
					w.lapse(w.wi)
				}
				var pending []string
				_ = json.Unmarshal([]byte(w.state("pendingRunIds")), &pending)
				for _, id := range pending {
					if w.count(`SELECT count(*) FROM runs WHERE id = $1 AND created_at <= $2::timestamptz`, id, before) != 0 {
						return false
					}
				}
				return w.step() != "awaitReview" || len(pending) > 0
			})
			if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND created_at <= $2::timestamptz
				AND status NOT IN ('aborted', 'completed', 'failed')`, w.wi, before); n != 0 {
				t.Errorf("%d Runs from before the sweep still live\n%s", n, w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM workflow_runs wf JOIN tasks t ON t.id = wf.task_id
				WHERE wf.task_id = $1 AND wf.status = 'completed' AND t.status IN ('running', 'review')`, w.wi); n != 0 {
				t.Errorf("the workflow completed while its task goes on: step %s\n%s", w.step(), w.describeRuns())
			}
			if n := w.count(`SELECT count(*) FROM conductor_wakes cw WHERE cw.task_id = $1 AND cw.key = $2
				AND (cw.conductor_run_id IS NOT NULL OR EXISTS (SELECT 1 FROM conductor_wake_attempts a WHERE a.wake_id = cw.id))`, w.wi, wake); n != 0 {
				t.Errorf("a conductor turn was started for the cleared decision's wake %s", wake)
			}
		})
	}
}

// State F: stopped with its candidate kept for a recovery. The candidate
// goes, the kept Runs are let go, and a retry fixes again directly.
func TestConvertCandidatesLetsAStoppedDeliveryRetryDirectly(t *testing.T) {
	w, ana, review, from := midCandidateReview(t)
	w.stopped(ana)
	if w.state("candidate") == "" {
		t.Fatalf("the stop dropped the candidate")
	}
	if n := w.convertCandidates(); n.Converted != 1 {
		t.Fatalf("swept %+v", n)
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND keep`, w.wi); n != 0 {
		t.Errorf("%d Runs still kept", n)
	}
	w.candidateDropped(from)
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/recover", map[string]any{"action": "retry"}); status != 200 {
		t.Fatalf("retry: %d %v", status, body)
	}
	w.directlyFixedAgain(review)
}

// State G: a hand-over, or an integration's mergeability polls, left on a
// delivery (keyless) is dropped with the rest.
func TestConvertCandidatesDropsAHandOver(t *testing.T) {
	for name, trace := range map[string]string{
		"handedOver":       `{"handedOver":{"target:abc:def":{"repo":"target","head":"abc","base":"def"}}}`,
		"integrationPolls": `{"integration":{"repo":"target","number":1,"base":"main","head":"abc","baseHead":"def","polls":3}}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := newPRFixWorld(t)
			// Keyless, as one frozen before the key existed.
			mustExec(t, w.owner, `UPDATE workflow_runs SET state = (state #- '{policy,pullRequestFixes}') || $2::jsonb WHERE task_id = $1`, w.wi, trace)
			if n := w.convertCandidates(); n != (delivery.CandidatesConverted{Converted: 1}) {
				t.Fatalf("swept %+v", n)
			}
			w.noCandidateTrace()
			if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
				t.Errorf("a second sweep did %+v", n)
			}
			if got := w.step(); got != "awaitPullRequest" {
				t.Errorf("step %s", got)
			}
		})
	}
}

// Selection: a delivery frozen before pullRequestFixes was a key (most
// live ones) fixed through a candidate too: with one open, the sweep
// converts it as any other, and a second sweep selects nothing.
func TestConvertCandidatesConvertsAKeylessDeliveryWithACandidate(t *testing.T) {
	w, _, review, from := midCandidateReview(t)
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state #- '{policy,pullRequestFixes}' WHERE task_id = $1`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{Converted: 1}) {
		t.Fatalf("swept %+v, want the keyless delivery converted", n)
	}
	w.candidateDropped(from)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE `+delivery.CandidateRowsSQL+` AND task_id = $1`, w.wi); n != 0 {
		t.Errorf("selected again after the sweep")
	}
	w.directlyFixedAgain(review)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("a second sweep did %+v", n)
	}
}

// Selection: a keyless delivery with no trace of the candidate path (as
// every one frozen since direct fixes) is not selected: left as it is,
// and its next fix publishes to the pull request.
func TestConvertCandidatesLeavesAKeylessDeliveryWithNoTrace(t *testing.T) {
	w := newPRFixWorld(t)
	before := w.str(`SELECT state::text || step FROM workflow_runs WHERE task_id = $1`, w.wi)
	if n := w.count(`SELECT count(*) FROM workflow_runs WHERE `+delivery.CandidateRowsSQL+` AND task_id = $1`, w.wi); n != 0 {
		t.Fatalf("a delivery with no trace of the candidate path is selected: %s", before)
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Fatalf("swept %+v, want nothing", n)
	}
	if after := w.str(`SELECT state::text || step FROM workflow_runs WHERE task_id = $1`, w.wi); after != before {
		t.Errorf("rewritten:\n%s\n%s", before, after)
	}
	w.comment("Please rename the greeting.")
	w.until("the fix on the pull request", func() bool {
		w.sync()
		return w.gh.SHA(w.branch) != w.before
	})
	if w.gh.SHA(delivery.CandidateBranch(w.wi, 1)) != "" {
		t.Errorf("a candidate branch was written")
	}
}

// Selection: an attempt started over is not the task's delivery any more.
// Stopped with its candidate kept, then superseded by a start over, it is
// left as it is: no notice, and the live attempt's kept Run stays kept.
func TestConvertCandidatesLeavesASupersededAttempt(t *testing.T) {
	w, ana, _, _ := midCandidateReview(t)
	w.stopped(ana)
	old := w.str(`SELECT id FROM workflow_runs WHERE task_id = $1`, w.wi)
	w.pickUp(ana, w.wi, map[string]any{"action": "restart"})
	w.until("the next attempt", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND id <> $2`, w.wi, old) == 1
	})
	// As a person's stop at decide leaves it (State.Stopped), recoverable
	// but for the start over.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || '{"stopped":{"reason":"stuck","step":"fix"}}' WHERE id = $1`, old)
	if w.count(`SELECT count(*) FROM workflow_runs WHERE id = $1 AND state ? 'candidate' AND (state ? 'stopped' OR state ? 'halt')`, old) != 1 {
		t.Fatalf("the stopped attempt kept no candidate: nothing to pin: %s", w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old))
	}
	// A Run of the live attempt kept for its next round.
	w.until("a Run of the next attempt", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND attempt = 2 AND status IN ('aborted', 'failed', 'completed')`, w.wi) > 0
	})
	kept := w.str(`SELECT max(id) FROM runs WHERE task_id = $1 AND attempt = 2 AND status IN ('aborted', 'failed', 'completed')`, w.wi)
	mustExec(t, w.owner, `UPDATE runs SET keep = true, kept_until = now() + interval '1 hour' WHERE id = $1`, kept)
	before := w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old)
	w.convertCandidates()
	if after := w.str(`SELECT state::text FROM workflow_runs WHERE id = $1`, old); after != before {
		t.Errorf("the superseded attempt was rewritten")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND keep AND kept_until > now()`, kept); n != 1 {
		t.Errorf("the live attempt's kept Run %s was let go", kept)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("%d notices for a superseded attempt", n)
	}
}

// Selection: a task closed with its candidate open has ended: not
// selected, and nothing tells Chat dude fixes it again.
func TestConvertCandidatesLeavesAClosedTask(t *testing.T) {
	w, ana, _, _ := midCandidateReview(t)
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	w.until("closed", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND status IN ('completed', 'aborted')`, w.wi) == 1
	})
	if w.state("candidate") == "" || w.state("stopped") != "" || w.state("halt") != "" {
		t.Fatalf("closed: candidate %q stopped %q halt %q: nothing to pin", w.state("candidate"), w.state("stopped"), w.state("halt"))
	}
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("swept %+v, want nothing", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi); n != 0 {
		t.Errorf("%d notices for a closed task", n)
	}
}

// Selection: a task stopped with its candidate kept, then closed, has
// ended: its delivery still says stopped (Close leaves State.Stopped), but
// it is not selected, nothing tells Chat dude fixes it again, and its
// state is left as it is.
func TestConvertCandidatesLeavesATaskClosedAfterAStop(t *testing.T) {
	w, ana, review, _ := midCandidateReview(t)
	// Parked on Ana with the candidate open, she stops it (State.Stopped).
	mustExec(t, w.owner, `UPDATE workflow_runs SET step = 'decide', status = 'waiting', wake_at = NULL,
		awaiting_signals = '["human.decision"]', state = state || jsonb_build_object('escalation', jsonb_build_object(
		  'reason', 'review_failed', 'step', 'review', 'at', 'awaitReview', 'detail', jsonb_build_object('runId', $2::text)))
		WHERE task_id = $1`, w.wi, review)
	mustExec(t, w.owner, `UPDATE tasks SET status = 'awaiting_input' WHERE id = $1`, w.wi)
	candidate := w.state("candidate")
	if code, body := w.decide(ana, w.wi, "stop", ""); code != 200 {
		t.Fatalf("stop: %d %v", code, body)
	}
	w.until("stopped", func() bool {
		return w.count(`SELECT count(*) FROM workflow_runs WHERE task_id = $1 AND state ? 'stopped'
			AND status IN ('completed', 'aborted', 'failed')`, w.wi) == 1
	})
	if status, body := w.callAs(ana, "/internal/tasks/"+w.wi+"/close", map[string]any{"resolution": "wont_do"}); status != 200 {
		t.Fatalf("close: %d %v", status, body)
	}
	if w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND resolution IS NOT NULL`, w.wi) != 1 {
		t.Fatalf("not closed")
	}
	// The decide step's entry converts the candidate before the stop is
	// recorded (steps.legacy): the row a dude from before stored, stopped
	// with its candidate, is put back as it was.
	mustExec(t, w.owner, `UPDATE workflow_runs SET state = state || jsonb_build_object('candidate', $2::jsonb) WHERE task_id = $1`, w.wi, candidate)
	if w.state("candidate") == "" || w.state("stopped") == "" {
		t.Fatalf("closed after a stop: candidate %q stopped %q: nothing to pin", w.state("candidate"), w.state("stopped"))
	}
	before := w.str(`SELECT state::text || step FROM workflow_runs WHERE task_id = $1`, w.wi)
	notices := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi)
	if n := w.convertCandidates(); n != (delivery.CandidatesConverted{}) {
		t.Errorf("swept %+v, want nothing", n)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND payload->>'about' = 'candidate_converted'`, w.wi) - notices; n != 0 {
		t.Errorf("%d notices for a closed task", n)
	}
	if after := w.str(`SELECT state::text || step FROM workflow_runs WHERE task_id = $1`, w.wi); after != before {
		t.Errorf("the closed task's delivery was rewritten:\n%s\n%s", before, after)
	}
}
