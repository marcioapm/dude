package orchestrator_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
)

// #485 round 5: a delivery's end against a poster in flight. The end never
// withholds an answer a poster holds (it may be on GitHub a moment later);
// it withholds what nobody holds, blocks new claims, and leaves the held
// one to its poster (posted, failed) or, if the poster is gone, to the
// pr-reconciler's pass (posted, withheld, uncertain). These tests use only
// what the delivery had before (a transport barrier in the poster's GitHub client, the
// poster's AfterAnswerPost as the crash, the pass through the syncer), so
// they run, and fail, on the code before the fix too.

// barrier holds the first GitHub write match picks, in the poster's own
// HTTP transport, until opened: the poster has claimed the answer and is
// inside its write, which GitHub has not yet taken.
type barrier struct {
	match    func(r *http.Request, body string) bool
	once     sync.Once
	inside   chan struct{}
	release  chan struct{}
	released sync.Once
}

func newBarrier(match func(r *http.Request, body string) bool) *barrier {
	return &barrier{match: match, inside: make(chan struct{}), release: make(chan struct{})}
}

func (b *barrier) open() { b.released.Do(func() { close(b.release) }) }

func (b *barrier) RoundTrip(r *http.Request) (*http.Response, error) {
	{
		var body string
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(raw))
			body = string(raw)
		}
		if b.match(r, body) {
			hit := false
			b.once.Do(func() { hit = true })
			if hit {
				close(b.inside)
				select {
				case <-b.release:
				case <-time.After(30 * time.Second):
				}
			}
		}
	}
	return http.DefaultTransport.RoundTrip(r)
}

// through is the organization's GitHub with its requests through rt.
func (a *answering) through(t *testing.T, rt http.RoundTripper) *forge.GitHub {
	t.Helper()
	var c forge.Credential
	var settings []byte
	if err := a.w.app.InOrg(context.Background(), a.w.org, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT auth::text, secret, api_base_url, settings FROM forge_credentials WHERE forge = 'github' LIMIT 1`).
			Scan(&c.Auth, &c.Secret, &c.APIBaseURL, &settings)
	}); err != nil {
		t.Fatal(err)
	}
	gh := forge.NewGitHub(c, forge.WithTransport(rt))
	gh.Settings = forge.ReadSettings(settings)
	return gh
}

// answerIDs are the fixer's held answers, in the order made: three
// replies (first, second, is), then two resolutions (first, second).
func (a *answering) answerIDs(run string) []string {
	return a.w.ids(`SELECT id FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, run, delivery.EvAnswerHeld)
}

// outcome is how held answer id is settled in dude's records: posted (its
// chat.message or pull_request.action), withheld, uncertain, failed, or
// "" when not settled. It fails the test if two records settle it (one
// withheld and then posted, above all).
func (a *answering) outcome(t *testing.T, run, id string) string {
	t.Helper()
	rows := a.w.ids(`SELECT CASE WHEN event_type IN ('chat.message', 'pull_request.action') THEN 'posted'
			WHEN payload->>'withheld' = 'true' THEN 'withheld' WHEN payload->>'uncertain' = 'true' THEN 'uncertain' ELSE 'failed' END
		FROM events WHERE run_id = $1 AND event_type IN ('chat.message', 'pull_request.action', $3)
		  AND COALESCE(payload->>'answerId', payload->'github'->>'answerId') = $2 ORDER BY cursor`, run, id, delivery.EvAnswerFailed)
	if len(rows) > 1 {
		t.Errorf("answer %s is settled %d times: %v", id, len(rows), rows)
	}
	if len(rows) == 0 {
		return ""
	}
	return rows[0]
}

// told is what dude said of the fixer's answers after its delivery ended:
// in the task's Chat, and in the conductor's wakes.
func (a *answering) told() string {
	return a.w.str(`SELECT COALESCE(string_agg(t, ' | '), '') FROM (
		SELECT payload->>'text' AS t FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
		UNION ALL SELECT line FROM conductor_wakes WHERE task_id = $1) x`, a.task)
}

// claimsAfterEnd counts claims recorded after the task's delivery ended
// for its answers (none before the fix: there was no end to read).
func (a *answering) claimsAfterEnd() int {
	return a.w.count(`SELECT count(*) FROM events c WHERE c.task_id = $1 AND c.event_type = $2
		AND c.cursor > (SELECT min(m.cursor) FROM events m WHERE m.task_id = $1 AND m.event_type = 'pull_request.answers_ended')`,
		a.task, delivery.EvAnswerClaimed)
}

