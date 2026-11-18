package orchestrator_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/forge"
	"github.com/marciomartins/dude/orchestrator/internal/ledger"
)

// #485, decision (a): dude posts a pull request fixer's answers itself,
// once the fix is verified on the pull request. These tests drive the
// capture through delivery's own checks and the posting through
// Store.PostFixerAnswers, the step's work, on a fixer Run they write; the
// root tests in reply_publication_test.go drive whole deliveries.

// answering is a pull request waiting on people, with a person's line
// comments and conversation comment on it, recorded by a sync.
type answering struct {
	w                     *world
	task                  string
	first, second, issue  int64
	gh                    *forge.GitHub
	store                 *delivery.Store
	firstID, secondID, is string
}

func newAnswering(t *testing.T, w *world) *answering {
	a := &answering{w: w, task: w.reviewing()}
	a.first = w.gh.LineComment(1, "ana", "FACTORY.md", "Say why.")
	a.second = w.gh.LineComment(1, "ana", "FACTORY.md", "And here.")
	a.issue = w.gh.Comment(1, "ana", "Overall, rename it.")
	a.firstID, a.secondID, a.is = fmt.Sprintf("line-comment-%d", a.first), fmt.Sprintf("line-comment-%d", a.second), fmt.Sprintf("issue-comment-%d", a.issue)
	var err error
	if a.gh, err = (forge.Resolver{DB: w.app}).For(context.Background(), w.org); err != nil {
		t.Fatal(err)
	}
	a.store = &delivery.Store{DB: w.app}
	w.sync()
	return a
}

// fixer writes a running fix Run whose batch is the three comments.
func (a *answering) fixer(id string) delivery.RunRef {
	feedback := fmt.Sprintf(`[{"source":"review","id":"%s","author":"ana","body":"Say why.","kind":"line_comment"},
		{"source":"review","id":"%s","author":"ana","body":"And here.","kind":"line_comment"},
		{"source":"review","id":"%s","author":"ana","body":"Overall, rename it.","kind":"comment"}]`, a.firstID, a.secondID, a.is)
	mustExec(a.w.t, a.w.owner, `INSERT INTO runs (id, organization_id, project_id, task_id, attempt, status, phase, role, pr_feedback)
		VALUES ($1, $2, $3, $4, 1, 'running', 'fix', 'implementer', $5::jsonb)`, id, a.w.org, a.w.project, a.task, feedback)
	return delivery.RunRef{Org: a.w.org, ProjectID: a.w.project, TaskID: a.task, RunID: id}
}

// reply is the fixer's reply_on_pull_request, through delivery's three
// steps as the tool server takes them.
func (a *answering) reply(ref delivery.RunRef, to, text string) (delivery.Replied, error) {
	ctx := context.Background()
	var p delivery.ReplyPost
	if err := a.w.app.InOrg(ctx, a.w.org, func(tx pgx.Tx) (err error) {
		p, err = delivery.CheckReplyTx(ctx, tx, ref, a.gh, delivery.Reply{PR: "1", Text: text, InReplyTo: to})
		return err
	}); err != nil {
		return delivery.Replied{}, err
	}
	posted, err := delivery.PostReply(ctx, a.gh, p)
	if err != nil {
		return delivery.Replied{}, err
	}
	var r delivery.Replied
	return r, a.w.app.InOrg(ctx, a.w.org, func(tx pgx.Tx) (err error) {
		r, err = delivery.RecordReplyTx(ctx, tx, p, posted)
		return err
	})
}

// resolve is the fixer's resolve_thread, through delivery's three steps.
func (a *answering) resolve(ref delivery.RunRef, thread string) (map[string]any, error) {
	ctx := context.Background()
	var c delivery.PRActionCheck
	if err := a.w.app.InOrg(ctx, a.w.org, func(tx pgx.Tx) (err error) {
		c, err = delivery.CheckPRActionTx(ctx, tx, ref, a.gh, delivery.PRAction{Action: delivery.ActionResolveThread, PR: "1", Thread: thread})
		return err
	}); err != nil {
		return nil, err
	}
	done, err := delivery.DoPRAction(ctx, a.gh, c)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	return out, a.w.app.InOrg(ctx, a.w.org, func(tx pgx.Tx) (err error) {
		out, err = delivery.RecordPRActionTx(ctx, tx, c, done)
		return err
	})
}

