package delivery

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
)

// verdict is the gate under ProfileReviewVerdict, pinned at pin ("" for
// none), read as gate() reads the merge profile's.
func (f *gateFixture) verdict(pin string) PRGate {
	f.t.Helper()
	var c ReadyGateCheck
	if err := f.store.DB.InOrg(f.ctx, f.org, func(tx pgx.Tx) (err error) {
		c, err = CheckProfileGateTx(f.ctx, tx, "wi_g", nil, ProfileReviewVerdict, map[string]string{"pr_g": pin})
		return err
	}); err != nil {
		f.t.Fatal(err)
	}
	gates := DoReadyGate(f.ctx, f.forge, c)
	if len(gates) != 1 || gates[0].Err != nil || len(gates[0].Items) != len(ReadyKeys) {
		f.t.Fatalf("verdict gate: %+v", gates)
	}
	for i, it := range gates[0].Items {
		if it.Key != ReadyKeys[i] {
			f.t.Fatalf("item %d is %s, want %s", i, it.Key, ReadyKeys[i])
		}
	}
	return gates[0]
}

// The review verdict's profile, item by item, beside the merge profile's
// answer to the same pull request: what it judges differently, and what
// it judges the same.
func TestTheReviewVerdictProfile(t *testing.T) {
	type answer struct {
		ok   bool
		want string
	}
	for _, c := range []struct {
		name string
		// setup builds the pull request; its result is the verdict's pin.
		setup  func(f *gateFixture) string
		key    string
		merge  answer
		review answer
	}{
		{name: "every item holding", key: ReadyOpen,
			setup:  func(f *gateFixture) string { f.gh.SetChecks("success"); return f.ready() },
			merge:  answer{true, "open"},
			review: answer{true, "open"}},
		{name: "a draft", key: ReadyOpen,
			setup: func(f *gateFixture) string {
				p := f.gh.Pull(f.n)
				f.gh.Set(func(*fakegithub.Server) { p.Draft = true })
				return f.ready()
			},
			merge:  answer{false, "it is a draft: mark it ready for review"},
			review: answer{false, VerdictDraft}},
		{name: "closed", key: ReadyOpen,
			setup:  func(f *gateFixture) string { h := f.ready(); f.gh.Close(f.n); return h },
			merge:  answer{false, "it is closed"},
			review: answer{false, "it is closed"}},
		{name: "the pin is GitHub's head, the recorded head behind it", key: ReadyHead,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.exec(`UPDATE pull_requests SET head_sha = '0123456789abcdef0123456789abcdef01234567' WHERE id = 'pr_g' AND organization_id = $1`)
				return h
			},
			merge:  answer{false, "dude last recorded 0123456"},
			review: answer{true, "at "}},
		{name: "GitHub's head moved past the pin", key: ReadyHead,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app // tweak\n"})
				f.synced()
				return h
			},
			merge:  answer{true, "at "},
			review: answer{false, "the verdict reviewed"}},
		{name: "a lens reviewed only before a person's commit", key: ReadyReviewed,
			setup: func(f *gateFixture) string {
				h1 := f.implemented(map[string]string{"db/migrations/001.sql": "SELECT 1;\n"})
				f.reviewed(h1, "correctness", "database")
				h2 := f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app\n"})
				f.reviewed(h2, "correctness")
				f.simplifiedEmpty(h2)
				f.synced()
				return h2
			},
			merge:  answer{true, "reviewed at"},
			review: answer{false, "by database (reviewed at "}},
		{name: "a lens never reviewed", key: ReadyReviewed,
			setup: func(f *gateFixture) string {
				h := f.implemented(map[string]string{"db/migrations/001.sql": "SELECT 1;\n"})
				f.reviewed(h)
				f.simplifiedEmpty(h)
				f.synced()
				return h
			},
			merge:  answer{false, "no completed review by database"},
			review: answer{false, "by database"}},
		{name: "a reviewed simplifier's commit since a lens's review", key: ReadyReviewed,
			setup: func(f *gateFixture) string {
				h := f.implemented(map[string]string{"app/greet.go": "package app\n"})
				f.reviewed(h)
				s := f.simplified()
				f.reviewed(s, "quality")
				f.synced()
				return s
			},
			merge:  answer{true, "reviewed at"},
			review: answer{false, "unreviewed since: "}},
		{name: "a pure merge of main since the review", key: ReadyReviewed,
			setup: func(f *gateFixture) string {
				f.ready()
				f.gh.CommitFiles("main", "Other work", map[string]string{"lib/other.go": "package lib\n"})
				f.gh.MergeInto("feature", "main", nil)
				return f.synced()
			},
			merge:  answer{true, "pure merges of main"},
			review: answer{true, "only pure merges of main"}},
		{name: "a person accepted the round at a head nobody reviewed", key: ReadyReviewed,
			setup: func(f *gateFixture) string {
				f.ready()
				c := f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app // tweak\n"})
				f.st.AcceptedHeads = map[string]string{"repo": c}
				f.saveState()
				return f.synced()
			},
			merge:  answer{true, "a person accepted"},
			review: answer{false, "unreviewed since: "}},
		{name: "the simplifier never ran", key: ReadySimplified,
			setup: func(f *gateFixture) string {
				h := f.implemented(map[string]string{"app/greet.go": "package app\n"})
				f.reviewed(h)
				f.synced()
				return h
			},
			merge:  answer{false, "start the simplifier"},
			review: answer{true, "not applicable: review only"}},
		{name: "a finding said to be already on the base", key: ReadyFindings,
			setup:  func(f *gateFixture) string { h := f.ready(); f.finding("false"); return h },
			merge:  answer{false, "fnd_v (high)"},
			review: answer{true, "no open finding at the fix bar introduced by the pull request"}},
		{name: "a finding said nothing of", key: ReadyFindings,
			setup:  func(f *gateFixture) string { h := f.ready(); f.finding("NULL"); return h },
			merge:  answer{false, "fnd_v (high)"},
			review: answer{false, "1 open finding(s) at the fix bar introduced by the pull request: fnd_v (high)"}},
		{name: "a finding the pull request introduced", key: ReadyFindings,
			setup:  func(f *gateFixture) string { h := f.ready(); f.finding("true"); return h },
			merge:  answer{false, "fnd_v (high)"},
			review: answer{false, "fnd_v (high)"}},
		{name: "a person's feedback", key: ReadyQuestions,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.exec(`INSERT INTO feedback_batches (id, organization_id, task_id, attempt, author_person_id, submission_key)
					VALUES ('fbt_g', $1, 'wi_g', 1, 'per_g_'||$1, 'k')`)
				f.exec(`INSERT INTO feedback_items (id, organization_id, task_id, attempt, batch_id, author_person_id, body, anchor_kind, anchor, status, submitted_at)
					VALUES ('fbi_g', $1, 'wi_g', 1, 'fbt_g', 'per_g_'||$1, 'Rename it', 'general', '{"kind":"general"}', 'open', now())`)
				return h
			},
			merge:  answer{false, "1 feedback item(s)"},
			review: answer{true, "nothing open to a person"}},
		{name: "a reviewer's question", key: ReadyQuestions,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.exec(`INSERT INTO review_questions (id, organization_id, task_id, run_id, n, category, question)
					VALUES ('rq_g', $1, 'wi_g', 'run_g02', 1, 'correctness', 'Is the token rotated?')`)
				return h
			},
			merge:  answer{false, "1 review question(s) open"},
			review: answer{false, "1 review question(s) open"}},
		{name: "an escalation", key: ReadyQuestions,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.st.Escalation = &Escalation{Reason: "ci_stuck"}
				f.saveState()
				return h
			},
			merge:  answer{false, "escalation ci_stuck"},
			review: answer{false, "escalation ci_stuck"}},
		{name: "an unresolved thread", key: ReadyThreads,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.gh.Set(func(s *fakegithub.Server) { s.Unresolved[f.n] = 2 })
				return h
			},
			merge:  answer{false, "2 review thread(s) unresolved"},
			review: answer{true, "2 review thread(s) unresolved"}},
		{name: "GitHub would not count the review threads", key: ReadyThreads,
			setup:  func(f *gateFixture) string { h := f.ready(); f.graphqlForbidden(); return h },
			merge:  answer{false, "GitHub would not count the review threads: unreadable"},
			review: answer{true, "threads: unreadable"}},
		{name: "a conflict", key: ReadyMergeable,
			setup: func(f *gateFixture) string {
				h := f.ready()
				f.gh.Set(func(s *fakegithub.Server) { s.Conflicting[f.n] = true })
				return h
			},
			merge:  answer{false, "conflicts with main"},
			review: answer{true, "conflicts with main"}},
		{name: "behind main", key: ReadyMergeable,
			setup:  func(f *gateFixture) string { h := f.ready(); f.gh.AdvanceBase("main", "Other work"); return h },
			merge:  answer{false, "behind main by 1 commit(s)"},
			review: answer{true, "behind main by 1 commit(s)"}},
		{name: "checks failing at the pin", key: ReadyChecks,
			setup:  func(f *gateFixture) string { f.gh.SetChecks("run:failure"); return f.ready() },
			merge:  answer{false, "failing: e2e"},
			review: answer{false, "failing: e2e"}},
		{name: "checks pending at the pin", key: ReadyChecks,
			setup:  func(f *gateFixture) string { f.gh.SetChecks("run:pending"); return f.ready() },
			merge:  answer{false, "pending: e2e"},
			review: answer{false, "pending: e2e"}},
		{name: "checks green at the pin", key: ReadyChecks,
			setup:  func(f *gateFixture) string { f.gh.SetChecks("run:success"); return f.ready() },
			merge:  answer{true, "1 check(s) passed"},
			review: answer{true, "1 check(s) passed"}},
		{name: "checks green at a head past the pin", key: ReadyChecks,
			setup: func(f *gateFixture) string {
				f.gh.SetChecks("run:success")
				h := f.ready()
				f.gh.CommitFiles("feature", "Tweak", map[string]string{"app/greet.go": "package app // tweak\n"})
				f.synced()
				return h
			},
			merge:  answer{true, "1 check(s) passed"},
			review: answer{false, "not read at "}},
		// The pin is GitHub's head, the syncer still records an older one
		// first seen an hour ago: the pin is a head not yet seen, within
		// the whole grace, whatever the older head's CI record says.
		{name: "nothing reported at a pin ahead of the record, the recorded head seen an hour ago", key: ReadyChecks,
			setup:  func(f *gateFixture) string { return f.aheadOfTheRecord("false", "passing", "1 hour") },
			merge:  answer{true, "no CI for this head: nothing reported within the grace"},
			review: answer{false, "pending: no checks have reported at the head yet"}},
		{name: "nothing reported at a pin ahead of the record, the recorded head had CI and passed", key: ReadyChecks,
			setup:  func(f *gateFixture) string { return f.aheadOfTheRecord("true", "passing", "1 hour") },
			merge:  answer{true, "no CI for this head: nothing reported within the grace"},
			review: answer{false, "pending: no checks have reported at the head yet"}},
		{name: "nothing reported at a pin ahead of the record, the syncer past the recorded head's grace", key: ReadyChecks,
			setup:  func(f *gateFixture) string { return f.aheadOfTheRecord("true", "unknown", "1 hour") },
			merge:  answer{true, "no CI for this head: nothing reported within the grace"},
			review: answer{false, "pending: no checks have reported at the head yet"}},
		{name: "nothing reported at a pin the syncer recorded just now", key: ReadyChecks,
			setup: func(f *gateFixture) string {
				h := f.aheadOfTheRecord("false", "passing", "0")
				f.exec(`UPDATE pull_requests SET head_sha = $2 WHERE id = 'pr_g' AND organization_id = $1`, h)
				return h
			},
			merge:  answer{false, "pending: no checks have reported at the head yet"},
			review: answer{false, "pending: no checks have reported at the head yet"}},
		// A pull request that has had CI (an external status, no workflows):
		// nothing at the pin yet is CI yet to start, not "no CI configured".
		{name: "nothing reported at a pin ahead of the record, on a pull request that had CI and has no workflows", key: ReadyChecks,
			setup: func(f *gateFixture) string {
				f.gh.SetChecks("none")
				h := f.ready()
				f.exec(`UPDATE pull_requests SET head_sha = '0123456789abcdef0123456789abcdef01234567', head_seen_at = now() - interval '1 hour',
					had_ci = true, checks = 'passing' WHERE id = 'pr_g' AND organization_id = $1`)
				return h
			},
			merge:  answer{true, "no CI for this head: nothing reported within the grace"},
			review: answer{false, "pending: no checks have reported at the head yet"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newGateFixture(t)
			pin := c.setup(f)
			g := f.gate()
			if it := g.item(c.key); it.OK != c.merge.ok || !strings.Contains(it.Detail, c.merge.want) {
				t.Errorf("merge profile %s: ok %v %q, want ok %v containing %q", c.key, it.OK, it.Detail, c.merge.ok, c.merge.want)
			}
			v := f.verdict(pin)
			holds(t, v, c.key, c.review.ok, c.review.want)
			if c.name == "every item holding" && !v.Ready {
				t.Errorf("the verdict profile is not ready: %+v", v.Items)
			}
		})
	}
}

