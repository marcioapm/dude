package delivery

import (
	"regexp"
	"testing"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// gateSHA is a commit's short form in a gate's detail: the fixture's
// commits are new on every run, the words around them are not.
var gateSHA = regexp.MustCompile(`\b[0-9a-f]{7}\b`)

// sameItems says got is want item by item, each commit in a detail read
// as <sha>.
func sameItems(t *testing.T, got PRGate, want []GateItem) {
	t.Helper()
	if len(got.Items) != len(want) {
		t.Fatalf("%d items, want %d: %+v", len(got.Items), len(want), got.Items)
	}
	for i, it := range got.Items {
		it.Detail = gateSHA.ReplaceAllString(it.Detail, "<sha>")
		if it != want[i] {
			t.Errorf("item %d:\n got %+v\nwant %+v", i, it, want[i])
		}
	}
}

// The merge profile, the gate every writing task reads, gives what it gave
// before profiles existed, item by item, word for word: once with every
// item holding, and once with every item failing for a reason the review
// verdict's profile judges differently (a draft, a finding not introduced
// by the pull request, a person's feedback, an unresolved thread, a
// conflict, red CI), and once with a person's commit since the review.
// The expected items were read at main (787619202) before the profile.
func TestTheMergeProfileIsTheGateAsItWas(t *testing.T) {
	t.Run("every item holding", func(t *testing.T) {
		f := newGateFixture(t)
		f.gh.SetChecks("success")
		f.ready()
		g := f.gate()
		sameItems(t, g, []GateItem{
			{ReadyOpen, true, "open"},
			{ReadyHead, true, "at <sha>"},
			{ReadyReviewed, true, "reviewed at <sha>"},
			{ReadySimplified, true, "the simplifier ran from <sha>, after the last change <sha>, and found nothing to simplify"},
			{ReadyFindings, true, "no open finding at the fix bar"},
			{ReadyQuestions, true, "nothing open to a person"},
			{ReadyThreads, true, "no unresolved review thread"},
			{ReadyMergeable, true, "clean"},
			{ReadyChecks, true, "1 check(s) passed at the head"},
		})
		if !g.Ready {
			t.Fatal("not ready")
		}
	})
	t.Run("every item failing", func(t *testing.T) {
		f := newGateFixture(t)
		f.ready()
		f.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status, introduced)
			VALUES ('fnd_base', $1, 'wi_g', 'run_g02', 'correctness', 'high', 'Already on main', 'open', false)`)
		f.exec(`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
			VALUES ('fbt_g', $1, 'wi_g', 1, 'per_g_'||$1, 'k')`)
		f.exec(`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at)
			VALUES ('fbi_g', $1, 'wi_g', 1, 'fbt_g', 'per_g_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now())`)
		f.gh.SetChecks("run:failure")
		p := f.gh.Pull(f.n)
		f.gh.Set(func(s *fakegithub.Server) {
			p.Draft = true
			s.Unresolved[f.n] = 2
			s.Conflicting[f.n] = true
		})
		f.gh.CommitOnTop("feature", "A person's commit")
		g := f.gate()
		sameItems(t, g, []GateItem{
			{ReadyOpen, false, "it is a draft: mark it ready for review"},
			{ReadyHead, false, "GitHub shows <sha>, dude last recorded <sha>: not ready, re-evaluate once it is synced"},
			{ReadyReviewed, true, "reviewed at <sha>"},
			{ReadySimplified, true, "the simplifier ran from <sha>, after the last change <sha>, and found nothing to simplify"},
			{ReadyFindings, false, "1 open finding(s) at the fix bar: fnd_base (high) Already on main"},
			{ReadyQuestions, false, "1 feedback item(s) of a person's still to handle"},
			{ReadyThreads, false, "2 review thread(s) unresolved: each is a person's open feedback"},
			{ReadyMergeable, false, "it conflicts with main: resolve the conflict"},
			{ReadyChecks, false, "failing: e2e"},
		})
	})
	t.Run("a person's commit since the review", func(t *testing.T) {
		f := newGateFixture(t)
		f.gh.SetChecks("success")
		f.ready()
		f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app // tweak\n"})
		f.synced()
		sameItems(t, f.gate(), []GateItem{
			{ReadyOpen, true, "open"},
			{ReadyHead, true, "at <sha>"},
			{ReadyReviewed, false, "reviewed at <sha>; unreviewed since: <sha>"},
			{ReadySimplified, false, "the simplifier has not run since <sha>, the last change: start the simplifier"},
			{ReadyFindings, true, "no open finding at the fix bar"},
			{ReadyQuestions, true, "nothing open to a person"},
			{ReadyThreads, true, "no unresolved review thread"},
			{ReadyMergeable, true, "clean"},
			{ReadyChecks, true, "1 check(s) passed at the head"},
		})
	})
}