// answerAll has the fixer answer all three, then resolve both threads.
func (a *answering) answerAll(t *testing.T, ref delivery.RunRef, prefix string) {
	t.Helper()
	for _, to := range []string{a.firstID, a.secondID, a.is} {
		r, err := a.reply(ref, to, prefix+" "+to)
		if err != nil || r.Held != delivery.AnswerHeldReply || r.URL != "" {
			t.Errorf("the fixer replying to %s: %+v %v", to, r, err)
		}
	}
	for _, th := range []string{a.firstID, a.secondID} {
		if out, err := a.resolve(ref, th); err != nil || out["held"] != delivery.AnswerHeldResolve {
			t.Errorf("the fixer resolving %s: %v %v", th, out, err)
		}
	}
}

// published records the fixer's publication at sha, as phases' publish
// does, and ends its Run with status.
func (a *answering) published(ref delivery.RunRef, sha, status string) {
	a.w.t.Helper()
	if err := a.w.app.InOrg(context.Background(), a.w.org, func(tx pgx.Tx) error {
		_, err := ledger.Append(context.Background(), tx, ref.Event(delivery.EvGitCommitCreated, ledger.ActorAgent,
			map[string]any{"repo": "target", "headSha": sha, "branch": a.w.gh.Pull(1).Head}))
		return err
	}); err != nil {
		a.w.t.Fatal(err)
	}
	a.end(ref, status)
}

func (a *answering) end(ref delivery.RunRef, status string) {
	mustExec(a.w.t, a.w.owner, `UPDATE runs SET status = $2, ended_at = now() WHERE id = $1`, ref.RunID, status)
}

// head is the pull request branch's commit on GitHub.
func (a *answering) head() string { return a.w.gh.SHA(a.w.gh.Pull(1).Head) }

// post is the step's posting, once.
func (a *answering) post() ([]delivery.FixerAnswers, error) {
	return a.store.PostFixerAnswers(context.Background(), a.w.org, a.task, a.gh)
}

// posted counts dude's comments on GitHub containing text, line comments
// and conversation comments alike, in the order GitHub has them.
func (a *answering) posted(text string) []string {
	var out []string
	p := a.w.gh.Pull(1)
	a.w.gh.Set(func(*fakegithub.Server) {
		for _, c := range append(append([]fakegithub.Comment{}, p.LineComments...), p.Comments...) {
			if strings.Contains(c.Body, text) {
				out = append(out, c.Body)
			}
		}
	})
	return out
}

func (a *answering) resolved() (first, second bool) {
	a.w.gh.Set(func(s *fakegithub.Server) {
		first, second = s.ResolvedThreads[s.ThreadID(a.first)], s.ResolvedThreads[s.ThreadID(a.second)]
	})
	return first, second
}

// countPosted fails the test unless each of the fixer's three answers
// (prefix) is on GitHub exactly n times.
func (a *answering) countPosted(t *testing.T, prefix string, n int) {
	t.Helper()
	for _, to := range []string{a.firstID, a.secondID, a.is} {
		if got := len(a.posted(prefix + " " + to)); got != n {
			t.Errorf("%s's answer to %s is on GitHub %d times, want %d", prefix, to, got, n)
		}
	}
}