// The delivery ends — a person's Stop, the pull request merged, or closed —
// while a poster is inside a GitHub write it claimed: a reply, or a
// thread's resolution. The in-flight answer ends posted (it is on GitHub,
// and true); every answer nobody held is withheld; none is recorded
// withheld and then posted; a later pass claims nothing after the end.
func TestRound5TheEndNeverWithholdsAnAnswerBeingPosted(t *testing.T) {
	ends := []struct {
		name string
		end  func(a *answering)
		why  string
	}{
		{"stopped", func(a *answering) { a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"}) }, "the delivery was stopped"},
		{"merged", func(a *answering) {
			a.w.gh.Merge(1)
			a.w.until("the task done", func() bool {
				a.w.sync()
				return a.w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'done'`, a.task) == 1
			})
		}, "the pull request was merged"},
		{"closed", func(a *answering) {
			a.w.gh.Close(1)
			a.w.until("the task aborted", func() bool {
				a.w.sync()
				return a.w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'aborted'`, a.task) == 1
			})
		}, "the pull request was closed"},
	}
	for _, end := range ends {
		for _, write := range []string{"reply", "resolve"} {
			t.Run(end.name+"/"+write, func(t *testing.T) {
				a, ref := endedWithHeld(t)
				ids := a.answerIDs(ref.RunID)
				if len(ids) != 5 {
					t.Fatalf("%d answers held, want 5", len(ids))
				}
				// The reply to the first comment, or the first thread's
				// resolution: answer 0, or answer 3 (the replies before it
				// posted first).
				inFlight := 0
				match := func(r *http.Request, _ string) bool {
					return r.Method == "POST" && strings.Contains(r.URL.Path, fmt.Sprintf("/comments/%d/replies", a.first))
				}
				if write == "resolve" {
					inFlight = 3
					thread := a.w.gh.ThreadID(a.first)
					match = func(r *http.Request, body string) bool {
						return strings.HasSuffix(r.URL.Path, "/graphql") && strings.Contains(body, "resolveReviewThread") && strings.Contains(body, thread)
					}
				}
				b := newBarrier(match)
				defer b.open()
				poster := &delivery.Store{DB: a.w.app}
				gh := a.through(t, b)
				errs := make(chan error, 1)
				go func() { _, err := poster.PostFixerAnswers(context.Background(), a.w.org, a.task, gh); errs <- err }()
				select {
				case <-b.inside:
				case err := <-errs:
					t.Fatalf("the poster ended before its write: %v", err)
				case <-time.After(20 * time.Second):
					t.Fatal("the poster never reached its write")
				}
				end.end(a)
				// The end, done while the write is held: the in-flight
				// answer is not withheld.
				if got := a.outcome(t, ref.RunID, ids[inFlight]); got == "withheld" {
					t.Errorf("the answer being posted was withheld by the end")
				}
				b.open()
				if err := <-errs; err != nil {
					t.Logf("the poster after the end: %v", err)
				}
				// Passes after the end: the step's, and the syncer's.
				for range 3 {
					if _, err := a.store.PostFixerAnswers(context.Background(), a.w.org, a.task, a.gh); err != nil {
						t.Logf("a pass after the end: %v", err)
					}
					a.w.sync()
				}
				for i, id := range ids {
					got := a.outcome(t, ref.RunID, id)
					switch {
					case i == inFlight && got != "posted":
						t.Errorf("the answer in flight at the end (%d) ended %q, want posted", i, got)
					case i < inFlight && got != "posted":
						t.Errorf("answer %d, posted before the write, ended %q", i, got)
					case i > inFlight && got != "withheld":
						t.Errorf("answer %d, unclaimed at the end, ended %q, want withheld", i, got)
					}
				}
				if write == "reply" {
					if n := len(a.posted("Fixed: " + a.firstID)); n != 1 {
						t.Errorf("the reply in flight is on GitHub %d times, want once", n)
					}
					if n := len(a.posted("Fixed: " + a.secondID)); n != 0 {
						t.Errorf("a withheld reply is on GitHub %d times", n)
					}
				} else {
					if f, s := a.resolved(); !f || s {
						t.Errorf("resolved %v %v; want the thread in flight only", f, s)
					}
				}
				if n := a.claimsAfterEnd(); n != 0 {
					t.Errorf("%d claims after the end", n)
				}
				if n := a.w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'pull_request.answers_ended'`, a.task); n != 1 {
					t.Errorf("the end recorded %d times, want once", n)
				}
				// The conductor was told what was withheld and what was in
				// flight, then what it ended as.
				wakes := a.w.str(`SELECT COALESCE(string_agg(line, ' | ' ORDER BY created_at), '') FROM conductor_wakes WHERE task_id = $1 AND kind = $2
					AND key LIKE 'answers:run_fixer:%'`, a.task, delivery.WakeRound)
				told := a.told()
				label := a.firstID
				if write == "resolve" {
					label = "resolving " + a.firstID
				}
				if !strings.Contains(wakes, end.why) || !strings.Contains(wakes, "Being posted as it ended, so not withheld: "+label) {
					t.Errorf("the end's wake %q does not name what is in flight", wakes)
				}
				if !strings.Contains(wakes, "Withheld, never posted or resolved:") {
					t.Errorf("the end's wake %q does not name what was withheld", wakes)
				}
				if _, list, ok := strings.Cut(wakes, "Withheld, never posted or resolved: "); ok {
					list, _, _ = strings.Cut(list, ".")
					for _, l := range strings.Split(list, ", ") {
						if l == label {
							t.Errorf("the end's wake says the answer in flight (%s) was withheld: %q", label, wakes)
						}
					}
				}
				if !strings.Contains(told, "answer "+label+", being posted when its delivery ended ("+end.why+"), was posted") {
					t.Errorf("nobody was told the in-flight answer was posted: %q", told)
				}
			})
		}
	}
}

// A poster gone after GitHub took its reply, before recording it (a crash),
// and then the delivery stopped: the pr-reconciler's pass, once the claim
// has long expired, settles the answer from GitHub. Dude's own marked
// comment found: posted. Claimed but never on GitHub (its write failed):
// withheld, as ended before it was posted.
// The search cut at its bound: uncertain, said once, never reposted.
func TestRound5TheReconcilerSettlesAnAnswerItsPosterLeft(t *testing.T) {
	// crashOn has the poster crash on answer i after GitHub took it,
	// before its record.
	crashOn := func(a *answering, ref delivery.RunRef, i int) {
		t.Helper()
		ids := a.answerIDs(ref.RunID)
		crash := errors.New("the poster crashed")
		a.store.AfterAnswerPost = func(id string) error {
			if id == ids[i] {
				return crash
			}
			return nil
		}
		if _, err := a.post(); !errors.Is(err, crash) {
			t.Fatalf("the crashing post: %v", err)
		}
		a.store.AfterAnswerPost = nil
	}
	// The claim long past: the pass reads GitHub at once.
	expire := func(a *answering, ref delivery.RunRef) {
		mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '1 hour' WHERE run_id = $1 AND event_type = $2`,
			ref.RunID, delivery.EvAnswerClaimed)
	}
	stop := func(a *answering) { a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"}) }

	t.Run("its comment found", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		ids := a.answerIDs(ref.RunID)
		crashOn(a, ref, 2)
		expire(a, ref)
		stop(a)
		for range 3 {
			a.w.sync()
		}
		if got := a.outcome(t, ref.RunID, ids[2]); got != "posted" {
			t.Errorf("the answer GitHub took ended %q, want posted", got)
		}
		if n := len(a.posted("Fixed: " + a.is)); n != 1 {
			t.Errorf("the answer is on GitHub %d times, want once", n)
		}
		for _, i := range []int{3, 4} {
			if got := a.outcome(t, ref.RunID, ids[i]); got != "withheld" {
				t.Errorf("unclaimed answer %d ended %q, want withheld", i, got)
			}
		}
	})
	t.Run("never written", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		ids := a.answerIDs(ref.RunID)
		// The two line replies post; the conversation reply is claimed
		// and GitHub fails its write (a 502: it may or may not have
		// taken it), and the poster is gone.
		a.w.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				if r.Method == "POST" && strings.Contains(r.URL.Path, "/issues/1/comments") {
					return 502
				}
				return 0
			}
		})
		if _, err := a.post(); err == nil {
			t.Fatal("the write GitHub failed did not fail")
		}
		a.w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
		expire(a, ref)
		stop(a)
		for range 3 {
			a.w.sync()
		}
		if n := len(a.posted("Fixed: " + a.is)); n != 0 {
			t.Fatalf("the answer is on GitHub %d times, want none", n)
		}
		if got := a.outcome(t, ref.RunID, ids[2]); got != "withheld" {
			t.Errorf("the answer never written ended %q, want withheld", got)
		}
		why := a.w.str(`SELECT COALESCE(payload->>'error', '') FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'answerId' = $3`,
			ref.RunID, delivery.EvAnswerFailed, ids[2])
		if why != "the delivery ended before it was posted" {
			t.Errorf("withheld for %q", why)
		}
	})
	t.Run("the search inconclusive", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		ids := a.answerIDs(ref.RunID)
		for i := range 1001 {
			a.w.gh.Comment(1, "ana", fmt.Sprintf("old discussion %d", i))
		}
		crashOn(a, ref, 2)
		expire(a, ref)
		stop(a)
		for range 3 {
			a.w.sync()
		}
		if n := len(a.posted("Fixed: " + a.is)); n != 1 {
			t.Errorf("the answer is on GitHub %d times, want once: never reposted", n)
		}
		if got := a.outcome(t, ref.RunID, ids[2]); got != "uncertain" {
			t.Errorf("the answer the search could not find ended %q, want uncertain", got)
		}
		if n := a.w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type = 'chat.notice'
			AND payload->>'text' LIKE '%may have been posted; check the pull request%'`, a.task); n != 1 {
			t.Errorf("%d notices of the uncertain answer, want one", n)
		}
		if n := a.w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id = $1 AND line LIKE '%may have been posted; check the pull request%'`, a.task); n > 1 {
			t.Errorf("%d wakes of the uncertain answer, want at most one", n)
		}
	})
}

