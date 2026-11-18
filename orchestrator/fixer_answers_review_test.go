package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// #485 round 3: the reviews' findings on round 2, as tests.

// Reviewer (rev485t-t, finding 2; rev485t-q F1): a fixer that published to
// another repository only, none of it on the pull request its answers are
// on, has them withheld there: "its fix did not change target".
func TestReviewerAnswersWhereTheFixerPublishedNothing(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	if err := a.w.app.InOrg(context.Background(), a.w.org, func(tx pgx.Tx) error {
		_, err := ledger.Append(context.Background(), tx, ref.Event(delivery.EvGitCommitCreated, ledger.ActorAgent,
			map[string]any{"repo": "elsewhere", "headSha": "deadbeef"}))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a.end(ref, "completed")
	done, err := a.post()
	t.Logf("posted %+v %v", done, err)
	if n := len(a.posted("Fixed: " + a.firstID)); n != 0 {
		t.Errorf("posted %d times on target#1, where the fixer published nothing", n)
	}
	a.countPosted(t, "Fixed:", 0)
	if f, s := a.resolved(); f || s {
		t.Errorf("resolved %v %v where the fixer published nothing", f, s)
	}
	if err != nil || len(done) != 1 || done[0].Withheld != "its fix did not change target" {
		t.Errorf("want withheld with the reason, got %+v %v", done, err)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'withheld' = 'true'`,
		ref.RunID, delivery.EvAnswerFailed); n != 5 {
		t.Errorf("%d answers withheld, want 5", n)
	}
	if strings.Contains(done[0].Line(), "is on the pull request, so dude posted") || !strings.Contains(done[0].Line(), "is not there") {
		t.Errorf("the wake does not say the fix is not there: %s", done[0].Line())
	}
}

// ghForges is a delivery.Forges that answers with one GitHub, or err.
type ghForges struct {
	gh  *forge.GitHub
	err error
}

func (f ghForges) For(context.Context, string) (*forge.GitHub, error) { return f.gh, f.err }

// step is the delivery step's posting (fixerAnswers), once: what the step
// fails or waits with, nil when it goes on.
func (a *answering) step() error {
	return a.store.FixerAnswersStep(context.Background(), a.w.org, a.task, ghForges{gh: a.gh})
}

// heldUnsettled counts the fixer's answers neither posted nor failed.
func (a *answering) heldUnsettled(run string) int {
	return a.w.count(`SELECT count(*) FROM events h WHERE h.run_id = $1 AND h.event_type = $2
		AND NOT EXISTS (SELECT 1 FROM events s WHERE s.run_id = h.run_id
			AND s.event_type IN ('chat.message', 'pull_request.action', $3)
			AND (s.payload->>'answerId' = h.id OR s.payload->'github'->>'answerId' = h.id))`,
		run, delivery.EvAnswerHeld, delivery.EvAnswerFailed)
}

// stepGoesOn fails the test unless the step neither failed nor waited
// (nothing a retry counts towards dead-lettering), the fixer's five
// answers all still held, none failed, none on GitHub.
func (a *answering) stepGoesOn(t *testing.T, ref delivery.RunRef, what string) {
	t.Helper()
	for i := range 5 {
		if err := a.step(); err != nil {
			t.Errorf("%s, attempt %d: the step fails with %v", what, i+1, err)
		}
	}
	if n := a.heldUnsettled(ref.RunID); n != 5 {
		t.Errorf("%s: %d answers still held, want 5", what, n)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerFailed); n != 0 {
		t.Errorf("%s: %d answers settled as failed or withheld, want none", what, n)
	}
	a.countPosted(t, "Fixed:", 0)
	why := a.w.str(`SELECT COALESCE(string_agg(payload->>'why', ' | '), '') FROM events WHERE run_id = $1 AND event_type = $2`,
		ref.RunID, delivery.EvAnswerUnverified)
	if why == "" {
		t.Errorf("%s: no reason recorded for the answers held", what)
	}
	t.Logf("%s: recorded %q", what, why)
}

// Reviewer (rev485t-t, finding 1; rev485t-q F2), adopted: GitHub refusing
// the containment compare outright (a 403: a token that may no longer
// read compares) is neither transient nor proof the fix is absent. The
// step goes on (no failure for a retry to count towards dead-lettering),
// the answers stay held, its reason recorded; once GitHub answers, they
// post.
func TestReviewerAComparisonRefusalDoesNotFailTheStep(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
	a.published(ref, fix, "completed")
	a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "a person's commit on top")
	refuse := true
	a.w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if refuse && strings.Contains(r.URL.Path, "/compare/") {
				return 403
			}
			return 0
		}
	})
	for i := range 5 {
		done, err := a.post()
		if err != nil && !forge.Transient(err) {
			t.Errorf("attempt %d: a non-transient error the step fails on (forgeWait passes it through): %v (done %+v)", i+1, err, done)
		}
	}
	a.stepGoesOn(t, ref, "the compare refused")
	if why := a.w.str(`SELECT COALESCE(string_agg(payload->>'why', ' | '), '') FROM events WHERE run_id = $1 AND event_type = $2`,
		ref.RunID, delivery.EvAnswerUnverified); !strings.Contains(why, "comparison with the fix could not be read: github 403") {
		t.Errorf("the reason recorded: %q", why)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerUnverified); n != 1 {
		t.Errorf("the same reason recorded %d times, want once", n)
	}
	a.w.gh.Set(func(*fakegithub.Server) { refuse = false })
	if err := a.step(); err != nil {
		t.Fatalf("once GitHub compares: %v", err)
	}
	a.countPosted(t, "Fixed:", 1)
}

// Every read containment needs, refused (403, 401), and the posting's own
// database failing (not GitHub at all), leave the step going on and the
// answers held; so does no GitHub at all.
func TestAFixersAnswersThatCannotBeVerifiedStayHeld(t *testing.T) {
	for _, c := range []struct {
		name   string
		path   string
		status int
	}{
		{"the pull request refused", "/pulls/1", 403},
		{"the branch refused", "/git/ref/heads/", 401},
		{"the compare refused", "/compare/", 403},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := newAnswering(t, newWorld(t))
			ref := a.fixer("run_fixer")
			a.answerAll(t, ref, "Fixed:")
			fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
			a.published(ref, fix, "completed")
			a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "a person's commit on top")
			a.w.gh.Set(func(s *fakegithub.Server) {
				s.Intercept = func(r *http.Request) int {
					if r.Method == "GET" && (strings.HasSuffix(r.URL.Path, c.path) || strings.Contains(r.URL.Path, c.path) && c.path != "/pulls/1") {
						return c.status
					}
					return 0
				}
			})
			a.stepGoesOn(t, ref, c.name)
		})
	}
	t.Run("a database error", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		a.store.BeforeAnswerClaim = func(string) error { return errors.New("the database went away") }
		for i := range 5 {
			if err := a.step(); err != nil {
				t.Errorf("attempt %d: the step fails with %v", i+1, err)
			}
		}
		if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerFailed); n != 0 {
			t.Errorf("%d answers settled as failed, want none", n)
		}
		if n := a.heldUnsettled(ref.RunID); n != 5 {
			t.Errorf("%d answers still held, want 5", n)
		}
		a.countPosted(t, "Fixed:", 0)
		a.store.BeforeAnswerClaim = nil
		if err := a.step(); err != nil {
			t.Fatal(err)
		}
		a.countPosted(t, "Fixed:", 1)
	})
	t.Run("no GitHub", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		for _, f := range []ghForges{{}, {err: errors.New("no credential")}} {
			if err := a.store.FixerAnswersStep(context.Background(), a.w.org, a.task, f); err != nil {
				t.Errorf("no GitHub (%v): the step fails with %v", f.err, err)
			}
		}
		if n := a.heldUnsettled(ref.RunID); n != 5 {
			t.Errorf("%d answers still held, want 5", n)
		}
	})
}

// Answers that cannot be verified are not held forever: past 24 hours
// from the fixer's end (an injected clock), the next pass withholds them,
// saying why, and the conductor is woken; before it, they stay held.
func TestAFixersUnverifiableAnswersAreWithheldAfterADay(t *testing.T) {
	w := conducting(t)
	a := newAnswering(t, w)
	if status, out := w.chat(a.task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
	a.published(ref, fix, "completed")
	a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "a person's commit on top")
	a.w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if strings.Contains(r.URL.Path, "/compare/") {
				return 403
			}
			return 0
		}
	})
	ended, err := time.Parse(time.RFC3339Nano, a.w.str(`SELECT to_char(ended_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"') FROM runs WHERE id = $1`, ref.RunID))
	if err != nil {
		t.Fatal(err)
	}
	clock := ended.Add(23*time.Hour + 59*time.Minute)
	a.store.Now = func() time.Time { return clock }
	a.stepGoesOn(t, ref, "a minute short of a day")
	clock = ended.Add(24*time.Hour + time.Minute)
	if err := a.step(); err != nil {
		t.Fatalf("past a day: %v", err)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'withheld' = 'true'
		AND payload->>'error' LIKE 'could not verify the fix is on the pull request: %github 403%'`, ref.RunID, delivery.EvAnswerFailed); n != 5 {
		t.Errorf("%d answers withheld past the bound with the reason, want 5", n)
	}
	a.countPosted(t, "Fixed:", 0)
	wake := w.str(`SELECT COALESCE(string_agg(line, ' | '), '') FROM conductor_wakes WHERE task_id = $1 AND kind = $2 AND key LIKE 'answers:run_fixer:%'`,
		a.task, delivery.WakeRound)
	if !strings.Contains(wake, "could not verify the fix is on the pull request") {
		t.Errorf("the conductor's wake %q does not say why", wake)
	}
}

// endedWithHeld is a conducted delivery waiting on its pull request, a
// fixer's five answers held on it, published, its Run ended, and the
// conductor live: no step has posted them yet.
func endedWithHeld(t *testing.T) (*answering, delivery.RunRef) {
	w := conducting(t)
	a := newAnswering(t, w)
	if status, out := w.chat(a.task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	a.published(ref, a.head(), "completed")
	return a, ref
}

// settledOnEnd fails the test unless the fixer's five answers are all
// withheld for why, none posted or resolved, and the conductor woken
// saying so.
func (a *answering) settledOnEnd(t *testing.T, ref delivery.RunRef, why string) {
	t.Helper()
	if n := a.heldUnsettled(ref.RunID); n != 0 {
		t.Errorf("%d answers left held, want none", n)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'withheld' = 'true'
		AND payload->>'error' = $3`, ref.RunID, delivery.EvAnswerFailed, why); n != 5 {
		t.Errorf("%d answers withheld for %q, want 5", n, why)
	}
	a.countPosted(t, "Fixed:", 0)
	if f, s := a.resolved(); f || s {
		t.Errorf("resolved %v %v", f, s)
	}
	wake := a.w.str(`SELECT COALESCE(string_agg(line, ' | '), '') FROM conductor_wakes WHERE task_id = $1 AND kind = $2 AND key LIKE 'answers:run_fixer:%'`,
		a.task, delivery.WakeRound)
	if !strings.Contains(wake, why) {
		t.Errorf("the conductor's wake %q does not say %q", wake, why)
	}
	// Ended, a fix can no longer reach the pull request: the wake does not
	// ask for one.
	if strings.Contains(wake, "once a fix is on the pull request") || strings.Contains(wake, "is not there") {
		t.Errorf("the conductor's wake %q waits for a fix on an ended delivery", wake)
	}
}

// A delivery that ends with a fixer's answers held — its pull request
// merged or closed on GitHub before any step posted them, or a person's
// Stop — reaches no step that posts them: they are withheld, saying why,
// and the conductor is woken. Every held answer is settled.
func TestAFixersHeldAnswersAreSettledWhenTheDeliveryEnds(t *testing.T) {
	t.Run("merged", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		a.w.gh.Merge(1)
		a.w.until("the task done", func() bool {
			a.w.sync()
			return a.w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'done'`, a.task) == 1
		})
		a.settledOnEnd(t, ref, "the pull request was merged")
	})
	t.Run("closed", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		a.w.gh.Close(1)
		a.w.until("the task aborted", func() bool {
			a.w.sync()
			return a.w.count(`SELECT count(*) FROM tasks WHERE id = $1 AND status = 'aborted'`, a.task) == 1
		})
		a.settledOnEnd(t, ref, "the pull request was closed")
	})
	t.Run("stopped", func(t *testing.T) {
		a, ref := endedWithHeld(t)
		ana := a.w.ownedBy(a.task, "Ana")
		a.w.stop(ana, a.task, map[string]any{"note": "enough"})
		a.settledOnEnd(t, ref, "the delivery was stopped")
	})
}

// A fixer with many answers (160: past where a key naming each id
// overflowed the wake's unique index, about 100) still wakes its
// conductor, once, on a key of fixed size.
func TestAFixerWithManyAnswersWakesItsConductorOnce(t *testing.T) {
	w := conducting(t)
	a := newAnswering(t, w)
	if status, out := w.chat(a.task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	ref := a.fixer("run_fixer")
	for i := range 160 {
		if r, err := a.reply(ref, a.is, fmt.Sprintf("Fixed: part %d", i)); err != nil || r.Held == "" {
			t.Fatalf("held reply %d: %+v %v", i, r, err)
		}
	}
	a.published(ref, a.head(), "completed")
	a.w.gh.Close(1)
	for range 3 {
		if err := a.step(); err != nil {
			t.Fatalf("the step: %v", err)
		}
	}
	if n := a.heldUnsettled(ref.RunID); n != 0 {
		t.Errorf("%d answers left held", n)
	}
	keys := w.ids(`SELECT key FROM conductor_wakes WHERE task_id = $1 AND kind = $2 AND key LIKE 'answers:run_fixer:%'`, a.task, delivery.WakeRound)
	if len(keys) != 1 {
		t.Fatalf("%d wakes for the fixer's 160 answers, want one: %v", len(keys), keys)
	}
	if len(keys[0]) > len("answers:run_fixer:")+16 {
		t.Errorf("the wake's key grows with the answers: %d bytes", len(keys[0]))
	}
	ids := w.ids(`SELECT id FROM events WHERE run_id = $1 AND event_type = $2 ORDER BY cursor`, ref.RunID, delivery.EvAnswerHeld)
	if want := delivery.AnswersKeyForIDs(ref.RunID, ids); keys[0] != want {
		t.Errorf("the wake's key %q, want %q (the answers it settled)", keys[0], want)
	}
}

// Reviewer R9 (rev485t-t, finding 3): two posters truly racing on a held
// reply's claim. Store.BeforeAnswerClaim holds the first poster after it
// read the answer unsettled and unclaimed, until the second has read it
// too (or two seconds pass): under the task's Chat lock the second cannot
// read until the first commits its claim, so the wait runs out and it
// finds the claim; without the lock both read "no claim", both claim, and
// both post. Each answer is on GitHub once.
func TestAFixersRacingPostersPostEachAnswerOnce(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	a.published(ref, a.head(), "completed")
	var mu sync.Mutex
	arrived := 0
	both := make(chan struct{})
	a.store.BeforeAnswerClaim = func(string) error {
		mu.Lock()
		arrived++
		n := arrived
		if n == 2 {
			close(both)
		}
		mu.Unlock()
		if n == 1 {
			select {
			case <-both:
			case <-time.After(2 * time.Second):
			}
		}
		return nil
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, _ = a.post() }()
	}
	close(start)
	wg.Wait()
	a.store.BeforeAnswerClaim = nil
	for _, to := range []string{a.firstID, a.secondID, a.is} {
		if got := len(a.posted("Fixed: " + to)); got > 1 {
			t.Errorf("racing posters put the answer to %s on GitHub %d times", to, got)
		}
	}
	// What the poster that found a claim left is posted past its lease.
	mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '10 minutes' WHERE run_id = $1 AND event_type = $2`,
		ref.RunID, delivery.EvAnswerClaimed)
	if err := a.step(); err != nil {
		t.Fatal(err)
	}
	a.countPosted(t, "Fixed:", 1)
}

// Reviewer R12 (rev485t-t, finding 3): the head is read from the pull
// request's branch, not its own read, which trails a push by seconds. A
// fix force-pushed away while the pull request's read still shows it
// (fakegithub's LaggingHead) has its answers withheld.
func TestAFixersAnswersAreWithheldWhenOnlyAStalePullRequestReadShowsTheFix(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	before := a.head()
	fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
	a.published(ref, fix, "completed")
	forceBranch(t, a.w.gh, a.w.gh.Pull(1).Head, before)
	a.w.gh.Set(func(s *fakegithub.Server) { s.LaggingHead = map[int]string{1: fix} })
	done, err := a.post()
	if err != nil || len(done) != 1 || !strings.Contains(done[0].Withheld, "does not contain the published fix") {
		t.Fatalf("the branch force-pushed away, the pull request's read still at the fix: %+v %v", done, err)
	}
	a.countPosted(t, "Fixed:", 0)
	if f, s := a.resolved(); f || s {
		t.Errorf("resolved %v %v", f, s)
	}
}

// Reviewer R7 (rev485t-t, finding 3): a fixer's replies land before its
// threads are resolved — "say so in it first" — as GitHub took them
// (fakegithub's write log).
func TestAFixersRepliesLandBeforeItsThreadsAreResolved(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	a.published(ref, a.head(), "completed")
	from := len(a.w.gh.Writes())
	if err := a.step(); err != nil {
		t.Fatal(err)
	}
	writes := a.w.gh.Writes()[from:]
	at := func(w string) int { return slices.Index(writes, w) }
	for _, c := range []int64{a.first, a.second} {
		reply, resolve := at(fmt.Sprintf("reply line-comment-%d", c)), at("resolve "+a.w.gh.ThreadID(c))
		if reply < 0 || resolve < 0 || resolve < reply {
			t.Errorf("thread %d: its reply at %d, its resolution at %d, in %v; want the reply first", c, reply, resolve, writes)
		}
	}
}

// R14, defence in depth: a poster that outlived its claim's lease between
// GitHub taking its reply and its record (a stall: AfterAnswerPost) finds
// the answer settled by the poster that took the stale claim over (which
// found the reply by its marker) and records nothing more: each answer is
// on GitHub once and recorded once.
func TestAFixersAnswerOutlivingItsClaimIsRecordedOnce(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	a.published(ref, a.head(), "completed")
	stalled, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	a.store.AfterAnswerPost = func(string) error {
		once.Do(func() { close(stalled); <-resume })
		return nil
	}
	errs := make(chan error, 1)
	go func() { _, err := a.post(); errs <- err }()
	<-stalled
	// The stalled poster's claim, past its lease: another takes it over.
	mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '10 minutes' WHERE run_id = $1 AND event_type = $2`,
		ref.RunID, delivery.EvAnswerClaimed)
	other := &delivery.Store{DB: a.w.app}
	if _, err := other.PostFixerAnswers(context.Background(), a.w.org, a.task, a.gh); err != nil {
		t.Fatalf("the poster taking over: %v", err)
	}
	close(resume)
	if err := <-errs; err != nil {
		t.Fatalf("the stalled poster: %v", err)
	}
	a.countPosted(t, "Fixed:", 1)
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message' AND payload->'github'->>'answerId' IS NOT NULL`,
		ref.RunID); n != 3 {
		t.Errorf("%d replies recorded, want 3: one record per answer", n)
	}
}

// F5 (rev485t-q): a recovering poster finds an earlier post of an answer
// by its marker only in a comment by dude's own login. Someone else's
// comment carrying the answer's marker and dude's reply marker (anyone can
// type them) is not taken for dude's post: the real answer is posted.
func TestAFixersAnswerIsNotTakenForSomeoneElsesMarkedComment(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	if r, err := a.reply(ref, a.is, "Fixed: renamed it."); err != nil || r.AnswerID == "" {
		t.Fatalf("held reply: %+v %v", r, err)
	}
	id := a.w.str(`SELECT id FROM events WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerHeld)
	a.w.gh.Comment(1, "mallory", "Fixed: renamed it.\n\n"+forge.AnswerMarker(id)+"\n"+forge.ReplyMarker)
	a.published(ref, a.head(), "completed")
	// A poster that claimed it and was lost (a 502 GitHub may have taken):
	// the next recovers, looking for an earlier post first.
	down := true
	a.w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if down && r.Method == "POST" {
				return 502
			}
			return 0
		}
	})
	if _, err := a.post(); !forge.Transient(err) {
		t.Fatalf("GitHub down: %v", err)
	}
	a.w.gh.Set(func(*fakegithub.Server) { down = false })
	if err := a.step(); err != nil {
		t.Fatal(err)
	}
	var ours int
	p := a.w.gh.Pull(1)
	a.w.gh.Set(func(*fakegithub.Server) {
		for _, c := range p.Comments {
			if c.Author != "mallory" && strings.Contains(c.Body, "Fixed: renamed it.") {
				ours++
			}
		}
	})
	if ours != 1 {
		t.Errorf("dude's answer is on GitHub %d times, want once: someone else's marked comment was taken for it", ours)
	}
	recorded := a.w.str(`SELECT COALESCE(payload->'github'->>'feedbackId', '') FROM events WHERE run_id = $1 AND event_type = 'chat.message'`, ref.RunID)
	if recorded == "" || strings.HasSuffix(recorded, "-0") {
		t.Errorf("recorded %q", recorded)
	}
}