// finding records an open high finding of the review Run, introduced as
// given (SQL: true, false or NULL).
func (f *gateFixture) finding(introduced string) {
	f.t.Helper()
	f.exec(`INSERT INTO review_findings (id, organization_id, task_id, run_id, category, severity, title, status, introduced)
		VALUES ('fnd_v', $1, 'wi_g', 'run_g02', 'correctness', 'high', 'It crashes', 'open', ` + introduced + `)`)
}

// aheadOfTheRecord is a reviewed pull request whose workflows run on pull
// requests, nothing reported at its head (the pin, returned), while the
// syncer records an older head first seen seen ago, with its CI record
// (SQL: had_ci, checks).
func (f *gateFixture) aheadOfTheRecord(hadCI, checks, seen string) string {
	f.t.Helper()
	f.gh.SetChecks("none")
	h := f.implemented(map[string]string{"app/greet.go": "package app\n", ".github/workflows/ci.yml": "on: pull_request\n"})
	f.reviewed(h)
	f.simplifiedEmpty(h)
	f.exec(`UPDATE pull_requests SET head_sha = '0123456789abcdef0123456789abcdef01234567', head_seen_at = now() - $2::interval,
		had_ci = `+hadCI+`, checks = '`+checks+`' WHERE id = 'pr_g' AND organization_id = $1`, seen)
	return h
}

// With no pin, the verdict is judged at the recorded head, as the merge
// profile judges it.
func TestTheReviewVerdictProfileWithNoPinJudgesTheRecordedHead(t *testing.T) {
	f := newGateFixture(t)
	f.ready()
	holds(t, f.verdict(""), ReadyHead, true, "at ")
	f.gh.CommitOnTop("feature", "A person's commit")
	holds(t, f.verdict(""), ReadyHead, false, "dude last recorded")
}