func expireReconciliationAnswer(t *testing.T, a *answering, ref delivery.RunRef) {
	t.Helper()
	mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '1 hour' WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerClaimed)
	mustExec(t, a.w.owner, `UPDATE answer_reconciliations SET due_at = now() - interval '1 second', created_at = now() - interval '1 hour' WHERE run_id = $1`, ref.RunID)
}

// The reconciliation due time has a full extra lease beyond the claim lease:
// at claim+lease a bounded GitHub write may still be in flight, while at
// claim+2*lease the row is eligible. The timestamps are controlled in SQL;
// this test does not sleep or depend on wall-clock passage.
func TestRound7ReconciliationLeaseMargin(t *testing.T) {
	a, ref := endedWithHeld(t)
	ids := a.answerIDs(ref.RunID)
	b := newBarrier(func(r *http.Request, _ string) bool {
		return r.Method == "POST" && strings.Contains(r.URL.Path, fmt.Sprintf("/comments/%d/replies", a.first))
	})
	defer b.open()
	errs := make(chan error, 1)
	go func() {
		_, err := a.store.PostFixerAnswers(context.Background(), a.w.org, a.task, a.through(t, b))
		errs <- err
	}()
	<-b.inside
	a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
	var claimed time.Time
	if err := a.w.owner.QueryRow(context.Background(), `SELECT occurred_at FROM events WHERE id=$1`, ids[0]).Scan(&claimed); err != nil {
		t.Fatal(err)
	}
	a.w.prs.Answers.Now = func() time.Time { return claimed.Add(2 * time.Minute) }
	if n, err := a.w.prs.Answers.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("claim+lease sweep settled %d: %v", n, err)
	}
	a.w.prs.Answers.Now = func() time.Time { return claimed.Add(4 * time.Minute) }
	mustExec(t, a.w.owner, `UPDATE answer_reconciliations SET due_at = now() - interval '1 second' WHERE answer_id = $1`, ids[0])
	if n, err := a.w.prs.Answers.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("claim+2*lease sweep settled %d: %v", n, err)
	}
	b.open()
	<-errs
}