// Reviewer B's gap test (F1, F2), adjusted to decision (a): every item of
// a three-item fixer batch (two line comments and an issue comment) is
// captured, not just the first or the line comments, and nothing reaches
// GitHub before publication; the conductor's reply and resolution, while
// that fixer is live, post at once. After publication all three are posted
// exactly once, in the order made, and both line threads are resolved.
func TestReviewerEveryFixerReplyHeldConductorImmediateDuringFix(t *testing.T) {
	w := conducting(t)
	a := newAnswering(t, w)
	if status, out := w.chat(a.task, "Look after the pull request for me."); status >= 300 {
		t.Fatalf("chat: %d %v", status, out)
	}
	ref := a.fixer("run_fixer")
	before := len(w.gh.Pull(1).Comments) + len(w.gh.Pull(1).LineComments)
	a.answerAll(t, ref, "Fixed:")
	if after := len(w.gh.Pull(1).Comments) + len(w.gh.Pull(1).LineComments); after != before {
		t.Errorf("%d comments on GitHub before publication, want %d", after, before)
	}
	if f, s := a.resolved(); f || s {
		t.Errorf("resolved before publication: %v %v", f, s)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = 'run_fixer' AND event_type = $1`, delivery.EvAnswerHeld); n != 5 {
		t.Errorf("%d answers captured, want 5", n)
	}
	// Nothing is posted while the fixer runs, however often the step runs.
	if done, err := a.post(); err != nil || len(done) != 0 {
		t.Errorf("posting while the fixer runs: %+v %v", done, err)
	}
	a.countPosted(t, "Fixed:", 0)

	// The conductor, the fixer still running, answers and resolves at once.
	out := w.must(a.task, "reply_on_pull_request", fmt.Sprintf(`{"pr":"1","text":"Because it reads better.","in_reply_to":"%s"}`, a.firstID))
	if out["url"] == "" || out["url"] == nil {
		t.Errorf("answered %v", out)
	}
	if got := len(a.posted("Because it reads better.")); got != 1 {
		t.Errorf("the conductor's reply is on GitHub %d times, want once at once", got)
	}
	w.must(a.task, "resolve_thread", fmt.Sprintf(`{"pr":"1","thread":"%s"}`, a.firstID))
	if f, _ := a.resolved(); !f {
		t.Error("the conductor's resolution did not reach GitHub at once")
	}

	// Published: each posted once, in the order the fixer made them.
	a.published(ref, a.head(), "completed")
	done, err := a.post()
	if err != nil || len(done) != 1 || len(done[0].Posted) != 3 || len(done[0].Resolved) != 2 {
		t.Fatalf("posting after publication: %+v %v", done, err)
	}
	a.countPosted(t, "Fixed:", 1)
	if f, s := a.resolved(); !f || !s {
		t.Errorf("after publication, resolved %v %v; want both threads", f, s)
	}
	got := w.ids(`SELECT payload->'github'->>'inReplyTo' FROM events WHERE run_id = 'run_fixer' AND event_type = 'chat.message' ORDER BY cursor`)
	if strings.Join(got, ",") != strings.Join([]string{a.firstID, a.secondID, a.is}, ",") {
		t.Errorf("posted in the order %v", got)
	}
	// Again: nothing more.
	if done, err := a.post(); err != nil || len(done) != 0 {
		t.Errorf("posting again: %+v %v", done, err)
	}
	a.countPosted(t, "Fixed:", 1)
}

// The posting step run again (a retried workflow step), run twice at once
// (concurrent syncs), and interrupted between GitHub taking a reply and its
// record (a crash): each answer is on GitHub once.
func TestAFixersAnswersPostExactlyOnce(t *testing.T) {
	t.Run("retried", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		for range 3 {
			if _, err := a.post(); err != nil {
				t.Fatal(err)
			}
		}
		a.countPosted(t, "Fixed:", 1)
	})
	t.Run("concurrent", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		// GitHub is slow to take a reply: both posters are at work at once.
		var inside atomic.Int32
		a.w.gh.Set(func(s *fakegithub.Server) {
			s.Intercept = func(r *http.Request) int {
				if r.Method == "POST" && strings.Contains(r.URL.Path, "comments") {
					inside.Add(1)
					for range 50 {
						if inside.Load() > 1 {
							break
						}
						sleepBriefly()
					}
				}
				return 0
			}
		})
		var wg sync.WaitGroup
		for range 2 {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = a.post() }()
		}
		wg.Wait()
		a.w.gh.Set(func(s *fakegithub.Server) { s.Intercept = nil })
		// The poster that found another's claim left it: what is left is
		// posted by a later step, past the claim's lease.
		mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '10 minutes' WHERE run_id = $1 AND event_type = $2`,
			ref.RunID, delivery.EvAnswerClaimed)
		if _, err := a.post(); err != nil {
			t.Fatal(err)
		}
		a.countPosted(t, "Fixed:", 1)
	})
	t.Run("crash after the post", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		crash := errors.New("the poster crashed")
		a.store.AfterAnswerPost = func(string) error { return crash }
		if _, err := a.post(); !errors.Is(err, crash) {
			t.Fatalf("the crashing post: %v", err)
		}
		if got := len(a.posted("Fixed: " + a.firstID)); got != 1 {
			t.Fatalf("GitHub took the first answer %d times before the crash", got)
		}
		a.store.AfterAnswerPost = nil
		// The crashed poster's claim stands for its lease: the next step
		// leaves it.
		if _, err := a.post(); err == nil {
			t.Error("a fresh claim was taken over")
		}
		mustExec(t, a.w.owner, `UPDATE events SET occurred_at = now() - interval '10 minutes' WHERE run_id = $1 AND event_type = $2`,
			ref.RunID, delivery.EvAnswerClaimed)
		if _, err := a.post(); err != nil {
			t.Fatal(err)
		}
		a.countPosted(t, "Fixed:", 1)
		if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'chat.message' AND payload->'github'->>'answerId' IS NOT NULL`, ref.RunID); n != 3 {
			t.Errorf("%d answers recorded, want 3", n)
		}
	})
}

// A transient GitHub failure leaves the answer to a later step; GitHub
// refusing one outright (the comment gone) fails it, recorded and
// reported, and the rest still post.
func TestAFixersAnswerFailuresAreTransientOrRecorded(t *testing.T) {
	a := newAnswering(t, newWorld(t))
	ref := a.fixer("run_fixer")
	a.answerAll(t, ref, "Fixed:")
	a.published(ref, a.head(), "completed")
	down := true
	a.w.gh.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			switch {
			case r.Method != "POST":
			case down && strings.Contains(r.URL.Path, "comments"):
				return 502
			case strings.Contains(r.URL.Path, fmt.Sprintf("/comments/%d/replies", a.second)):
				return 404
			}
			return 0
		}
	})
	if _, err := a.post(); err == nil || !forge.Transient(err) {
		t.Fatalf("GitHub down: %v", err)
	}
	a.countPosted(t, "Fixed:", 0)
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2`, ref.RunID, delivery.EvAnswerFailed); n != 0 {
		t.Errorf("%d answers failed while GitHub was down", n)
	}
	a.w.gh.Set(func(*fakegithub.Server) { down = false })
	done, err := a.post()
	if err != nil || len(done) != 1 || len(done[0].Failed) != 1 || !strings.Contains(done[0].Failed[0], a.secondID) {
		t.Fatalf("after GitHub came back: %+v %v", done, err)
	}
	if got := len(a.posted("Fixed: " + a.firstID)); got != 1 {
		t.Errorf("the first answer posted %d times", got)
	}
	if got := len(a.posted("Fixed: " + a.is)); got != 1 {
		t.Errorf("the conversation answer posted %d times", got)
	}
	if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'answerId' IS NOT NULL
		AND payload->>'withheld' IS NULL`, ref.RunID, delivery.EvAnswerFailed); n != 1 {
		t.Errorf("%d failures recorded, want the refused reply", n)
	}
	if again, err := a.post(); err != nil || len(again) != 0 {
		t.Errorf("posting again: %+v %v", again, err)
	}
}

// Containment: a fix published, then the pull request's branch force-moved
// to a head without it, has nothing posted or resolved; its answers are
// withheld, with why. A later push on top of the fix still contains it.
func TestAFixersAnswersPostOnlyWhereTheHeadContainsTheFix(t *testing.T) {
	t.Run("force-pushed away", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		before := a.head()
		fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
		a.published(ref, fix, "completed")
		forceBranch(t, a.w.gh, a.w.gh.Pull(1).Head, before)
		done, err := a.post()
		if err != nil || len(done) != 1 || !strings.Contains(done[0].Withheld, "does not contain the published fix") || len(done[0].Posted) != 0 {
			t.Fatalf("after the force-push: %+v %v", done, err)
		}
		a.countPosted(t, "Fixed:", 0)
		if f, s := a.resolved(); f || s {
			t.Errorf("resolved %v %v", f, s)
		}
		if n := a.w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = $2 AND payload->>'withheld' = 'true'`,
			ref.RunID, delivery.EvAnswerFailed); n != 5 {
			t.Errorf("%d answers withheld, want 5", n)
		}
		// The fix pushed back later does not bring them back: withheld is final.
		forceBranch(t, a.w.gh, a.w.gh.Pull(1).Head, fix)
		if again, err := a.post(); err != nil || len(again) != 0 {
			t.Errorf("after the fix came back: %+v %v", again, err)
		}
		a.countPosted(t, "Fixed:", 0)
	})
	t.Run("pushed on top", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		fix := a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "the fix")
		a.published(ref, fix, "completed")
		a.w.gh.CommitOnTop(a.w.gh.Pull(1).Head, "a person's commit on top")
		if done, err := a.post(); err != nil || len(done) != 1 || done[0].Withheld != "" {
			t.Fatalf("after a push on top: %+v %v", done, err)
		}
		a.countPosted(t, "Fixed:", 1)
	})
	t.Run("pull request closed", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "completed")
		a.w.gh.Close(1)
		if done, err := a.post(); err != nil || len(done) != 1 || !strings.Contains(done[0].Withheld, "closed") {
			t.Fatalf("on a closed pull request: %+v %v", done, err)
		}
		a.countPosted(t, "Fixed:", 0)
	})
}

