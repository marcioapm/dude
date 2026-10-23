package orchestrator_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/marciomartins/dude/orchestrator/internal/delivery"
	"github.com/marciomartins/dude/orchestrator/internal/fakeagent"
	"github.com/marciomartins/dude/orchestrator/internal/fakegithub"
	"github.com/marciomartins/dude/orchestrator/internal/fakelux"
	"github.com/marciomartins/dude/orchestrator/internal/lux"
)

// fixRow is one implement or fix Run as the test reads it.
type fixRow struct {
	ID, Phase, Continues, How, Branch string
}

func (w *world) writerRows(task string) []fixRow {
	w.t.Helper()
	rows, err := w.owner.Query(context.Background(), `SELECT id, phase::text, COALESCE(continues_run_id, ''), COALESCE(continues_how, ''),
			COALESCE(push_branch, '')
		FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') ORDER BY created_at, id`, task)
	if err != nil {
		w.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fixRow])
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// stuckReview is a task whose reviewer judges every fix of its finding
// "still": fix rounds follow each other until the policy's bound.
func stuckReview(t *testing.T, iterations int) (*world, string) {
	w := resuming(t)
	w.onModel("reviewer", fakeagent.StuckModel)
	wi := w.task()
	w.names(wi, w.repoID)
	p := resumingPolicy()
	p.MaxReviewIterations = iterations
	w.start(wi, p)
	return w, wi
}

// The implementer is taken back up for the first fix round and that fixer
// for the second: each a new row on the implementer's lux Run, told the
// round with what it fixes — the fix contract only the first time, since
// the implementer's instruction file lacks it. Both rounds push to the one
// per-Run branch, which publish keeps while the writer is kept: lux's lease
// is that branch's last push, so had the first round's publish deleted it
// the second round's push would be rejected.
func TestFixRoundsResumeTheImplementerThenTheFixerAndPushAgain(t *testing.T) {
	w, wi := stuckReview(t, 3)
	w.until("the second fix round published", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, wi) >= 2
	})
	writers := w.writerRows(wi)
	if len(writers) != 3 {
		t.Fatalf("writers: %+v; want the implementer and two fixers", writers)
	}
	impl, fix1, fix2 := writers[0], writers[1], writers[2]
	if fix1.Continues != impl.ID || fix1.How != delivery.ContinuesResume || fix2.Continues != fix1.ID || fix2.How != delivery.ContinuesResume {
		t.Fatalf("fix rounds: %+v; want the implementer resumed, then the first fixer", writers)
	}
	if fix1.Branch != impl.Branch || fix2.Branch != impl.Branch {
		t.Fatalf("push branches %q %q %q, want the implementer's throughout", impl.Branch, fix1.Branch, fix2.Branch)
	}
	if luxImpl, luxFix := w.luxRunsOf("implement"), w.luxRunsOf("fix"); len(luxImpl) != 1 || len(luxFix) != 0 || luxImpl[0].Resumed != 2 {
		t.Fatalf("%d implementers, %d fixers submitted to lux; want one implementer, resumed twice", len(luxImpl), len(luxFix))
	}
	// Each round's push was accepted and published: the task branch holds
	// the second round's commit.
	if n := w.count(`SELECT count(*) FROM runs WHERE id = ANY($1) AND push_result @> '{"results":[{"status":"pushed"}]}'`,
		[]string{fix1.ID, fix2.ID}); n != 2 {
		t.Fatalf("%d of the two fix rounds pushed: %v", n,
			w.count(`SELECT count(*) FROM runs WHERE id = $1 AND push_result @> '{"results":[{"status":"rejected"}]}'`, fix2.ID))
	}
	var rounds []string
	for _, in := range w.lux.Inputs(w.luxRunsOf("implement")[0].ID) {
		if strings.HasPrefix(in, "## A new round") {
			rounds = append(rounds, in)
		}
	}
	if len(rounds) != 2 {
		t.Fatalf("%d round messages, want 2: %q", len(rounds), rounds)
	}
	// Only the reviewer ran between rounds: the head each round starts from
	// is the writer's own last push, so there is no range to name.
	for i, r := range rounds {
		if !strings.Contains(r, "## Review findings") || strings.Contains(r, "git log ") {
			t.Errorf("round %d: want the findings and no range:\n%s", i+1, r)
		}
	}
	if !strings.Contains(rounds[0], "## How you fix") || strings.Contains(rounds[1], "## How you fix") {
		t.Errorf("the fix contract: want it in the first round only:\n%s\n---\n%s", rounds[0], rounds[1])
	}
	// The finding: one, its attempts one per fix round, not doubled.
	if n := w.count(`SELECT count(*) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("%d findings, want one", n)
	}
	if n := w.count(`SELECT max(fix_attempts) FROM review_findings WHERE task_id = $1`, wi); n != 2 {
		t.Fatalf("fix attempts %d, want one per round", n)
	}
}

// A fixer whose spec would differ from the implementer's — here a project
// secret given to fixers only, which lux would not add to the
// implementer's Run — is never the implementer resumed: a fresh fixer
// given its conversation, its run.created saying why.
func TestAFixerOnAnotherSpecDoesNotResumeTheImplementer(t *testing.T) {
	w := resuming(t)
	// Not a scripted model: lux is given dude's prompt itself.
	w.onModel("implementer", "llm-implement")
	w.grant("FIX_TOKEN", "fix-test-not-a-real-token", "fixer")
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a fix completed", func() bool {
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, wi) >= 1
	})
	writers := w.writerRows(wi)
	if len(writers) != 2 {
		t.Fatalf("writers: %+v; want the implementer and one fixer", writers)
	}
	impl, fix := writers[0], writers[1]
	if fix.Continues != impl.ID || fix.How != delivery.ContinuesReplay {
		t.Fatalf("the fixer %+v; want a replay of the implementer %s", fix, impl.ID)
	}
	luxFix := w.luxRunsOf("fix")
	if len(luxFix) != 1 || !strings.Contains(luxFix[0].Prompt(), "## The conversation so far") {
		t.Fatalf("%d fixers submitted; want one given the implementer's conversation", len(luxFix))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'fresh' = 'spec_changed'`, fix.ID); n != 1 {
		t.Error("the fresh fixer's run.created does not say its spec changed")
	}
	if _, ok := projectSecretsOf(specOf(t, luxFix[0].Spec))["FIX_TOKEN"]; !ok {
		t.Error("the fresh fixer was not given its secret")
	}
}