// but also its terminal action marker. A resolved thread is posted exactly once;
// a conclusive absent or unresolved thread is withheld; an unreadable thread is
// uncertain and is announced once. The second sync is deliberately included:
// settled_at must fence duplicate notices and wakes.
func TestRound7ResolutionReconciliationOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*answering, delivery.RunRef, []string)
		want      string
		resolved  bool
		uncertain bool
	}{
		{"resolved", func(a *answering, ref delivery.RunRef, ids []string) {
			crashOn := errors.New("resolution poster crashed")
			a.store.AfterAnswerPost = func(id string) error {
				if id == ids[3] {
					return crashOn
				}
				return nil
			}
			if _, err := a.post(); !errors.Is(err, crashOn) {
				t.Fatalf("resolution crash: %v", err)
			}
			a.store.AfterAnswerPost = nil
			a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
			expireReconciliationAnswer(t, a, ref)
		}, "posted", true, false},
		{"absent", func(a *answering, ref delivery.RunRef, ids []string) {
			crash := errors.New("resolution poster crashed")
			a.store.AfterAnswerPost = func(id string) error {
				if id == ids[3] {
					return crash
				}
				return nil
			}
			if _, err := a.post(); !errors.Is(err, crash) {
				t.Fatalf("resolution crash: %v", err)
			}
			a.store.AfterAnswerPost = nil
			a.w.gh.Set(func(s *fakegithub.Server) { s.ResolvedThreads[s.ThreadID(a.first)] = false })
			a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
			expireReconciliationAnswer(t, a, ref)
		}, "withheld", false, false},
		{"unresolved", func(a *answering, ref delivery.RunRef, ids []string) {
			crash := errors.New("resolution poster crashed")
			a.store.AfterAnswerPost = func(id string) error {
				if id == ids[3] {
					return crash
				}
				return nil
			}
			if _, err := a.post(); !errors.Is(err, crash) {
				t.Fatalf("resolution crash: %v", err)
			}
			a.store.AfterAnswerPost = nil
			a.w.gh.Set(func(s *fakegithub.Server) { s.ResolvedThreads[s.ThreadID(a.first)] = false })
			a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
			expireReconciliationAnswer(t, a, ref)
		}, "withheld", false, false},
		{"unreadable", func(a *answering, ref delivery.RunRef, ids []string) {
			crash := errors.New("resolution poster crashed")
			a.store.AfterAnswerPost = func(id string) error {
				if id == ids[3] {
					return crash
				}
				return nil
			}
			if _, err := a.post(); !errors.Is(err, crash) {
				t.Fatalf("resolution crash: %v", err)
			}
			a.store.AfterAnswerPost = nil
			a.w.gh.Set(func(s *fakegithub.Server) {
				s.ThreadsStatus = 502
				s.ResolvedThreads[s.ThreadID(a.first)] = false
			})
			expireReconciliationAnswer(t, a, ref)
			a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
		}, "uncertain", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a, ref := endedWithHeld(t)
			ids := a.answerIDs(ref.RunID)
			tc.setup(a, ref, ids)
			for range 2 {
				a.w.sync()
			}
			a.w.sync()
			if got := a.outcome(t, ref.RunID, ids[3]); got != tc.want {
				t.Fatalf("resolution outcome %q, want %q", got, tc.want)
			}
			first, second := a.resolved()
			if first != tc.resolved {
				t.Errorf("first thread resolved=%v, want %v", first, tc.resolved)
			}
			if n := a.w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='chat.notice' AND payload->>'about'='answer_reconciled' AND payload->>'answerId'=$2`, a.task, ids[3]); n != 1 {
				t.Errorf("%d reconciliation notices, want 1", n)
			}
			if n := a.w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND key=$2`, a.task, "answer-reconciled:"+ids[3]); n > 1 {
				t.Errorf("%d reconciliation wakes, want at most 1", n)
			}
			if tc.uncertain {
				if n := a.w.count(`SELECT count(*) FROM events WHERE task_id=$1 AND event_type='chat.notice' AND payload->>'text' LIKE '%may have been posted; check the pull request%'`, a.task); n != 1 {
					t.Errorf("%d uncertainty notices, want 1", n)
				}
				if n := a.w.count(`SELECT count(*) FROM conductor_wakes WHERE task_id=$1 AND line LIKE '%may have been posted; check the pull request%'`, a.task); n > 1 {
					t.Errorf("%d uncertainty wakes, want at most 1", n)
				}
			}
			if second {
				t.Errorf("second thread unexpectedly resolved")
			}
		})
	}
}