// forceBranch moves branch to sha on the fake GitHub, as a force-push does.
func forceBranch(t *testing.T, gh *fakegithub.Server, branch, sha string) {
	t.Helper()
	if out, err := exec.Command("git", "-C", gh.Repo, "update-ref", "refs/heads/"+branch, sha).CombinedOutput(); err != nil {
		t.Fatalf("force-moving %s: %v %s", branch, err, out)
	}
}

// A fixer that ends before publishing (aborted, failed) posts nothing,
// ever; one aborted after its batch was published posts, as its code is on
// the pull request. Two published fixers post in the order they
// published, whatever order they were made in.
func TestAFixersAnswersFollowItsPublication(t *testing.T) {
	t.Run("ended before publication", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		aborted, failed := a.fixer("run_aborted"), a.fixer("run_failed")
		a.answerAll(t, aborted, "Aborted:")
		a.answerAll(t, failed, "Failed:")
		a.end(aborted, "aborted")
		a.end(failed, "failed")
		for range 3 {
			if done, err := a.post(); err != nil || len(done) != 0 {
				t.Errorf("posting: %+v %v", done, err)
			}
		}
		a.countPosted(t, "Aborted:", 0)
		a.countPosted(t, "Failed:", 0)
		// A commit published by another fixer later does not make them its.
		other := a.fixer("run_other")
		a.published(other, a.head(), "completed")
		if _, err := a.post(); err != nil {
			t.Fatal(err)
		}
		a.countPosted(t, "Aborted:", 0)
		a.countPosted(t, "Failed:", 0)
	})
	t.Run("aborted after publication", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		ref := a.fixer("run_fixer")
		a.answerAll(t, ref, "Fixed:")
		a.published(ref, a.head(), "aborted")
		if _, err := a.post(); err != nil {
			t.Fatal(err)
		}
		a.countPosted(t, "Fixed:", 1)
	})
	t.Run("publication order", func(t *testing.T) {
		a := newAnswering(t, newWorld(t))
		early, late := a.fixer("run_made_first"), a.fixer("run_made_second")
		a.answerAll(t, early, "First made:")
		a.answerAll(t, late, "Second made:")
		a.published(late, a.head(), "completed")
		a.published(early, a.head(), "completed")
		done, err := a.post()
		if err != nil || len(done) != 2 || done[0].RunID != "run_made_second" || done[1].RunID != "run_made_first" {
			t.Fatalf("posted %+v %v; want the fixer published first, first", done, err)
		}
		got := a.w.ids(`SELECT run_id FROM events WHERE task_id = $1 AND event_type = 'chat.message' AND payload->>'by' = 'fixer' ORDER BY cursor`, a.task)
		if len(got) != 6 || got[0] != "run_made_second" || got[2] != "run_made_second" || got[3] != "run_made_first" {
			t.Errorf("recorded in the order %v", got)
		}
	})
}

// sleepBriefly is a slow GitHub's pause.
func sleepBriefly() { time.Sleep(10 * time.Millisecond) }