// A writer whose round ended with the head somewhere it did not push (the
// simplifier published on top) is not taken back up when its lux Run
// checked the repository out on a branch, as the implementer's does: lux
// fast-forwards only a detached checkout to a commit, so the resume could
// never land. The fix after the simplifier's diff's review is a replay of
// the last fixer — itself the implementer's lux Run resumed — and the
// implementer's lux Run is not resumed again.
func TestAWriterOnItsBranchIsReplayedWhenTheHeadMovedPastItsPush(t *testing.T) {
	w := resuming(t)
	w.onModel("reviewer", "llm-reviewer")
	findingOnTheSimplification(w)
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	writers := w.writerRows(wi)
	if len(writers) != 3 {
		t.Fatalf("writers: %+v; want the implementer, its continuation and the fixer after the simplifier", writers)
	}
	impl, fix1, fix2 := writers[0], writers[1], writers[2]
	if fix1.Continues != impl.ID || fix1.How != delivery.ContinuesResume {
		t.Fatalf("the first fix %+v; want the implementer resumed", fix1)
	}
	if fix2.Continues != fix1.ID || fix2.How != delivery.ContinuesReplay {
		t.Fatalf("the fix after the simplifier %+v; want a replay of %s", fix2, fix1.ID)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'fresh' = 'head_moved'`, fix2.ID); n != 1 {
		t.Error("the replay's run.created does not say the head moved")
	}
	if luxImpl, luxFix := w.luxRunsOf("implement"), w.luxRunsOf("fix"); len(luxImpl) != 1 || luxImpl[0].Resumed != 1 || len(luxFix) != 1 {
		t.Fatalf("%d implementers (resumed %d), %d fixers submitted; want the implementer resumed once, one fresh fixer",
			len(luxImpl), luxImpl[0].Resumed, len(luxFix))
	}
	if n := w.count(`SELECT count(*) FROM events WHERE task_id = $1 AND event_type IN ('run.round.unsynced', 'run.restarted')`, wi); n != 0 {
		t.Errorf("%d unsynced or restarted events; want the replay chosen at the round's creation", n)
	}
}

// findingOnTheSimplification has the review of the simplifier's diff (the
// task's third review decided) raise simplifierFinding, and the next pass
// it.
func findingOnTheSimplification(w *world) {
	scripted := w.lux.Decide
	var reviews atomic.Int32
	w.lux.Decide = func(spec map[string]any) fakelux.Behaviour {
		labels, _ := spec["labels"].(map[string]any)
		if labels["dude.phase"] == "review" {
			switch reviews.Add(1) {
			case 3:
				return fakelux.Behaviour{Reply: simplifierFinding}
			case 4:
				return fakelux.Behaviour{Reply: "Reviewed the fix of the simplification.\n\n" + fakeagent.Verdict}
			}
		}
		return scripted(spec)
	}
}

// A fixer submitted fresh (here its own secret) is checked out detached at
// the commit it started from, so after the simplifier moved the branch
// past its push its lux Run is resumed and fast-forwarded there: a pull
// request comment's fix takes that fixer back up (prFix goes on from the
// attempt's last writer).
func TestAPullRequestFixResumesTheLastFixerFastForwardedPastTheSimplifier(t *testing.T) {
	w := resuming(t)
	w.grant("FIX_TOKEN", "fix-test-not-a-real-token", "fixer")
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	var simplified string
	_ = w.owner.QueryRow(context.Background(), `SELECT heads->'target'->>'sha' FROM runs WHERE task_id = $1 AND phase = 'simplify'`, wi).Scan(&simplified)
	if simplified == "" {
		t.Fatal("the simplifier published nothing")
	}
	w.gh.Comment(1, "alice", "Please rename the greeting.")
	w.until("the pull request's fix completed", func() bool {
		w.sync()
		return w.fixes(wi) == 1 && w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND pr_feedback <> '[]'::jsonb
			AND status = 'completed'`, wi) == 1
	})
	writers := w.writerRows(wi)
	if len(writers) != 3 {
		t.Fatalf("writers: %+v; want the implementer, the fresh fixer and the pull request's fix", writers)
	}
	fix1, prFix := writers[1], writers[2]
	if fix1.Continues != writers[0].ID || fix1.How != delivery.ContinuesReplay {
		t.Fatalf("the first fix %+v; want a replay of the implementer (its own secret)", fix1)
	}
	if prFix.Continues != fix1.ID || prFix.How != delivery.ContinuesResume {
		t.Fatalf("the pull request's fix %+v; want %s resumed", prFix, fix1.ID)
	}
	luxFix := w.luxRunsOf("fix")
	if len(luxFix) != 1 || luxFix[0].Resumed != 1 {
		t.Fatalf("%d fixers submitted; want one, resumed once", len(luxFix))
	}
	if syncs := luxFix[0].ResumeSyncs; len(syncs) != 1 || len(syncs[0]) != 1 || syncs[0][0].Ref != simplified || syncs[0][0].Mode != lux.SyncFastForward {
		t.Fatalf("resume syncs %v; want a fast-forward to the simplifier's %s", syncs, simplified)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type IN ('run.round.unsynced', 'run.restarted')`, prFix.ID); n != 0 {
		t.Fatal("the resumed fixer's checkout did not sync")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND base_shas->>'target' = $2
		AND push_result @> '{"results":[{"status":"pushed"}]}'`, prFix.ID, simplified); n != 1 {
		t.Fatal("the resumed fixer did not start from the simplifier's head and push")
	}
}