func mustReadBody(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(b))
	return b
}

// after it, claims nothing: the end's record stops it. Here the first
// poster is inside its write of the first reply, its claim taken for
// lapsed; a second, stale, poster listed the answers before the end and
// reaches its claims after it. Without the end's record it would take the
// lapsed claim over and, finding no comment yet, post it again.
func TestRound5AStalePosterClaimsNothingAfterTheEnd(t *testing.T) {
	a, ref := endedWithHeld(t)
	ids := a.answerIDs(ref.RunID)
	first := newBarrier(func(r *http.Request, _ string) bool {
		return r.Method == "POST" && strings.Contains(r.URL.Path, fmt.Sprintf("/comments/%d/replies", a.first))
	})
	defer first.open()
	p1 := &delivery.Store{DB: a.w.app}
	errs := make(chan error, 2)
	go func() {
		_, err := p1.PostFixerAnswers(context.Background(), a.w.org, a.task, a.through(t, first))
		errs <- err
	}()
	<-first.inside
	mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '10 minutes' WHERE run_id = $1 AND event_type = $2`,
		ref.RunID, delivery.EvAnswerClaimed)
	// The stale poster, held reading the pull request: it has listed the
	// answers, and claims none yet.
	stale := newBarrier(func(r *http.Request, _ string) bool {
		return r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls/1")
	})
	defer stale.open()
	p2 := &delivery.Store{DB: a.w.app}
	go func() {
		_, err := p2.PostFixerAnswers(context.Background(), a.w.org, a.task, a.through(t, stale))
		errs <- err
	}()
	<-stale.inside
	a.w.stop(a.w.ownedBy(a.task, "Ana"), a.task, map[string]any{"note": "enough"})
	stale.open()
	if err := <-errs; err != nil {
		t.Logf("the stale poster: %v", err)
	}
	first.open()
	if err := <-errs; err != nil {
		t.Logf("the first poster: %v", err)
	}
	if n := a.claimsAfterEnd(); n != 0 {
		t.Errorf("%d claims after the end", n)
	}
	if n := len(a.posted("Fixed: " + a.firstID)); n != 1 {
		t.Errorf("the reply in flight is on GitHub %d times, want once", n)
	}
	if got := a.outcome(t, ref.RunID, ids[0]); got != "posted" {
		t.Errorf("the reply in flight ended %q, want posted", got)
	}
	for _, i := range []int{1, 2, 3, 4} {
		if got := a.outcome(t, ref.RunID, ids[i]); got != "withheld" {
			t.Errorf("unclaimed answer %d ended %q, want withheld", i, got)
		}
	}
}

// Another poster's pass finds the fix gone from the pull request (a
// force-push) while the first poster is inside its write of the first
// reply: the pass withholds what nobody holds, never the answer being
// written, which its poster then records posted. Before, the pass withheld
// it too, and the poster, finding it settled, recorded nothing: GitHub
// had a reply dude's records said was withheld.
func TestRound5AWithholdingPassNeverOvertakesAPosterInFlight(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	before := a.head()
	fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
	a.published(ref, fix, "completed")
	ids := a.answerIDs(ref.RunID)
	b := newBarrier(func(r *http.Request, _ string) bool {
		return r.Method == "POST" && strings.Contains(r.URL.Path, fmt.Sprintf("/comments/%d/replies", a.first))
	})
	defer b.open()
	p1 := &delivery.Store{DB: a.w.app}
	errs := make(chan error, 1)
	go func() {
		_, err := p1.PostFixerAnswers(context.Background(), a.w.org, a.task, a.through(t, b))
		errs <- err
	}()
	<-b.inside
	forceBranch(t, a.w.gh, a.w.gh.Pull(1).Head, before)
	if _, err := (&delivery.Store{DB: a.w.app}).PostFixerAnswers(context.Background(), a.w.org, a.task, a.gh); err != nil {
		t.Logf("the withholding pass: %v", err)
	}
	if got := a.outcome(t, ref.RunID, ids[0]); got == "withheld" {
		t.Errorf("the answer being written was withheld by another pass")
	}
	b.open()
	if err := <-errs; err != nil {
		t.Logf("the poster in flight: %v", err)
	}
	if n := len(a.posted("Fixed: " + a.firstID)); n != 1 {
		t.Errorf("the reply in flight is on GitHub %d times, want once", n)
	}
	if got := a.outcome(t, ref.RunID, ids[0]); got != "posted" {
		t.Errorf("the reply GitHub took ended %q, want posted", got)
	}
	for _, i := range []int{1, 2, 3, 4} {
		if got := a.outcome(t, ref.RunID, ids[i]); got != "withheld" {
			t.Errorf("answer %d ended %q, want withheld", i, got)
		}
	}
}