func TestAResumedPullRequestFixHasOnlyThisRoundsCheckExcerpt(t *testing.T) {
	w := resuming(t)
	w.grant("FIX_TOKEN", "fix-test-not-a-real-token", "fixer")
	w.gh.SetChecks("run:success")
	wi := w.task()
	w.names(wi, w.repoID)
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	downloads := httptest.NewServer(w.gh.LogDownloads())
	t.Cleanup(downloads.Close)
	w.gh.Set(func(s *fakegithub.Server) {
		s.CheckRuns = map[int64]fakegithub.CheckRun{77: fakegithub.NoisyActionsJob()}
	})
	w.gh.SetJobLog(77, "current-round: AssertionError: 3 != 2\n##[error]Process completed with exit code 1.\n", downloads.URL)
	w.gh.SetChecks("run:failure")
	w.until("the resumed CI fix", func() bool {
		w.sync()
		return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix'
			AND pr_feedback <> '[]'::jsonb AND continues_how = 'resume'`, wi) == 1
	})
	var text string
	if err := w.owner.QueryRow(context.Background(), `SELECT d.text FROM directives d JOIN runs r ON r.id = d.run_id
		WHERE r.task_id = $1 AND r.phase = 'fix' AND r.pr_feedback <> '[]'::jsonb`, wi).Scan(&text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current-round: AssertionError: 3 != 2", "the excerpt below is all you have for this round",
		"Any earlier files under `/workspace/.dude/checks/` are from earlier rounds and are not this failure."} {
		if !strings.Contains(text, want) {
			t.Errorf("directive lacks %q:\n%s", want, text)
		}
	}
	for _, noise := range []string{"Node.js 20", "ubuntu-latest", "warning", "notice", "The last "} {
		if strings.Contains(text, noise) {
			t.Errorf("directive contains %q:\n%s", noise, text)
		}
	}
	t.Logf("round-2 directive:\n%s", text)
}

// A continuing writer's round message waits for its resume's sync for at
// most CheckoutSyncWait from when lux reported it running again, however
// long the resume took: a slow placement whose sync then lands goes on;
// a sync never reported is replaced once the wait is over.
func TestAContinuingWritersSyncIsWaitedForFromItsResume(t *testing.T) {
	for _, landed := range []bool{true, false} {
		t.Run(map[bool]string{true: "slow resume, sync lands", false: "sync never reported"}[landed], func(t *testing.T) {
			w := resuming(t)
			w.syncer.CheckoutSyncWait = time.Second
			if landed {
				// Far longer than the test runs: only a bound taken from the
				// backdated round message below can expire.
				w.syncer.CheckoutSyncWait = time.Minute
			}
			wi := w.task()
			w.names(wi, w.repoID)
			// The test goroutine polls on w.owner; the hook gets its own.
			hookConn, err := pgx.ConnectConfig(context.Background(), w.owner.Config())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hookConn.Close(context.Background()) })
			w.lux.BeforeStart(func(id string) {
				if !w.fixStarts(id) {
					return
				}
				if !landed {
					// Placed well after the handover queued the round message.
					time.Sleep(2 * time.Second)
					return
				}
				// A resume placed an hour after the handover queued the round
				// message: the sync's wait runs from the resume, not from it.
				tag, err := hookConn.Exec(context.Background(), `UPDATE directives SET created_at = now() - interval '1 hour'
					WHERE run_id = (SELECT id FROM runs WHERE task_id = $1 AND phase = 'fix' AND continues_how = 'resume')`, wi)
				if err != nil || tag.RowsAffected() == 0 {
					t.Errorf("backdating the continuation's round message: %v, %d rows", err, tag.RowsAffected())
				}
			})
			released := make(chan struct{})
			release := sync.OnceFunc(func() { close(released) })
			t.Cleanup(func() { w.lux.BeforeEvent(nil); release() })
			var held atomic.Bool
			// Pumps completed by w.until below.
			var pumps atomic.Int64
			w.lux.BeforeEvent(func(runID string, _ int64, typ string) {
				if typ != "git.sync" || !w.fixStarts(runID) || !held.CompareAndSwap(false, true) {
					return
				}
				if !landed {
					<-released
					return
				}
				// Held until the continuation is recorded running and a whole
				// pump (its sweep delivering directives) has run since: an
				// expired bound replaces it before the sync lands.
				for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					if time.Now().After(deadline) {
						t.Error("the continuation was never recorded running")
						return
					}
					var running bool
					_ = hookConn.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM runs r
						JOIN run_resumes rr ON rr.run_id = r.id AND rr.running_at IS NOT NULL
						WHERE r.task_id = $1 AND r.phase = 'fix' AND r.continues_how = 'resume' AND r.lux_state = 'running')`, wi).Scan(&running)
					if running {
						break
					}
				}
				for after := pumps.Load() + 2; pumps.Load() < after; time.Sleep(20 * time.Millisecond) {
				}
			})
			w.start(wi, resumingPolicy())
			w.until("a pull request", func() bool { pumps.Add(1); return len(w.gh.Pulls()) == 1 })
			writers := w.writerRows(wi)
			if len(writers) < 2 || writers[1].How != delivery.ContinuesResume {
				t.Fatalf("writers: %+v; want the implementer resumed for the first fix", writers)
			}
			cont := writers[1]
			replaced := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by IS NOT NULL`, cont.ID)
			restarted := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.restarted' AND payload->>'by' = 'dude'`, cont.ID)
			if landed {
				if replaced != 0 || restarted != 0 || w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'completed'`, cont.ID) != 1 {
					t.Fatalf("the continuation whose sync landed was replaced (%d) or restarted (%d), or did not complete", replaced, restarted)
				}
				return
			}
			if replaced != 1 || restarted != 1 {
				t.Fatalf("the continuation whose sync was never reported: replaced %d, restarted %d; want both", replaced, restarted)
			}
			if luxFix := w.luxRunsOf("fix"); len(luxFix) != 1 {
				t.Fatalf("%d fixers submitted; want the fresh one in its slot", len(luxFix))
			}
		})
	}
}

// A fixer whose own settings differ from the implementer's — its tier, its
// notes, its current saved prompt — is a replay given the implementer's
// conversation, never the implementer resumed. A fixer prompt saved once
// and since cleared (its latest version empty) is no difference.
func TestAFixerWithItsOwnSettingsIsAReplayOfTheImplementer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(w *world)
		fresh bool
	}{
		{"tier", func(w *world) { w.onModel("fixer", "llm-fixer") }, true},
		{"notes", func(w *world) {
			mustExec(t, w.owner, `UPDATE projects SET agent_models = jsonb_set(agent_models, '{fixer}',
				COALESCE(agent_models->'fixer', '{}'::jsonb) || '{"context":"Fix only what is named."}') WHERE id = $1`, w.project)
		}, true},
		{"current prompt", func(w *world) {
			mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, role, body, created_by)
				VALUES ('pv_fix_'||$1, $1, 'fixer', 'FIXER STANCE', 'key_x')`, w.org)
		}, true},
		{"project replace, empty", func(w *world) {
			// An empty replace still drops the org's prompt for the fixer.
			mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by)
				VALUES ('pv_fix_proj_'||$1, $1, $2, 'fixer', 'replace', '', 'key_x')`, w.org, w.project)
		}, true},
		{"cleared prompt", func(w *world) {
			mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, role, body, created_by, created_at)
				VALUES ('pv_fix_old_'||$1, $1, 'fixer', 'FIXER STANCE', 'key_x', now() - interval '1 hour'),
				       ('pv_fix_new_'||$1, $1, 'fixer', '', 'key_x', now())`, w.org)
			mustExec(t, w.owner, `INSERT INTO prompt_versions (id, organization_id, project_id, role, mode, body, created_by)
				VALUES ('pv_fix_proj_'||$1, $1, $2, 'fixer', 'add', '', 'key_x')`, w.org, w.project)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := resuming(t)
			// Not a scripted model: lux is given dude's prompt itself.
			w.onModel("implementer", "llm-implement")
			tc.setup(w)
			wi := w.task()
			w.names(wi, w.repoID)
			w.start(wi, resumingPolicy())
			w.until("a fix completed", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase = 'fix' AND status = 'completed'`, wi) >= 1
			})
			writers := w.writerRows(wi)
			if len(writers) != 2 {
				t.Fatalf("writers: %+v; want the implementer and one fixer", writers)
			}
			impl, fix := writers[0], writers[1]
			if !tc.fresh {
				if fix.Continues != impl.ID || fix.How != delivery.ContinuesResume || len(w.luxRunsOf("fix")) != 0 {
					t.Fatalf("the fixer %+v; want the implementer %s resumed", fix, impl.ID)
				}
				return
			}
			if fix.Continues != impl.ID || fix.How != delivery.ContinuesReplay {
				t.Fatalf("the fixer %+v; want a replay of the implementer %s", fix, impl.ID)
			}
			if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.created' AND payload->>'fresh' = 'spec_changed'`, fix.ID); n != 1 {
				t.Error("the replay's run.created does not say its spec changed")
			}
			luxFix := w.luxRunsOf("fix")
			if len(luxFix) != 1 || w.luxRunsOf("implement")[0].Resumed != 0 {
				t.Fatalf("%d fixers submitted, implementer resumed %d times; want one fresh fixer and no resume", len(luxFix), w.luxRunsOf("implement")[0].Resumed)
			}
			if !strings.Contains(luxFix[0].Prompt(), "## The conversation so far") {
				t.Fatal("the fresh fixer was not given the implementer's conversation")
			}
		})
	}
}

// A writer kept for its next round leaves its per-Run branch at publish;
// when its finish then fails — here a second repository's task branch
// cannot be moved — the branch it left is deleted with the Run.
func TestAWriterThatFailsAfterPublishingHasItsBranchDeleted(t *testing.T) {
	w := resuming(t)
	wi := w.task()
	w.addWeb(wi, "write")
	task := delivery.BranchFor(wi, 1)
	w.web.Set(func(s *fakegithub.Server) {
		s.Intercept = func(r *http.Request) int {
			if r.Method != http.MethodGet && r.Method != http.MethodDelete && strings.Contains(r.URL.Path, "/git/refs") &&
				(strings.HasSuffix(r.URL.Path, "/heads/"+task) || r.Method == http.MethodPost) {
				return http.StatusUnprocessableEntity
			}
			return 0
		}
	})
	w.start(wi, resumingPolicy())
	var impl fixRow
	w.until("an implementer failed after target's publish", func() bool {
		for _, r := range w.writerRows(wi) {
			if r.Phase == "implement" && w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'failed'`, r.ID) == 1 {
				impl = r
				return true
			}
		}
		return false
	})
	if impl.Branch == "" || w.gh.SHA(task) == "" {
		t.Fatalf("implementer %+v, target's task branch at %q; want target published before the failure", impl, w.gh.SHA(task))
	}
	w.until("the failed writer's branch deleted", func() bool { return w.gh.SHA(impl.Branch) == "" })
}

func specOf(t *testing.T, raw []byte) lux.Spec {
	t.Helper()
	var spec lux.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	return spec
}

// A continuing fixer whose checkout its resume's fast-forward cannot bring
// to the round's head (here: a tracked file changed in it, which lux's
// fast-forward keeps, as it keeps a diverged one) never hears its round:
// dude replaces it in its slot with a fresh fixer given the implementer's
// conversation. The finding's attempt is counted once, and the delivery
// goes on.
func TestAFixerWhoseCheckoutDidNotSyncIsReplacedByAFreshOne(t *testing.T) {
	w := resuming(t)
	// Not a scripted model: lux is given dude's prompt itself, which
	// fakelux's scripted agent plays all the same.
	w.onModel("implementer", "llm-implement")
	wi := w.task()
	w.names(wi, w.repoID)
	var once sync.Once
	w.lux.BeforeStart(func(id string) {
		if !w.fixStarts(id) {
			return
		}
		once.Do(func() {
			var spec lux.Spec
			_ = json.Unmarshal(w.luxRunsOf("implement")[0].Spec, &spec)
			work, err := w.lux.Checkout(id, spec.Git.Repositories[0].Name)
			if err == nil {
				err = os.WriteFile(filepath.Join(work, "FACTORY.md"), []byte("edited in the checkout\n"), 0o644)
			}
			if err != nil {
				t.Error(err)
			}
		})
	})
	w.start(wi, resumingPolicy())
	w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
	writers := w.writerRows(wi)
	if len(writers) != 3 {
		t.Fatalf("writers: %+v; want the implementer, its continuation and the fresh fixer", writers)
	}
	impl, cont, fresh := writers[0], writers[1], writers[2]
	if cont.Continues != impl.ID || cont.How != delivery.ContinuesResume || fresh.Continues != impl.ID || fresh.How != delivery.ContinuesReplay {
		t.Fatalf("writers %+v; want the continuation replaced by a replay of the implementer", writers)
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.round.unsynced' AND payload->>'status' = 'kept'`, cont.ID); n != 1 {
		t.Fatal("no run.round.unsynced (kept) on the continuation: replaced for another reason")
	}
	if n := w.count(`SELECT count(*) FROM runs WHERE id = $1 AND status = 'aborted' AND replaced_by = $2`, cont.ID, fresh.ID); n != 1 {
		t.Fatal("the continuation was not replaced in its slot")
	}
	if n := w.count(`SELECT count(*) FROM events WHERE run_id = $1 AND event_type = 'run.restarted' AND payload->>'by' = 'dude'`, cont.ID); n != 1 {
		t.Fatal("no run.restarted by dude")
	}
	for _, in := range w.lux.Inputs(w.luxRunsOf("implement")[0].ID) {
		if strings.HasPrefix(in, "## A new round") {
			t.Fatalf("the unsynced continuation heard its round:\n%s", in)
		}
	}
	if luxFix := w.luxRunsOf("fix"); len(luxFix) != 1 || !strings.Contains(luxFix[0].Prompt(), "## The conversation so far") {
		t.Fatalf("%d fixers submitted; want one given the implementer's conversation", len(luxFix))
	}
	if n := w.count(`SELECT max(fix_attempts) FROM review_findings WHERE task_id = $1`, wi); n != 1 {
		t.Fatalf("fix attempts %d, want 1", n)
	}
	if n := w.count(`SELECT count(*) FROM questions WHERE task_id = $1`, wi); n != 0 {
		t.Fatalf("%d questions asked of a person, want the delivery not escalated", n)
	}
}

// A kept writer's per-Run branch outlives its publish only while the
// writer is kept: once the task lets it go, RetireCompleted terminates
// its lux Run and deletes the branch. With the policy off the branch goes
// at publish, as before, and nothing is kept.
func TestAKeptWritersBranchIsDeletedWhenItIsLetGo(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "resuming", false: "off"}[on], func(t *testing.T) {
			var w *world
			if on {
				w = resuming(t)
			} else {
				w = newWorld(t)
			}
			wi := w.task()
			w.names(wi, w.repoID)
			p := resumingPolicy()
			p.ResumeRounds = on
			w.start(wi, p)
			w.until("a pull request", func() bool { return len(w.gh.Pulls()) == 1 })
			impl := w.writerRows(wi)[0]
			if impl.Branch == "" {
				t.Fatal("the implementer has no push branch")
			}
			kept := w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') AND keep`, wi)
			if !on {
				if kept != 0 || w.gh.SHA(impl.Branch) != "" {
					t.Fatalf("policy off: %d writers kept, branch at %q; want none, deleted", kept, w.gh.SHA(impl.Branch))
				}
				return
			}
			last := w.writerRows(wi)
			holder := last[len(last)-1]
			if kept != 1 || w.gh.SHA(holder.Branch) == "" {
				t.Fatalf("%d writers kept, branch %q at %q; want the last kept with its branch", kept, holder.Branch, w.gh.SHA(holder.Branch))
			}
			if err := w.app.InOrg(context.Background(), w.org, func(tx pgx.Tx) error {
				_, err := delivery.SetTaskStatusTx(context.Background(), tx, w.org, w.project, wi, "", "done", "merged")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			w.until("the writer retired and its branch deleted", func() bool {
				return w.count(`SELECT count(*) FROM runs WHERE task_id = $1 AND phase IN ('implement', 'fix') AND lux_stop_reason = 'cancel'
					AND lux_run_id IS NOT NULL`, wi) == 1 && w.gh.SHA(holder.Branch) == ""
			})
		})
	}
}
